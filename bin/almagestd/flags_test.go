package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/coder/websocket"
)

func TestFlagsStorageAndAdminAPI(t *testing.T) {
	root := t.TempDir()
	manager, err := openKVManagerInStorage(root, bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstManager := manager
	t.Cleanup(func() { _ = firstManager.Close() })
	if got := manager.flagsSnapshot(); len(got.Flags) != 0 {
		t.Fatalf("missing flags file = %#v", got)
	}
	events := make([]api.Message, 0, 1)
	session := (&AdminAPI{kv: manager}).newAPISession(nil, nil, func(msg api.Message) error {
		events = append(events, msg)
		return nil
	})
	if err := session.sendFlagsSnapshot(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Surface != api.SurfaceFlags || events[0].Method != string(api.EventFlagsSnapshot) {
		t.Fatalf("initial flags event = %#v", events)
	}
	initial, err := api.DecodePayload[api.FlagsDocument](events[0].Payload)
	if err != nil || len(initial.Flags) != 0 {
		t.Fatalf("initial flags = %#v, %v", initial, err)
	}
	document := api.FlagsDocument{Flags: map[string]bool{"sample": true, "off": false}}
	response, err := session.handleMessage(mustRequest(t, "1", api.SurfaceFlags, string(api.MethodFlagsReplace), document))
	if err != nil || response.Error != nil {
		t.Fatalf("replace flags = %#v, %v", response, err)
	}
	data, err := os.ReadFile(filepath.Join(root, flagsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var stored api.FlagsDocument
	if err := json.Unmarshal(data, &stored); err != nil || !stored.Flags["sample"] || stored.Flags["off"] {
		t.Fatalf("stored flags = %#v, %v", stored, err)
	}
	response, err = session.handleMessage(mustRequest(t, "2", api.SurfaceFlags, string(api.MethodFlagsSnapshot), nil))
	if err != nil || response.Error != nil {
		t.Fatalf("get flags = %#v, %v", response, err)
	}
	got, err := api.DecodePayload[api.FlagsDocument](response.Result)
	if err != nil || !got.Flags["sample"] {
		t.Fatalf("flags response = %#v, %v", got, err)
	}
	for _, invalid := range []string{`null`, `{"unknown":true}`, `{"flags":{"sample":"yes"}}`} {
		request := api.Message{Version: api.Version, Kind: api.MessageRequest, ID: "invalid", Surface: api.SurfaceFlags,
			Method: string(api.MethodFlagsReplace), Payload: json.RawMessage(invalid)}
		response, err := session.handleMessage(request)
		if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
			t.Fatalf("invalid flags %s response = %#v, %v", invalid, response, err)
		}
	}
	if !manager.flagsSnapshot().Flags["sample"] {
		t.Fatal("invalid API replacement changed flags")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err = openKVManagerInStorage(root, bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if !manager.flagsSnapshot().Flags["sample"] {
		t.Fatal("restart did not load flags")
	}
	if err := os.WriteFile(filepath.Join(root, flagsFileName), []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.reloadFlags(); err == nil {
		t.Fatal("invalid flags file was accepted")
	}
	if !manager.flagsSnapshot().Flags["sample"] {
		t.Fatal("failed reload changed active flags")
	}
	if err := os.Remove(filepath.Join(root, flagsFileName)); err != nil {
		t.Fatal(err)
	}
	if err := manager.reloadFlags(); err != nil || len(manager.flagsSnapshot().Flags) != 0 {
		t.Fatalf("missing flags reload = %#v, %v", manager.flagsSnapshot(), err)
	}
	if err := manager.replaceFlags(context.Background(), document); err != nil {
		t.Fatal(err)
	}
}

func TestMarkOpenedPreservesOtherFlags(t *testing.T) {
	root := t.TempDir()
	manager, err := openKVManagerInStorage(root, bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	session := (&AdminAPI{kv: manager}).newAPISession(nil, nil, func(api.Message) error { return nil })
	initial := api.FlagsDocument{Flags: map[string]bool{"sample": true}, LaunchedBefore: true}
	if err := manager.replaceFlags(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"docs", "snapshots"} {
		response, err := session.handleMessage(mustRequest(t, page, api.SurfaceFlags, string(api.MethodFlagsMarkOpened), api.FlagsMarkOpenedRequest{Page: page}))
		if err != nil || response.Error != nil {
			t.Fatalf("mark %s opened = %#v, %v", page, response, err)
		}
	}
	stored, err := readFlags(filepath.Join(root, flagsFileName))
	if err != nil || !stored.DocsOpened || !stored.SnapshotsOpened || !stored.LaunchedBefore || !stored.Flags["sample"] {
		t.Fatalf("stored flags = %#v, %v", stored, err)
	}
	response, err := session.handleMessage(mustRequest(t, "invalid", api.SurfaceFlags, string(api.MethodFlagsMarkOpened), api.FlagsMarkOpenedRequest{Page: "other"}))
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("invalid page = %#v, %v", response, err)
	}
}

func TestFlagsOverridesOnReadAndWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, flagsFileName)
	if err := os.WriteFile(path, []byte(`{"flags":{"fixed":false,"free":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := openKVManagerInStorage(root, bboltKVManagerOptions{FlagOverrides: map[string]bool{"fixed": true, "disabled": false}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	assertFlags := func(document api.FlagsDocument) {
		t.Helper()
		if !document.Flags["fixed"] || document.Flags["disabled"] || !document.Flags["free"] {
			t.Fatalf("effective flags = %#v", document.Flags)
		}
	}
	assertFlags(manager.flagsSnapshot())
	if err := manager.replaceFlags(context.Background(), api.FlagsDocument{Flags: map[string]bool{"fixed": false, "disabled": true, "free": true}}); err != nil {
		t.Fatal(err)
	}
	stored, err := readFlags(path)
	if err != nil {
		t.Fatal(err)
	}
	assertFlags(stored)
	if err := os.WriteFile(path, []byte(`{"flags":{"fixed":false,"disabled":true,"free":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.reloadFlags(); err != nil {
		t.Fatal(err)
	}
	assertFlags(manager.flagsSnapshot())
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := manager.reloadFlags(); err != nil {
		t.Fatal(err)
	}
	if got := manager.flagsSnapshot().Flags; !got["fixed"] || got["disabled"] {
		t.Fatalf("missing file flags = %#v", got)
	}
}

func TestProtectedFlagsConfig(t *testing.T) {
	for _, parse := range []func([]byte, []byte) (*Config, error){ParseConfig, ParseIsolatedConfig} {
		config, err := parse([]byte(`{}`), []byte(`{"Flags":{"enabled":true,"disabled":false}}`))
		if err != nil {
			t.Fatal(err)
		}
		if !config.Protected.Flags["enabled"] || config.Protected.Flags["disabled"] {
			t.Fatalf("protected flags = %#v", config.Protected.Flags)
		}
	}
}

func TestFlagsReplacementBlockedWhenRecoveryRequired(t *testing.T) {
	manager, err := openKVManagerInStorage(t.TempDir(), bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	admin := &AdminAPI{kv: manager}
	admin.mutations.setInitialRecoveryRequired(errors.New("failed rollback"))
	session := admin.newAPISession(nil, nil, func(api.Message) error { return nil })
	response, err := session.handleMessage(mustRequest(t, "write", api.SurfaceFlags, string(api.MethodFlagsReplace), api.FlagsDocument{
		Flags: map[string]bool{"blocked": true},
	}))
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorUnavailable {
		t.Fatalf("flags write during recovery = %#v, %v", response, err)
	}
	if _, err := os.Stat(manager.flagsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked write created flags file: %v", err)
	}
	response, err = session.handleMessage(mustRequest(t, "read", api.SurfaceFlags, string(api.MethodFlagsSnapshot), nil))
	if err != nil || response.Error != nil {
		t.Fatalf("flags read during recovery = %#v, %v", response, err)
	}
}

func TestFlagsSentOnNewAdminStream(t *testing.T) {
	manager, err := openKVManagerInStorage(t.TempDir(), bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.replaceFlags(context.Background(), api.FlagsDocument{Flags: map[string]bool{"connected": true}}); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = client.Close() })
	go (&AdminAPI{kv: manager}).serveAPIStream(ctx, server, nil, nil, nil)
	codec := api.NewLimitedReadWriteCodec(client, api.SnapshotAPIMessageMaxBytes)
	for i := 0; i < 2; i++ {
		message, err := codec.Read()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if message.Surface != api.SurfaceAdmin || message.Method != string(api.EventAdminProjectVersionStatus) {
				t.Fatalf("first event = %#v", message)
			}
			continue
		}
		if message.Surface != api.SurfaceFlags || message.Method != string(api.EventFlagsSnapshot) {
			t.Fatalf("flags event = %#v", message)
		}
		document, err := api.DecodePayload[api.FlagsDocument](message.Payload)
		if err != nil || !document.Flags["connected"] {
			t.Fatalf("flags event payload = %#v, %v", document, err)
		}
	}
}

func TestFlagsSentOnNewAdminWebsocket(t *testing.T) {
	manager, err := openKVManagerInStorage(t.TempDir(), bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.replaceFlags(context.Background(), api.FlagsDocument{Flags: map[string]bool{"web": true}}); err != nil {
		t.Fatal(err)
	}
	admin := &AdminAPI{kv: manager, webAllowCrossOrigin: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.serveWebsocket(r.Context(), w, r, nil, nil, nil)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	for i := 0; i < 2; i++ {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		message, err := api.UnmarshalMessage(data)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if message.Surface != api.SurfaceFlags || message.Method != string(api.EventFlagsSnapshot) {
				t.Fatalf("flags websocket event = %#v", message)
			}
			document, err := api.DecodePayload[api.FlagsDocument](message.Payload)
			if err != nil || !document.Flags["web"] {
				t.Fatalf("flags websocket payload = %#v, %v", document, err)
			}
		}
	}
}

func TestInvalidSnapshotFlagsRejectedBeforeStartupStorageChanges(t *testing.T) {
	service, _, _, _, storagePath := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "flags validation")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	archive := appendSnapshotArchive(t, exported.Archive, func(writer *zip.Writer) error {
		return writeZipBytes(writer, snapshotFilesPrefix+flagsFileName, []byte(`{"unknown":true}`), 0o600, time.Now())
	})
	if _, err := service.Import(context.Background(), bytes.NewReader(archive), nil); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("import invalid flags = %v", err)
	}
	current := []byte(`{"flags":{"current":true}}`)
	path := filepath.Join(storagePath, flagsFileName)
	if err := os.WriteFile(path, current, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restoreStartupSnapshot(context.Background(), storagePath, bytes.NewReader(archive), nil); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("startup restore invalid flags = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, current) {
		t.Fatalf("startup restore changed live flags = %s, %v", data, err)
	}
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), "bad flags"); err == nil {
		t.Fatal("snapshot creation accepted malformed live flags")
	}
	if _, err := service.Restore(context.Background(), created.UUID); err == nil {
		t.Fatal("snapshot restore accepted malformed recovery flags")
	}
	journalPath, recoveryPath := restoreStatePaths(storagePath)
	for _, path := range []string{journalPath, recoveryPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed restore left recovery state %s: %v", path, err)
		}
	}
}

func TestFlagsFileSizeBoundary(t *testing.T) {
	manager, err := openKVManagerInStorage(t.TempDir(), bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	base, err := json.Marshal(api.FlagsDocument{Flags: map[string]bool{"": true}})
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("x", flagsMaxBytes-len(base)-1)
	valid := api.FlagsDocument{Flags: map[string]bool{name: true}}
	if err := manager.replaceFlags(context.Background(), valid); err != nil {
		t.Fatalf("maximum-size flags file: %v", err)
	}
	if data, err := os.ReadFile(manager.flagsPath); err != nil || len(data) != flagsMaxBytes {
		t.Fatalf("flags file size = %d, %v", len(data), err)
	}
	if _, err := readFlags(manager.flagsPath); err != nil {
		t.Fatalf("read maximum-size flags file: %v", err)
	}
	tooLarge := api.FlagsDocument{Flags: map[string]bool{name + "x": true}}
	if err := manager.replaceFlags(context.Background(), tooLarge); err == nil {
		t.Fatal("oversized flags file was accepted")
	}
	if len(manager.flagsSnapshot().Flags) != 1 || !manager.flagsSnapshot().Flags[name] {
		t.Fatal("failed oversized replacement changed active flags")
	}
}

func TestFlagsReplaceRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires an optional Windows privilege")
	}
	manager, err := openKVManagerInStorage(t.TempDir(), bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	target := filepath.Join(t.TempDir(), "outside.json")
	before := []byte(`{"flags":{"outside":true}}`)
	if err := os.WriteFile(target, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, manager.flagsPath); err != nil {
		t.Fatal(err)
	}
	if err := manager.replaceFlags(context.Background(), api.FlagsDocument{}); err == nil {
		t.Fatal("flags replacement accepted a symlink")
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("flags replacement changed symlink target: %s, %v", after, err)
	}
}

func TestRestoreSnapshotWithoutFlagsKeepsLiveFlags(t *testing.T) {
	service, manager, _, _, storagePath := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "no flags")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.replaceFlags(context.Background(), api.FlagsDocument{Flags: map[string]bool{"new": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restore(context.Background(), created.UUID); err != nil {
		t.Fatal(err)
	}
	if got := manager.flagsSnapshot(); !got.Flags["new"] {
		t.Fatalf("restored flags = %#v, want existing value", got)
	}
	if data, err := os.ReadFile(filepath.Join(storagePath, flagsFileName)); err != nil || !bytes.Contains(data, []byte(`"new":true`)) {
		t.Fatalf("flags file after restore = %s, %v; want existing value", data, err)
	}
}

func TestFailedRestoreRollsBackFlagsFileAndCache(t *testing.T) {
	for _, present := range []bool{false, true} {
		name := "missing"
		if present {
			name = "present"
		}
		t.Run(name, func(t *testing.T) {
			service, manager, _, _, _ := newSnapshotTestService(t, false)
			if err := manager.replaceFlags(context.Background(), api.FlagsDocument{Flags: map[string]bool{"snapshot": true}}); err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(context.Background(), "flags rollback")
			if err != nil {
				t.Fatal(err)
			}
			if present {
				if err := manager.replaceFlags(context.Background(), api.FlagsDocument{Flags: map[string]bool{"current": true}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(manager.flagsPath); err != nil {
					t.Fatal(err)
				}
				if err := manager.reloadFlags(); err != nil {
					t.Fatal(err)
				}
			}
			injected := errors.New("injected failure after flags apply")
			service.restore.hooks.afterDataApplied = func() error {
				if !manager.flagsSnapshot().Flags["snapshot"] {
					t.Error("snapshot flags were not applied before failure")
				}
				return injected
			}
			if _, err := service.Restore(context.Background(), created.UUID); !errors.Is(err, injected) {
				t.Fatalf("restore error = %v", err)
			}
			got := manager.flagsSnapshot()
			if got.Flags["snapshot"] || got.Flags["current"] != present {
				t.Fatalf("flags cache after rollback = %#v", got)
			}
			if present {
				disk, err := readFlags(manager.flagsPath)
				if err != nil || !disk.Flags["current"] || disk.Flags["snapshot"] {
					t.Fatalf("flags file after rollback = %#v, %v", disk, err)
				}
			} else if _, err := os.Stat(manager.flagsPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rollback retained new flags file: %v", err)
			}
		})
	}
}

func TestStartupSnapshotWithoutFlagsKeepsExistingFile(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "no flags")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	path := filepath.Join(destination, flagsFileName)
	data := []byte("{\n  \"flags\": {\"keep\": true}\n}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := restoreStartupSnapshot(context.Background(), destination, bytes.NewReader(exported.Archive), nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("startup restore replaced or touched omitted flags file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("startup restore changed omitted flags: %q, %v", got, err)
	}
	manager, err := openKVManagerInStorage(destination, bboltKVManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if !manager.flagsSnapshot().Flags["keep"] {
		t.Fatal("startup did not load preserved flags")
	}
}
