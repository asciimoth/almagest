package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tlspolicy"
	"github.com/asciimoth/pasta/pasta"
)

type snapshotTestWriter struct {
	configPath    string
	protectedPath string
	writes        [][]byte
	err           error
}

func (w *snapshotTestWriter) WriteConfig(data []byte) error {
	w.writes = append(w.writes, bytes.Clone(data))
	if w.err != nil {
		return w.err
	}
	return os.WriteFile(w.configPath, data, 0o600)
}

func (w *snapshotTestWriter) ConfigFilePath() string {
	return w.configPath
}

func (w *snapshotTestWriter) ProtectedConfigFilePath() string {
	return w.protectedPath
}

func TestSnapshotCreateListAndRestoreCompleteDaemonState(t *testing.T) {
	service, manager, engine, writer, storagePath := newSnapshotTestService(t, false)
	ctx := context.Background()

	initialID, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node")
	if !ok {
		t.Fatal("initial saved-node is missing")
	}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("saved-kv", storage.KValue{Data: []byte("before"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("saved-secret", "before-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("saved-secret", []string{"saved-node"}); err != nil {
		t.Fatal(err)
	}
	dnsKey, err := manager.setDNSRecord("saved-cache", "", api.DNSRecordInput{
		Name: "snapshot.test.", Type: "A", Class: "IN", TTL: 3600, Value: "192.0.2.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(storagePath, "future-store", "nested.txt")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("before-file"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storagePath, snapshotDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(storagePath, snapshotDirectoryName, "must-survive")
	if err := os.WriteFile(markerPath, []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 9, 9, 12, 30, 0, 123456789, time.UTC)
	service.now = func() time.Time { return createdAt }
	first, err := service.Create(ctx, "duplicate name")
	if err != nil {
		t.Fatal(err)
	}
	if first.Timestamp != createdAt.UnixNano() || first.Name != "duplicate name" {
		t.Fatalf("created snapshot = %#v", first)
	}
	assertSnapshotArchiveIsSanitized(t, storagePath, first.UUID)

	service.now = func() time.Time { return createdAt.Add(time.Second) }
	second, err := service.Create(ctx, "duplicate name")
	if err != nil {
		t.Fatal(err)
	}
	if second.UUID == first.UUID {
		t.Fatal("snapshot UUIDs are not unique")
	}
	listed, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].UUID != second.UUID || listed[1].UUID != first.UUID {
		t.Fatalf("listed snapshots = %#v, want newest first", listed)
	}
	renamed, err := service.Rename(first.UUID, "renamed snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.UUID != first.UUID || renamed.Timestamp != first.Timestamp || renamed.Name != "renamed snapshot" {
		t.Fatalf("renamed snapshot = %#v", renamed)
	}
	listed, err = service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[1] != renamed {
		t.Fatalf("listed renamed snapshots = %#v", listed)
	}

	if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("saved-kv", storage.KValue{Data: []byte("after"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("saved-secret", "after-secret"); err != nil {
		t.Fatal(err)
	}
	if err := manager.dropDNSCache("saved-cache"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("after-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	unwantedPath := filepath.Join(storagePath, "not-in-snapshot.txt")
	if err := os.WriteFile(unwantedPath, []byte("remove me"), 0o600); err != nil {
		t.Fatal(err)
	}
	protectedBefore := []byte("protected-current-secret")
	if err := os.WriteFile(writer.protectedPath, protectedBefore, 0o600); err != nil {
		t.Fatal(err)
	}

	response, err := service.Restore(ctx, first.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if !response.ConfigWritten || response.ConfigWriteError != "" || response.Snapshot != renamed {
		t.Fatalf("restore response = %#v", response)
	}
	restoredID, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node")
	if !ok || restoredID == initialID {
		t.Fatalf("restored node ID = %d, found=%t; want a fully rebuilt node", restoredID, ok)
	}
	assertKVString(t, manager, "saved-kv", "before")
	secret, err := manager.GetSecret("saved-secret", "saved-node")
	if err != nil || secret.Value != "before-secret" {
		t.Fatalf("restored secret = %#v, err=%v", secret, err)
	}
	dns, err := manager.dnsKeysSnapshot("saved-cache")
	if err != nil || !dns.Exists || len(dns.Keys) != 1 || dns.Keys[0].Key != dnsKey {
		t.Fatalf("restored DNS cache = %#v, err=%v", dns, err)
	}
	if data, err := os.ReadFile(dataPath); err != nil || string(data) != "before-file" {
		t.Fatalf("restored future file = %q, err=%v", data, err)
	}
	if _, err := os.Stat(unwantedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file absent from snapshot still exists: %v", err)
	}
	if data, err := os.ReadFile(markerPath); err != nil || string(data) != "marker" {
		t.Fatalf("excluded snapshots content = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(writer.protectedPath); err != nil || !bytes.Equal(data, protectedBefore) {
		t.Fatalf("protected config changed: %q, err=%v", data, err)
	}
	if len(writer.writes) < 2 {
		t.Fatalf("config writes = %d, want mutation and restore writes", len(writer.writes))
	}
	lastWrite := writer.writes[len(writer.writes)-1]
	if bytes.Contains(lastWrite, []byte("Protected")) || bytes.Contains(lastWrite, []byte("embedded-secret")) {
		t.Fatalf("restored config write contains protected data: %s", lastWrite)
	}

	if err := service.Delete(second.UUID); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].UUID != first.UUID {
		t.Fatalf("snapshots after delete = %#v, want only first snapshot", listed)
	}
	if _, err := os.Stat(filepath.Join(storagePath, snapshotDirectoryName, second.UUID+".zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted snapshot archive still exists: %v", err)
	}
}

func TestSnapshotRestoreKeepsConfigInMemoryWhenWriteFailsOrIsReadOnly(t *testing.T) {
	for _, test := range []struct {
		name     string
		readOnly bool
		writeErr error
	}{
		{name: "write failure", writeErr: errors.New("disk is read-only")},
		{name: "stdin config", readOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, engine, writer, _ := newSnapshotTestService(t, test.readOnly)
			info, err := service.Create(context.Background(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
				t.Fatal(err)
			}
			writer.err = test.writeErr
			response, err := service.Restore(context.Background(), info.UUID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node"); !ok {
				t.Fatal("restored config is not active in memory")
			}
			if response.ConfigWritten {
				t.Fatalf("restore response = %#v, want no config write", response)
			}
			if test.writeErr != nil && !strings.Contains(response.ConfigWriteError, test.writeErr.Error()) {
				t.Fatalf("restore response = %#v, want write error", response)
			}
			if test.readOnly && response.ConfigWriteError != "" {
				t.Fatalf("read-only restore response = %#v, want no attempted write", response)
			}
		})
	}
}

func TestSnapshotCreateDoesNotReplaceExistingUUID(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	service.random = bytes.NewReader(make([]byte, 32))
	first, err := service.Create(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), "second"); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("duplicate UUID create error = %v, want invalid snapshot", err)
	}
	listed, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].UUID != first.UUID || listed[0].Name != "first" {
		t.Fatalf("snapshots after UUID collision = %#v", listed)
	}
}

func TestSnapshotAdminAPIRejectsUnknownAndMalformedUUIDs(t *testing.T) {
	service, _, engine, _, _ := newSnapshotTestService(t, false)
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)

	malformed := mustRequest(t, "1", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotUUIDRequest{UUID: "../escape"})
	response, err := session.handleMessage(malformed)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("malformed UUID response = %#v, err=%v", response, err)
	}
	unknown := mustRequest(t, "2", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotUUIDRequest{UUID: "11111111-1111-4111-8111-111111111111"})
	response, err = session.handleMessage(unknown)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("unknown UUID response = %#v, err=%v", response, err)
	}

	malformed = mustRequest(t, "3", api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: "../escape"})
	response, err = session.handleMessage(malformed)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("malformed delete UUID response = %#v, err=%v", response, err)
	}
	unknown = mustRequest(t, "4", api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: "11111111-1111-4111-8111-111111111111"})
	response, err = session.handleMessage(unknown)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("unknown delete UUID response = %#v, err=%v", response, err)
	}

	malformed = mustRequest(t, "5", api.SurfaceSnapshots, string(api.MethodSnapshotsRename), api.SnapshotsRenameRequest{UUID: "../escape", Name: "name"})
	response, err = session.handleMessage(malformed)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("malformed rename UUID response = %#v, err=%v", response, err)
	}
	unknown = mustRequest(t, "6", api.SurfaceSnapshots, string(api.MethodSnapshotsRename), api.SnapshotsRenameRequest{
		UUID: "11111111-1111-4111-8111-111111111111",
		Name: "name",
	})
	response, err = session.handleMessage(unknown)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("unknown rename UUID response = %#v, err=%v", response, err)
	}
}

func TestSnapshotAdminAPIDeletesArchive(t *testing.T) {
	service, _, engine, _, storagePath := newSnapshotTestService(t, false)
	info, err := service.Create(context.Background(), "delete through API")
	if err != nil {
		t.Fatal(err)
	}
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)
	renameRequest := mustRequest(t, "1", api.SurfaceSnapshots, string(api.MethodSnapshotsRename), api.SnapshotsRenameRequest{
		UUID: info.UUID,
		Name: "renamed through API",
	})
	renameResponse, err := session.handleMessage(renameRequest)
	if err != nil || renameResponse.Error != nil {
		t.Fatalf("rename response = %#v, err=%v", renameResponse, err)
	}
	renamed, err := api.DecodePayload[api.SnapshotInfo](renameResponse.Result)
	if err != nil || renamed.Name != "renamed through API" || renamed.UUID != info.UUID {
		t.Fatalf("renamed snapshot = %#v, err=%v", renamed, err)
	}
	request := mustRequest(t, "2", api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: info.UUID})
	response, err := session.handleMessage(request)
	if err != nil || response.Error != nil {
		t.Fatalf("delete response = %#v, err=%v", response, err)
	}
	if _, err := os.Stat(filepath.Join(storagePath, snapshotDirectoryName, info.UUID+".zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted snapshot archive still exists: %v", err)
	}
}

func TestSnapshotExtractionRejectsSymlinkParent(t *testing.T) {
	outside := t.TempDir()
	stage := t.TempDir()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	if err := writeZipBytes(writer, "escape", []byte(outside), os.ModeSymlink|0o777, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writeZipBytes(writer, "escape/written.txt", []byte("unsafe"), 0o600, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if err := extractSnapshotFile(reader.File[0], stage, reader.File[0].Name); err != nil {
		t.Fatalf("extract symlink: %v", err)
	}
	err = extractSnapshotFile(reader.File[1], stage, reader.File[1].Name)
	if !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("extract through symlink error = %v, want invalid snapshot", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "written.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive extraction escaped staging: %v", err)
	}
}

func TestValidateBboltDatabaseRejectsNonDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), kvDBFileName)
	if err := os.WriteFile(path, []byte("not a bbolt database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateBboltDatabase(path); err == nil {
		t.Fatal("invalid bbolt database was accepted")
	}
}

func newSnapshotTestService(t *testing.T, readOnly bool) (*snapshotService, *bboltKVManager, almagest.Engine, *snapshotTestWriter, string) {
	t.Helper()
	storagePath := t.TempDir()
	configPath := filepath.Join(storagePath, "config", "alm.hujson")
	protectedPath := filepath.Join(storagePath, "config", "protected_alm.hujson")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	mainData := []byte(`{
		"nodes": {"saved-node": {"Class": "test/AdminAPIBatchNode"}},
		"Protected": {"inline": "embedded-secret"}
	}`)
	protectedData := []byte(`{"Secrets":{"protected-only":{"Value":"never-archive","Nodes":["saved-node"]}}}`)
	if err := os.WriteFile(configPath, mainData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protectedPath, protectedData, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := ParseConfig(mainData, protectedData)
	if err != nil {
		t.Fatal(err)
	}
	builtinCA, err := loadOrCreateTLSBuiltinCA(storagePath, defaultTLSBuiltinCAOptions(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	immutableSecrets, err := config.Protected.ImmutableSecrets()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := openKVManagerInStorage(storagePath, bboltKVManagerOptions{
		ImmutableSecrets: immutableSecrets,
		BuiltinCA:        builtinCA,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	engine, err := almagest.BuildEngine(
		context.Background(), config.Main, logging.NewDebugLogFactory(128), &sysnetdebug.System{},
		manager, manager, manager, manager, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	writer := &snapshotTestWriter{configPath: configPath, protectedPath: protectedPath}
	autosaver := newConfigAutosaver(engine, config, writer, logging.NewDebugLogFactory(32).EngineLogger(), time.Millisecond)
	service := newSnapshotService(
		storagePath, autosaver, engine, manager, config, writer, readOnly,
		logging.NewDebugLogFactory(32).EngineLogger(),
		tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) { return nil, nil }),
	)
	return service, manager, engine, writer, storagePath
}

func assertKVString(t *testing.T, manager *bboltKVManager, key, expected string) {
	t.Helper()
	err := manager.DefaultStore().View(context.Background(), func(tx storage.KVReadTx) error {
		value, found, err := tx.Get(key)
		if err != nil {
			return err
		}
		if !found || string(value.Data) != expected {
			t.Fatalf("KV %q = %#v, found=%t; want %q", key, value, found, expected)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotArchiveIsSanitized(t *testing.T, storagePath, uuid string) {
	t.Helper()
	reader, err := zip.OpenReader(filepath.Join(storagePath, snapshotDirectoryName, uuid+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close() //nolint:errcheck
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, snapshotFilesPrefix+snapshotDirectoryName+"/") {
			t.Fatalf("snapshot contains its own snapshot storage: %s", file.Name)
		}
		if file.Name == snapshotFilesPrefix+"config/protected_alm.hujson" {
			t.Fatalf("snapshot contains protected config: %s", file.Name)
		}
		if file.Name != snapshotConfigName {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(entry)
		_ = entry.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("Protected")) || bytes.Contains(data, []byte("embedded-secret")) {
			t.Fatalf("snapshot config contains protected content: %s", data)
		}
	}
}
