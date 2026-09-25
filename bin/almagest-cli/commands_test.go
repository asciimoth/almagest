package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/pasta/pasta"
)

type fakeTransport struct {
	reads    []api.Message
	writes   []api.Message
	closed   bool
	readErr  error
	writeErr error
	closeErr error
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func (t *fakeTransport) Read(context.Context) (api.Message, error) {
	if len(t.reads) == 0 {
		if t.readErr != nil {
			return api.Message{}, t.readErr
		}
		return api.Message{}, io.EOF
	}
	msg := t.reads[0]
	t.reads = t.reads[1:]
	return msg, nil
}

func (t *fakeTransport) Write(_ context.Context, msg api.Message) error {
	if t.writeErr != nil {
		return t.writeErr
	}
	t.writes = append(t.writes, msg)
	return nil
}

func (t *fakeTransport) Close() error {
	t.closed = true
	return t.closeErr
}

func TestClientRequestAndEventErrorPaths(t *testing.T) {
	want := errors.New("transport failed")
	if _, err := newClient(&fakeTransport{writeErr: want}).request(context.Background(), api.SurfaceAdmin, "ping", nil); !errors.Is(err, want) {
		t.Fatalf("write error = %v, want %v", err, want)
	}
	if _, err := newClient(&fakeTransport{readErr: want}).request(context.Background(), api.SurfaceAdmin, "ping", nil); !errors.Is(err, want) {
		t.Fatalf("read error = %v, want %v", err, want)
	}
	response := api.Message{Kind: api.MessageResponse, ID: "1"}
	client := newClient(&fakeTransport{reads: []api.Message{
		{Kind: api.MessageRequest, ID: "ignored"},
		{Kind: api.MessageResponse, ID: "other"},
		response,
	}})
	result, err := client.request(context.Background(), api.SurfaceAdmin, "ping", nil)
	if err != nil || string(result) != "{}" {
		t.Fatalf("empty response = %q, %v", result, err)
	}
	apiFailure := &api.Error{Code: api.ErrorConflict, Message: "busy"}
	client = newClient(&fakeTransport{reads: []api.Message{{Kind: api.MessageResponse, ID: "1", Error: apiFailure}}})
	if _, err := client.request(context.Background(), api.SurfaceAdmin, "ping", nil); err == nil || err.Error() != "conflict: busy" {
		t.Fatalf("API error = %v", err)
	}
	client = newClient(&fakeTransport{readErr: want})
	if _, err := client.nextEvent(context.Background()); !errors.Is(err, want) {
		t.Fatalf("event read error = %v, want %v", err, want)
	}
	client = newClient(&fakeTransport{reads: []api.Message{
		{Kind: api.MessageResponse, ID: "ignored"},
		{Kind: api.MessageEvent, Method: "event"},
	}})
	if event, err := client.nextEvent(context.Background()); err != nil || event.Method != "event" {
		t.Fatalf("event = %#v, %v", event, err)
	}
	if err := newClient(&fakeTransport{closeErr: want}).Close(); !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
}

func TestCommandOutputErrors(t *testing.T) {
	want := errors.New("output failed")
	badJSON := api.Message{Kind: api.MessageResponse, ID: "1", Result: json.RawMessage(`{`)}
	tests := []struct {
		name string
		args []string
		msg  api.Message
		out  io.Writer
	}{
		{name: "request invalid JSON", args: []string{"admin", "ping"}, msg: badJSON, out: io.Discard},
		{name: "request write", args: []string{"admin", "ping"}, msg: mustResponse(t, "1", api.PingResponse{}), out: failingWriter{want}},
		{name: "config get invalid JSON", args: []string{"config", "get"}, msg: badJSON, out: io.Discard},
		{name: "config get write", args: []string{"config", "get"}, msg: mustResponse(t, "1", api.ConfigSnapshot{Text: "data"}), out: failingWriter{want}},
		{name: "dns watch invalid JSON", args: []string{"dns", "watch-caches"}, msg: badJSON, out: io.Discard},
		{name: "dns watch write", args: []string{"dns", "watch-caches"}, msg: mustResponse(t, "1", struct{}{}), out: failingWriter{want}},
		{name: "kv watch invalid JSON", args: []string{"kv", "watch"}, msg: badJSON, out: io.Discard},
		{name: "kv watch write", args: []string{"kv", "watch"}, msg: mustResponse(t, "1", struct{}{}), out: failingWriter{want}},
		{name: "secrets watch invalid JSON", args: []string{"secrets", "watch"}, msg: badJSON, out: io.Discard},
		{name: "secrets watch write", args: []string{"secrets", "watch"}, msg: mustResponse(t, "1", struct{}{}), out: failingWriter{want}},
		{name: "tls watch invalid JSON", args: []string{"tls", "watch"}, msg: badJSON, out: io.Discard},
		{name: "tls watch write", args: []string{"tls", "watch"}, msg: mustResponse(t, "1", struct{}{}), out: failingWriter{want}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeTransport{reads: []api.Message{test.msg}}
			err := run(context.Background(), test.args, nil, test.out, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
				return transport, nil
			})
			if err == nil {
				t.Fatalf("run(%q) succeeded", test.args)
			}
		})
	}
	event := mustEvent(t, api.SurfaceLog, string(api.EventLogEntry), api.LogEntry{Message: "entry"})
	if err := watchEvents(context.Background(), newClient(&fakeTransport{reads: []api.Message{event}}), failingWriter{want}, func(api.Message) bool { return true }); !errors.Is(err, want) {
		t.Fatalf("watch output error = %v, want %v", err, want)
	}
}

func TestCLIInputReaderBranches(t *testing.T) {
	want := errors.New("read failed")
	if _, err := readConfigInput("-", nil); err == nil {
		t.Fatal("config stdin nil was accepted")
	}
	if _, err := readConfigInput("-", failingReader{want}); !errors.Is(err, want) {
		t.Fatalf("config stdin error = %v", err)
	}
	configPath := t.TempDir() + "/config.hujson"
	if err := os.WriteFile(configPath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := readConfigInput(configPath, nil); err != nil || string(data) != "config" {
		t.Fatalf("config file = %q, %v", data, err)
	}
	if _, err := readConfigInput(configPath+"-missing", nil); err == nil {
		t.Fatal("missing config file was accepted")
	}

	if _, err := readJSONInput("", failingReader{want}); !errors.Is(err, want) {
		t.Fatalf("JSON stdin error = %v", err)
	}
	if _, err := readJSONInput("{", nil); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if raw, err := readJSONInput("", strings.NewReader(`{"ok":true}`)); err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("JSON stdin = %q, %v", raw, err)
	}

	if _, err := readTLSInput("value", "file", nil, "--value", "--file"); err == nil {
		t.Fatal("two TLS inputs were accepted")
	}
	if got, err := readTLSInput("value", "", nil, "--value", "--file"); err != nil || got != "value" {
		t.Fatalf("TLS direct input = %q, %v", got, err)
	}
	if _, err := readTLSInput("", "-", nil, "--value", "--file"); err == nil {
		t.Fatal("TLS nil stdin was accepted")
	}
	if _, err := readTLSInput("", "-", failingReader{want}, "--value", "--file"); !errors.Is(err, want) {
		t.Fatalf("TLS stdin error = %v", err)
	}
	tlsPath := t.TempDir() + "/input.pem"
	if err := os.WriteFile(tlsPath, []byte("pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readTLSInput("", tlsPath, nil, "--value", "--file"); err != nil || got != "pem" {
		t.Fatalf("TLS file input = %q, %v", got, err)
	}
	if _, err := readTLSInput("", tlsPath+"-missing", nil, "--value", "--file"); err == nil {
		t.Fatal("missing TLS file was accepted")
	}
	if _, err := readTLSInput("", "", nil, "--value", "--file"); err == nil {
		t.Fatal("missing TLS input was accepted")
	}
}

func TestSnapshotArchiveIOBranches(t *testing.T) {
	want := errors.New("read failed")
	if _, err := readSnapshotArchive("-", nil); err == nil {
		t.Fatal("snapshot nil stdin was accepted")
	}
	if _, err := readSnapshotArchive("-", failingReader{want}); !errors.Is(err, want) {
		t.Fatalf("snapshot stdin error = %v", err)
	}
	path := t.TempDir() + "/snapshot.zip"
	if err := os.WriteFile(path, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if archive, err := readSnapshotArchive(path, nil); err != nil || string(archive) != "zip" {
		t.Fatalf("snapshot file = %q, %v", archive, err)
	}
	if _, err := readSnapshotArchive(path+"-missing", nil); err == nil {
		t.Fatal("missing snapshot file was accepted")
	}
	if _, err := readBoundedSnapshotArchive(io.LimitReader(zeroReader{}, api.SnapshotArchiveMaxBytes+1)); err == nil {
		t.Fatal("oversized snapshot archive was accepted")
	}

	uuid := "11111111-1111-4111-8111-111111111111"
	if _, err := resolveSnapshotUUID("export", uuid, []string{uuid}); err == nil {
		t.Fatal("duplicate snapshot UUID was accepted")
	}
	if _, err := resolveSnapshotUUID("export", "", []string{"one", "two"}); err == nil {
		t.Fatal("two snapshot UUIDs were accepted")
	}
	if got, err := resolveSnapshotUUID("export", uuid, nil); err != nil || got != uuid {
		t.Fatalf("flag snapshot UUID = %q, %v", got, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

func TestRunPingPrintsPrettyResult(t *testing.T) {
	event, err := api.NewEvent(api.SurfaceAdmin, string(api.EventAdminProjectVersionStatus), api.ProjectVersionStatus{Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := api.NewResponse("1", api.PingResponse{Nonce: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransport{reads: []api.Message{event, resp}}

	var stdout, stderr bytes.Buffer
	err = run(context.Background(), []string{"--url", "unix://tmp/admin.sock", "admin", "ping", "--nonce", "abc"}, nil, &stdout, &stderr, func(_ context.Context, ep endpoint) (apiTransport, error) {
		if ep.path != "/tmp/admin.sock" {
			t.Fatalf("endpoint path = %q", ep.path)
		}
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if !ft.closed {
		t.Fatal("transport was not closed")
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceAdmin || ft.writes[0].Method != string(api.MethodAdminPing) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	req, err := api.DecodePayload[api.PingRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Nonce != "abc" {
		t.Fatalf("nonce = %q, want abc", req.Nonce)
	}
	want := "{\n  \"nonce\": \"abc\"\n}\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunRawSendsJSONPayload(t *testing.T) {
	resp, err := api.NewResponse("1", map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransport{reads: []api.Message{resp}}
	var stdout, stderr bytes.Buffer

	err = run(context.Background(), []string{"raw", "--surface", "engine", "--method", "workspace_snapshot", "--payload", `{"workspace":"main"}`}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(ft.writes[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["workspace"] != "main" {
		t.Fatalf("payload = %#v, want workspace main", payload)
	}
	if !strings.Contains(stdout.String(), `"ok": true`) {
		t.Fatalf("stdout = %q, want ok result", stdout.String())
	}
}

func TestRunConfigGetPrintsHuJSON(t *testing.T) {
	const text = "{\n  // editable\n  nodes: {},\n}\n"
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", api.ConfigSnapshot{
		Text:     text,
		Revision: "revision-1",
	})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"config", "get"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("config get failed: %v\nstderr: %s", err, stderr.String())
	}
	if stdout.String() != text {
		t.Fatalf("stdout = %q, want HuJSON text", stdout.String())
	}
	if len(ft.writes) != 1 || ft.writes[0].Surface != api.SurfaceConfig || ft.writes[0].Method != string(api.MethodConfigSnapshot) {
		t.Fatalf("request = %#v, want config snapshot", ft.writes)
	}
}

func TestRunConfigApplyReadsStdinAndSendsRevision(t *testing.T) {
	const text = "{nodes: {}}\n"
	response := api.ConfigUpdateResponse{Snapshot: api.ConfigSnapshot{Text: text, Revision: "revision-2"}}
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", response)}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"config", "apply", "--revision", "revision-1", "-"}, strings.NewReader(text), &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("config apply failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 1 || ft.writes[0].Surface != api.SurfaceConfig || ft.writes[0].Method != string(api.MethodConfigUpdate) {
		t.Fatalf("request = %#v, want config update", ft.writes)
	}
	req, err := api.DecodePayload[api.ConfigUpdateRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Text != text || req.Revision != "revision-1" {
		t.Fatalf("config update request = %#v", req)
	}
	if !strings.Contains(stdout.String(), `"revision": "revision-2"`) {
		t.Fatalf("stdout = %q, want update response", stdout.String())
	}
}

func TestRunSnapshotCommandsSendAllSnapshotRequests(t *testing.T) {
	info := api.SnapshotInfo{
		UUID:      "11111111-1111-4111-8111-111111111111",
		Timestamp: 1788955200000000000,
		Name:      "same name",
	}
	transport := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", info),
		mustResponse(t, "1", api.SnapshotsListResponse{Snapshots: []api.SnapshotInfo{info}}),
		mustResponse(t, "1", api.SnapshotsExportResponse{Snapshot: info, Archive: []byte("ZIP archive")}),
		mustResponse(t, "1", api.SnapshotInfo{UUID: info.UUID, Timestamp: info.Timestamp, Name: "imported"}),
		mustResponse(t, "1", api.SnapshotInfo{UUID: info.UUID, Timestamp: info.Timestamp, Name: "renamed"}),
		mustResponse(t, "1", api.SnapshotsRestoreResponse{Snapshot: info, ConfigWritten: true}),
		mustResponse(t, "1", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer
	dial := func(context.Context, endpoint) (apiTransport, error) { return transport, nil }

	if err := run(context.Background(), []string{"snapshots", "create", "--name", "same name"}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot create failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run(context.Background(), []string{"snapshots", "list"}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot list failed: %v\nstderr: %s", err, stderr.String())
	}
	archivePath := t.TempDir() + "/" + info.UUID + ".zip"
	if err := run(context.Background(), []string{"snapshots", "export", "--output", archivePath, info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot export failed: %v\nstderr: %s", err, stderr.String())
	}
	if data, err := os.ReadFile(archivePath); err != nil || string(data) != "ZIP archive" {
		t.Fatalf("exported archive = %q, err=%v", data, err)
	}
	if err := run(context.Background(), []string{"snapshots", "import", "--name", "imported", archivePath}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot import failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run(context.Background(), []string{"snapshots", "rename", "--name", "renamed", info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot rename failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run(context.Background(), []string{"snapshots", "restore", info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot restore failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run(context.Background(), []string{"snapshots", "delete", "--uuid", info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot delete failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(transport.writes) != 7 {
		t.Fatalf("snapshot writes = %d, want 7", len(transport.writes))
	}
	wantMethods := []api.SnapshotsMethod{
		api.MethodSnapshotsCreate,
		api.MethodSnapshotsList,
		api.MethodSnapshotsExport,
		api.MethodSnapshotsImport,
		api.MethodSnapshotsRename,
		api.MethodSnapshotsRestore,
		api.MethodSnapshotsDelete,
	}
	for index, message := range transport.writes {
		if message.Surface != api.SurfaceSnapshots || message.Method != string(wantMethods[index]) {
			t.Fatalf("snapshot request %d = %#v", index, message)
		}
	}
	createRequest, err := api.DecodePayload[api.SnapshotsCreateRequest](transport.writes[0].Payload)
	if err != nil || createRequest.Name != info.Name {
		t.Fatalf("create request = %#v, err=%v", createRequest, err)
	}
	exportRequest, err := api.DecodePayload[api.SnapshotUUIDRequest](transport.writes[2].Payload)
	if err != nil || exportRequest.UUID != info.UUID {
		t.Fatalf("export request = %#v, err=%v", exportRequest, err)
	}
	importRequest, err := api.DecodePayload[api.SnapshotsImportRequest](transport.writes[3].Payload)
	if err != nil || string(importRequest.Archive) != "ZIP archive" || importRequest.Name == nil || *importRequest.Name != "imported" {
		t.Fatalf("import request = %#v, err=%v", importRequest, err)
	}
	renameRequest, err := api.DecodePayload[api.SnapshotsRenameRequest](transport.writes[4].Payload)
	if err != nil || renameRequest.UUID != info.UUID || renameRequest.Name != "renamed" {
		t.Fatalf("rename request = %#v, err=%v", renameRequest, err)
	}
	restoreRequest, err := api.DecodePayload[api.SnapshotUUIDRequest](transport.writes[5].Payload)
	if err != nil || restoreRequest.UUID != info.UUID {
		t.Fatalf("restore request = %#v, err=%v", restoreRequest, err)
	}
	deleteRequest, err := api.DecodePayload[api.SnapshotUUIDRequest](transport.writes[6].Payload)
	if err != nil || deleteRequest.UUID != info.UUID {
		t.Fatalf("delete request = %#v, err=%v", deleteRequest, err)
	}
	if !strings.Contains(stdout.String(), `"config_written": true`) {
		t.Fatalf("snapshot output = %q", stdout.String())
	}
}

func TestRunKVSetSendsRequest(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", api.KVValueSnapshot{
		Found: true,
		Value: &api.KVValue{Data: "value", Encoding: "string", Size: 5, String: true},
	})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"kv", "set",
		"--plugin", "plug",
		"--path", "bucket/nested",
		"--key", "key",
		"--data", "value",
		"--hidden",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceKV || ft.writes[0].Method != string(api.MethodKVSet) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	req, err := api.DecodePayload[api.KVSetRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Store.Kind != api.KVStorePlugin || req.Store.Name != "plug" {
		t.Fatalf("store = %#v, want plugin plug", req.Store)
	}
	if len(req.Path) != 2 || req.Path[0] != "bucket" || req.Path[1] != "nested" {
		t.Fatalf("path = %#v, want bucket/nested", req.Path)
	}
	if req.Key != "key" || req.Value.Data != "value" || req.Value.Encoding != "string" || !req.Value.Hidden || !req.Value.String {
		t.Fatalf("payload = %#v, want hidden string value", req)
	}
}

func TestRunKVSnapshotAndMutationCommands(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		method api.KVMethod
	}{
		{name: "stores", args: []string{"kv", "stores"}, method: api.MethodKVStoresSnapshot},
		{name: "entries", args: []string{"kv", "entries", "--path", "one/two"}, method: api.MethodKVEntriesSnapshot},
		{name: "get", args: []string{"kv", "get", "--key", "key"}, method: api.MethodKVValueSnapshot},
		{name: "ensure", args: []string{"kv", "ensure", "--path", "one"}, method: api.MethodKVEnsure},
		{name: "delete", args: []string{"kv", "delete", "--key", "key"}, method: api.MethodKVDelete},
		{name: "drop", args: []string{"kv", "drop", "--path", "one"}, method: api.MethodKVDrop},
		{name: "unsubscribe", args: []string{"kv", "unsubscribe", "--subscription-id", "7"}, method: api.MethodKVUnsubscribe},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeTransport{reads: []api.Message{mustResponse(t, "1", map[string]any{"ok": true})}}
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), test.args, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
				return transport, nil
			})
			if err != nil {
				t.Fatalf("run failed: %v; stderr=%s", err, stderr.String())
			}
			if len(transport.writes) != 1 || transport.writes[0].Method != string(test.method) {
				t.Fatalf("writes = %#v", transport.writes)
			}
		})
	}
}

func TestSubscribeAndWatchKVPrintsSnapshotAndAcceptedEvents(t *testing.T) {
	ignored, err := api.NewEvent(api.SurfaceDNS, string(api.EventDNSCachesUpdate), map[string]any{"ignored": true})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := api.NewEvent(api.SurfaceKV, string(api.EventKVStoresUpdate), map[string]any{"updated": true})
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", map[string]any{"subscription_id": 9}), ignored, accepted,
	}}
	client := newClient(transport)
	var stdout bytes.Buffer
	err = subscribeAndWatchKV(context.Background(), client, api.MethodKVSubscribeStores, nil, &stdout, func(msg api.Message) bool {
		return msg.Surface == api.SurfaceKV && msg.Method == string(api.EventKVStoresUpdate)
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("watch error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"subscription_id": 9`) || !strings.Contains(stdout.String(), `"updated":true`) || strings.Contains(stdout.String(), "ignored") {
		t.Fatalf("watch output = %q", stdout.String())
	}
	if err := client.Close(); err != nil || !transport.closed {
		t.Fatalf("Close = %v, closed=%t", err, transport.closed)
	}
}

func TestSubscribeAndWatchKVRejectsInvalidSnapshot(t *testing.T) {
	response := api.Message{Kind: api.MessageResponse, ID: "1", Result: json.RawMessage(`{invalid`)}
	client := newClient(&fakeTransport{reads: []api.Message{response}})
	if err := subscribeAndWatchKV(context.Background(), client, api.MethodKVSubscribeStores, nil, io.Discard, func(api.Message) bool { return true }); err == nil {
		t.Fatal("invalid snapshot succeeded")
	}
}

func TestRunDNSSetSendsReadableRecordRequest(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", api.DNSSetResponse{
		Key: "example.test.|28|1",
		Snapshot: api.DNSValueSnapshot{
			Found: true,
			Value: &api.DNSValue{JSON: `{"Answers":[]}`, Size: 14},
		},
	})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"dns", "set",
		"--cache", "manual",
		"--key", "example.test.|1|1",
		"--name", "example.test.",
		"--type", "aaaa",
		"--ttl", "120",
		"--value", "2001:db8::44",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceDNS || ft.writes[0].Method != string(api.MethodDNSSet) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	req, err := api.DecodePayload[api.DNSSetRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Cache != "manual" || req.Key != "example.test.|1|1" {
		t.Fatalf("target = %#v, want manual cache and old key", req)
	}
	if req.Record.Name != "example.test." || req.Record.Type != "AAAA" || req.Record.Class != "IN" || req.Record.TTL != 120 || req.Record.Value != "2001:db8::44" {
		t.Fatalf("record = %#v, want normalized AAAA record", req.Record)
	}
	if !strings.Contains(stdout.String(), `"key": "example.test.|28|1"`) {
		t.Fatalf("stdout = %q, want set response key", stdout.String())
	}
}

func TestRunDNSGetAndDropSendCacheRequests(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.DNSValueSnapshot{Found: true, Value: &api.DNSValue{Size: 1}}),
		mustResponse(t, "1", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"dns", "get", "--cache", "manual", "--key", "example.test.|1|1"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("get failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{"dns", "drop", "--cache", "manual"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("drop failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 2 {
		t.Fatalf("writes = %d, want get and drop", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceDNS || ft.writes[0].Method != string(api.MethodDNSValueSnapshot) {
		t.Fatalf("get request = %#v", ft.writes[0])
	}
	valueReq, err := api.DecodePayload[api.DNSValueRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if valueReq.Cache != "manual" || valueReq.Key != "example.test.|1|1" {
		t.Fatalf("value request = %#v, want manual/example", valueReq)
	}
	if ft.writes[1].Surface != api.SurfaceDNS || ft.writes[1].Method != string(api.MethodDNSDrop) {
		t.Fatalf("drop request = %#v", ft.writes[1])
	}
	keysReq, err := api.DecodePayload[api.DNSKeysRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if keysReq.Cache != "manual" {
		t.Fatalf("keys request = %#v, want manual", keysReq)
	}
}

func TestRunSecretsSetAndSetNodesSendRequests(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.SecretEntry{Key: "token", Value: "secret", Source: api.SecretSourceMutable}),
		mustResponse(t, "1", api.SecretEntry{
			Key:    "token",
			Value:  "secret",
			Nodes:  []string{"node,one", "node two"},
			Source: api.SecretSourceMutable,
		}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"secrets", "set",
		"--key", "token",
		"--value", "secret",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("set failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"secrets", "set-nodes",
		"--key", "token",
		"--node", "node,one",
		"--node", "node two",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("set-nodes failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 2 {
		t.Fatalf("writes = %d, want set and set-nodes", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceSecrets || ft.writes[0].Method != string(api.MethodSecretsSetValue) {
		t.Fatalf("set request = %#v", ft.writes[0])
	}
	valueReq, err := api.DecodePayload[api.SecretsSetValueRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if valueReq.Key != "token" || valueReq.Value != "secret" {
		t.Fatalf("set payload = %#v, want token secret", valueReq)
	}
	if ft.writes[1].Surface != api.SurfaceSecrets || ft.writes[1].Method != string(api.MethodSecretsSetNodes) {
		t.Fatalf("set-nodes request = %#v", ft.writes[1])
	}
	nodesReq, err := api.DecodePayload[api.SecretsSetNodesRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if nodesReq.Key != "token" || len(nodesReq.Nodes) != 2 || nodesReq.Nodes[0] != "node,one" || nodesReq.Nodes[1] != "node two" {
		t.Fatalf("set-nodes payload = %#v, want exact repeated node names", nodesReq)
	}
}

func TestRunSecretsRenameDeleteAndClearNodesSendRequests(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.SecretEntry{Key: "renamed", Value: "secret", Source: api.SecretSourceMutable}),
		mustResponse(t, "1", api.SecretEntry{Key: "renamed", Value: "secret", Source: api.SecretSourceMutable}),
		mustResponse(t, "1", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"secrets", "rename",
		"--key", "token",
		"--new-key", "renamed",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("rename failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"secrets", "set-nodes",
		"--key", "renamed",
		"--clear-nodes",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("clear nodes failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"secrets", "delete",
		"--key", "renamed",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("delete failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 3 {
		t.Fatalf("writes = %d, want rename, clear, and delete", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceSecrets || ft.writes[0].Method != string(api.MethodSecretsRename) {
		t.Fatalf("rename request = %#v", ft.writes[0])
	}
	renameReq, err := api.DecodePayload[api.SecretsRenameRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if renameReq.Key != "token" || renameReq.NewKey != "renamed" {
		t.Fatalf("rename payload = %#v, want token renamed", renameReq)
	}
	if ft.writes[1].Surface != api.SurfaceSecrets || ft.writes[1].Method != string(api.MethodSecretsSetNodes) {
		t.Fatalf("clear nodes request = %#v", ft.writes[1])
	}
	nodesReq, err := api.DecodePayload[api.SecretsSetNodesRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if nodesReq.Key != "renamed" || len(nodesReq.Nodes) != 0 {
		t.Fatalf("clear nodes payload = %#v, want renamed with no nodes", nodesReq)
	}
	if ft.writes[2].Surface != api.SurfaceSecrets || ft.writes[2].Method != string(api.MethodSecretsDelete) {
		t.Fatalf("delete request = %#v", ft.writes[2])
	}
	deleteReq, err := api.DecodePayload[api.SecretKeyRequest](ft.writes[2].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if deleteReq.Key != "renamed" {
		t.Fatalf("delete payload = %#v, want renamed", deleteReq)
	}
}

func TestRunTLSCertCommandsSendRequests(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.TLSCertEntry{Name: "manual-ca", PEM: certPEM, Source: api.TLSSourceMutable}),
		mustResponse(t, "1", api.TLSCertEntry{Name: "renamed-ca", PEM: certPEM, Source: api.TLSSourceMutable}),
		mustResponse(t, "1", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"tls", "set-cert",
		"--name", "manual-ca",
		"--pem", certPEM,
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("set-cert failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"tls", "rename-cert",
		"--name", "manual-ca",
		"--new-name", "renamed-ca",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("rename-cert failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"tls", "delete-cert",
		"--name", "renamed-ca",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("delete-cert failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 3 {
		t.Fatalf("writes = %d, want set, rename, and delete", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceTLS || ft.writes[0].Method != string(api.MethodTLSSetCert) {
		t.Fatalf("set-cert request = %#v", ft.writes[0])
	}
	setReq, err := api.DecodePayload[api.TLSSetCertRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if setReq.Name != "manual-ca" || setReq.PEM != certPEM {
		t.Fatalf("set-cert payload = %#v, want manual-ca PEM", setReq)
	}
	if ft.writes[1].Surface != api.SurfaceTLS || ft.writes[1].Method != string(api.MethodTLSRenameCert) {
		t.Fatalf("rename-cert request = %#v", ft.writes[1])
	}
	renameReq, err := api.DecodePayload[api.TLSRenameRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if renameReq.Name != "manual-ca" || renameReq.NewName != "renamed-ca" {
		t.Fatalf("rename-cert payload = %#v, want manual-ca renamed-ca", renameReq)
	}
	if ft.writes[2].Surface != api.SurfaceTLS || ft.writes[2].Method != string(api.MethodTLSDeleteCert) {
		t.Fatalf("delete-cert request = %#v", ft.writes[2])
	}
	deleteReq, err := api.DecodePayload[api.TLSNameRequest](ft.writes[2].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if deleteReq.Name != "renamed-ca" {
		t.Fatalf("delete-cert payload = %#v, want renamed-ca", deleteReq)
	}
}

func TestRunTLSPolicyCommandsSendRequests(t *testing.T) {
	specPath := t.TempDir() + "/policy.json"
	specJSON := `{"trust_anchors":[{"cert":"manual-ca","scope":{"dns":[{"domain":"example.test","include_subdomains":true}]}}],"constraints":[{"cert":"issuer-ca","match":"spki","scope":{"ip_prefixes":["192.0.2.0/24"]}}],"pins":[{"kind":"spki","fingerprint":"sha256:abc","scope":{"any":true}}]}`
	if err := os.WriteFile(specPath, []byte(specJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.TLSPolicyEntry{Name: "custom", Valid: true}),
		mustResponse(t, "1", api.TLSPolicyEntry{Name: "renamed", Valid: true}),
		mustResponse(t, "1", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{
		"tls", "set-policy",
		"--name", "custom",
		"--spec-file", specPath,
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("set-policy failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"tls", "rename-policy",
		"--name", "custom",
		"--new-name", "renamed",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("rename-policy failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"tls", "delete-policy",
		"--name", "renamed",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("delete-policy failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 3 {
		t.Fatalf("writes = %d, want set, rename, and delete", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceTLS || ft.writes[0].Method != string(api.MethodTLSSetPolicy) {
		t.Fatalf("set-policy request = %#v", ft.writes[0])
	}
	setReq, err := api.DecodePayload[api.TLSSetPolicyRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if setReq.Name != "custom" || len(setReq.Spec.TrustAnchors) != 1 || setReq.Spec.TrustAnchors[0].Cert != "manual-ca" {
		t.Fatalf("set-policy anchors = %#v, want custom manual-ca anchor", setReq)
	}
	if len(setReq.Spec.Constraints) != 1 || setReq.Spec.Constraints[0].Match != "spki" {
		t.Fatalf("set-policy constraints = %#v, want spki constraint", setReq.Spec.Constraints)
	}
	if len(setReq.Spec.Pins) != 1 || setReq.Spec.Pins[0].Fingerprint != "sha256:abc" || !setReq.Spec.Pins[0].Scope.Any {
		t.Fatalf("set-policy pins = %#v, want sha256:abc any pin", setReq.Spec.Pins)
	}
	if ft.writes[1].Surface != api.SurfaceTLS || ft.writes[1].Method != string(api.MethodTLSRenamePolicy) {
		t.Fatalf("rename-policy request = %#v", ft.writes[1])
	}
	renameReq, err := api.DecodePayload[api.TLSRenameRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if renameReq.Name != "custom" || renameReq.NewName != "renamed" {
		t.Fatalf("rename-policy payload = %#v, want custom renamed", renameReq)
	}
	if ft.writes[2].Surface != api.SurfaceTLS || ft.writes[2].Method != string(api.MethodTLSDeletePolicy) {
		t.Fatalf("delete-policy request = %#v", ft.writes[2])
	}
	deleteReq, err := api.DecodePayload[api.TLSNameRequest](ft.writes[2].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if deleteReq.Name != "renamed" {
		t.Fatalf("delete-policy payload = %#v, want renamed", deleteReq)
	}
}

func TestRunTLSBuiltinCAAndReloadCommandsSendRequests(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.TLSBuiltinCAInfo{Options: api.TLSBuiltinCAOptions{Issuer: "Almagest"}}),
		mustResponse(t, "1", api.TLSBuiltinCAInfo{Options: api.TLSBuiltinCAOptions{Issuer: "Changed", KeyAlgorithm: "rsa", KeyBits: 4096}}),
		mustResponse(t, "1", api.TLSSnapshot{Certs: []api.TLSCertEntry{{Name: "os-ca:system", Source: api.TLSSourceOS}}}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"tls", "builtin-ca"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("builtin-ca failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{
		"tls", "set-builtin-ca",
		"--issuer", "Changed",
		"--validity-days", "365",
		"--key-algorithm", "rsa",
		"--key-bits", "4096",
	}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("set-builtin-ca failed: %v\nstderr: %s", err, stderr.String())
	}
	err = run(context.Background(), []string{"tls", "reload-os-certs"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("reload-os-certs failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 3 {
		t.Fatalf("writes = %d, want builtin, set builtin, and reload", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceTLS || ft.writes[0].Method != string(api.MethodTLSBuiltinCA) {
		t.Fatalf("builtin-ca request = %#v", ft.writes[0])
	}
	if ft.writes[1].Surface != api.SurfaceTLS || ft.writes[1].Method != string(api.MethodTLSSetBuiltinCA) {
		t.Fatalf("set-builtin-ca request = %#v", ft.writes[1])
	}
	setReq, err := api.DecodePayload[api.TLSSetBuiltinCARequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if setReq.Options.Issuer != "Changed" || setReq.Options.ValidityDays != 365 ||
		setReq.Options.KeyAlgorithm != "rsa" || setReq.Options.KeyBits != 4096 {
		t.Fatalf("set-builtin-ca payload = %#v, want configured RSA options", setReq)
	}
	if ft.writes[2].Surface != api.SurfaceTLS || ft.writes[2].Method != string(api.MethodTLSReloadOSCerts) {
		t.Fatalf("reload-os-certs request = %#v", ft.writes[2])
	}
}

func TestRunEngineRemoveNodesSendsBatchRequest(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", struct{}{})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"engine", "remove-nodes", "--workspace", "main", "--ids", "7,8,9"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceEngine || ft.writes[0].Method != string(api.MethodEngineRemoveNodes) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	req, err := api.DecodePayload[api.IDsRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Workspace != "main" || len(req.IDs) != 3 || req.IDs[0] != 7 || req.IDs[2] != 9 {
		t.Fatalf("payload = %#v, want batch node IDs", req)
	}
}

func TestRunEngineSetNodePositionResolvesName(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.WorkspaceSnapshotResponse{Snapshot: api.NewWorkspaceSnapshot(pasta.WorkspaceSnapshot{
			Nodes: map[uint64]pasta.NodeSnapshot{
				42: {Name: "server", Class: "test/server"},
			},
		}, nil)}),
		mustResponse(t, "2", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"engine", "set-node-position", "--name", "server", "--position", "10 20"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 2 {
		t.Fatalf("writes = %d, want workspace snapshot and mutation", len(ft.writes))
	}
	if ft.writes[0].Method != string(api.MethodEngineWorkspaceSnapshot) || ft.writes[1].Method != string(api.MethodEngineSetNodePosition) {
		t.Fatalf("methods = %q, %q", ft.writes[0].Method, ft.writes[1].Method)
	}
	req, err := api.DecodePayload[api.SetNodePositionRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.ID != 42 || req.Position != "10 20" {
		t.Fatalf("payload = %#v, want resolved node 42 position", req)
	}
}

func TestRunEngineSetNodeNameResolvesNameWithNewName(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.WorkspaceSnapshotResponse{Snapshot: api.NewWorkspaceSnapshot(pasta.WorkspaceSnapshot{
			Nodes: map[uint64]pasta.NodeSnapshot{
				42: {Name: "server", Class: "test/server"},
			},
		}, nil)}),
		mustResponse(t, "2", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"engine", "set-node-name", "--name", "server", "--new-name", "renamed"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	req, err := api.DecodePayload[api.SetNodeNameRequest](ft.writes[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.ID != 42 || req.Name != "renamed" {
		t.Fatalf("payload = %#v, want resolved node 42 renamed", req)
	}
}

func TestRunEngineSetNodeNameKeepsIDNameForm(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", struct{}{})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"engine", "set-node-name", "--id", "42", "--name", "renamed"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want mutation only", len(ft.writes))
	}
	req, err := api.DecodePayload[api.SetNodeNameRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.ID != 42 || req.Name != "renamed" {
		t.Fatalf("payload = %#v, want ID 42 renamed", req)
	}
}

func TestRunGraphDeleteNodesSendsBatchRequest(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", struct{}{})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"graph", "delete-nodes", "--ids", "11,12"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceEngine || ft.writes[0].Method != string(api.MethodEngineRemoveNodes) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	req, err := api.DecodePayload[api.IDsRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Workspace != "main" || len(req.IDs) != 2 || req.IDs[0] != 11 || req.IDs[1] != 12 {
		t.Fatalf("payload = %#v, want batch node IDs", req)
	}
}

func TestRunReturnsAPIError(t *testing.T) {
	resp, err := api.NewErrorResponse("1", api.ErrorNotFound, "workspace was not found", nil)
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransport{reads: []api.Message{resp}}
	var stdout, stderr bytes.Buffer

	err = run(context.Background(), []string{"engine", "workspace-snapshot", "--workspace", "missing"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err == nil || err.Error() != "not_found: workspace was not found" {
		t.Fatalf("err = %v, want API not_found", err)
	}
}

func TestEngineWatchPrintsEventsQueuedDuringRequests(t *testing.T) {
	eventBeforeSubscribeResponse := mustEvent(t, api.SurfaceEngine, string(api.EventEngineNotification), map[string]any{
		"subscription_id": 77,
		"notification":    map[string]any{"kind": "workspace"},
	})
	eventBeforeFullSnapshotResponse := mustEvent(t, api.SurfaceEngine, string(api.EventEngineNotification), map[string]any{
		"subscription_id": 77,
		"notification":    map[string]any{"kind": "workspace", "workspace_id": "main"},
	})
	ft := &fakeTransport{reads: []api.Message{
		eventBeforeSubscribeResponse,
		mustResponse(t, "1", api.SubscribeNotificationsResponse{SubscriptionID: 77}),
		eventBeforeFullSnapshotResponse,
		mustResponse(t, "2", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"engine", "watch", "--full-snapshot"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("run error = %v, want EOF after queued events are drained", err)
	}
	if len(ft.writes) != 2 {
		t.Fatalf("writes = %d, want subscribe and full snapshot requests", len(ft.writes))
	}
	if ft.writes[0].Method != string(api.MethodEngineSubscribeNotifications) || ft.writes[1].Method != string(api.MethodEngineRequestFullSnapshot) {
		t.Fatalf("methods = %q, %q", ft.writes[0].Method, ft.writes[1].Method)
	}
	out := stdout.String()
	if strings.Count(strings.TrimSpace(out), "\n")+1 != 2 || !strings.Contains(out, `"workspace_id":"main"`) {
		t.Fatalf("stdout = %q, want both queued engine events", out)
	}
}

func TestLogWatchPrintsEventQueuedDuringSubscribe(t *testing.T) {
	entry := api.LogEntry{At: "2026-06-25T00:00:00Z", Level: "info", Source: "test", Message: "queued"}
	ft := &fakeTransport{reads: []api.Message{
		mustEvent(t, api.SurfaceLog, string(api.EventLogEntry), entry),
		mustResponse(t, "1", api.SubscribeLogsResponse{SubscriptionID: 9}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"log", "watch"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("run error = %v, want EOF after queued event is drained", err)
	}
	if len(ft.writes) != 1 || ft.writes[0].Method != string(api.MethodLogSubscribe) {
		t.Fatalf("writes = %#v, want one log subscribe request", ft.writes)
	}
	if !strings.Contains(stdout.String(), `"message":"queued"`) {
		t.Fatalf("stdout = %q, want queued log event", stdout.String())
	}
}

func TestDNSWatchKeysPrintsInitialResponseAndQueuedEvents(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustEvent(t, api.SurfaceLog, string(api.EventLogEntry), api.LogEntry{Message: "ignore"}),
		mustEvent(t, api.SurfaceDNS, string(api.EventDNSKeysUpdate), api.DNSKeysEvent{
			SubscriptionID: 7,
			Cache:          "manual",
			Change:         api.DNSKeyChange{Key: "example.test.|1|1", Entry: &api.DNSKeyInfo{Key: "example.test.|1|1", Size: 12}},
		}),
		mustResponse(t, "1", api.DNSSubscribeKeysResponse{
			SubscriptionID: 7,
			Snapshot: api.DNSKeysSnapshot{
				Exists: true,
				Keys:   []api.DNSKeyInfo{{Key: "example.test.|1|1", Size: 12}},
			},
		}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"dns", "watch-keys", "--cache", "manual"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("run error = %v, want EOF after queued event is drained", err)
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want subscribe request", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceDNS || ft.writes[0].Method != string(api.MethodDNSSubscribeKeys) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	req, err := api.DecodePayload[api.DNSKeysRequest](ft.writes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Cache != "manual" {
		t.Fatalf("payload = %#v, want manual cache", req)
	}
	out := stdout.String()
	if !strings.Contains(out, `"subscription_id": 7`) {
		t.Fatalf("stdout = %q, want pretty subscription response", out)
	}
	if !strings.Contains(out, `"method":"keys_update"`) || strings.Contains(out, "ignore") {
		t.Fatalf("stdout = %q, want DNS key event only", out)
	}
}

func TestSecretsWatchPrintsInitialResponseAndQueuedEvents(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustEvent(t, api.SurfaceLog, string(api.EventLogEntry), api.LogEntry{Message: "ignore"}),
		mustEvent(t, api.SurfaceSecrets, string(api.EventSecretsUpdate), api.SecretsEvent{
			SubscriptionID: 4,
			Change: api.SecretChange{
				Key:   "token",
				Entry: &api.SecretEntry{Key: "token", Value: "new", Source: api.SecretSourceMutable},
			},
		}),
		mustResponse(t, "1", api.SecretsSubscribeResponse{
			SubscriptionID: 4,
			Snapshot: api.SecretsSnapshot{
				Secrets: []api.SecretEntry{{Key: "token", Value: "old", Source: api.SecretSourceMutable}},
			},
		}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"secrets", "watch"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("run error = %v, want EOF after queued event is drained", err)
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want subscribe request", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceSecrets || ft.writes[0].Method != string(api.MethodSecretsSubscribe) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	out := stdout.String()
	if !strings.Contains(out, `"subscription_id": 4`) {
		t.Fatalf("stdout = %q, want pretty subscription response", out)
	}
	if !strings.Contains(out, `"method":"update"`) || strings.Contains(out, "ignore") {
		t.Fatalf("stdout = %q, want secrets event only", out)
	}
}

func TestTLSWatchPrintsInitialResponseAndQueuedEvents(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{
		mustEvent(t, api.SurfaceLog, string(api.EventLogEntry), api.LogEntry{Message: "ignore"}),
		mustEvent(t, api.SurfaceTLS, string(api.EventTLSUpdate), api.TLSEvent{
			SubscriptionID: 8,
			Change: api.TLSChange{
				Kind: api.TLSChangeCert,
				Name: "manual-ca",
				Cert: &api.TLSCertEntry{Name: "manual-ca", Source: api.TLSSourceMutable},
			},
		}),
		mustResponse(t, "1", api.TLSSubscribeResponse{
			SubscriptionID: 8,
			Snapshot: api.TLSSnapshot{
				Certs: []api.TLSCertEntry{{Name: "manual-ca", Source: api.TLSSourceMutable}},
			},
		}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"tls", "watch"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("run error = %v, want EOF after queued event is drained", err)
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want subscribe request", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceTLS || ft.writes[0].Method != string(api.MethodTLSSubscribe) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	out := stdout.String()
	if !strings.Contains(out, `"subscription_id": 8`) {
		t.Fatalf("stdout = %q, want pretty subscription response", out)
	}
	if !strings.Contains(out, `"method":"update"`) || strings.Contains(out, "ignore") {
		t.Fatalf("stdout = %q, want TLS event only", out)
	}
}

func TestHelpDoesNotDial(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--help"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		t.Fatal("dial should not be called for help")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q, want help", stdout.String())
	}
}

func TestCommandDispatchCoversRequestCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"admin hello", []string{"admin", "hello", "--client-name", "test", "--surfaces", "engine, kv"}},
		{"admin colormap", []string{"admin", "colormap"}},
		{"admin acquire", []string{"admin", "acquire-mutation", "--holder", "test", "--ttl-ms", "50"}},
		{"admin renew", []string{"admin", "renew-mutation", "--lease-id", "lease", "--ttl-ms", "50"}},
		{"admin release", []string{"admin", "release-mutation", "--lease-id", "lease"}},
		{"engine subscribe", []string{"engine", "subscribe-notifications", "--workspace", "test"}},
		{"engine unsubscribe", []string{"engine", "unsubscribe-notifications", "--subscription-id", "2"}},
		{"engine full snapshot", []string{"engine", "request-full-snapshot", "--subscription-id", "2"}},
		{"engine subscribe menu", []string{"engine", "subscribe-node-menu", "--id", "3", "--subscription-id", "2"}},
		{"engine unsubscribe menu", []string{"engine", "unsubscribe-node-menu", "--node", "3", "--subscription-id", "2"}},
		{"engine workspace", []string{"engine", "workspace-snapshot", "--workspace", "test"}},
		{"engine workers", []string{"engine", "workers-snapshot"}},
		{"engine class", []string{"engine", "node-class-snapshot", "--class", "test/Node"}},
		{"engine class description", []string{"engine", "node-class-long-description", "--class", "test/Node"}},
		{"engine remove class", []string{"engine", "remove-nodes-by-class", "--class", "test/Node"}},
		{"engine node", []string{"engine", "node-snapshot", "--id", "3"}},
		{"engine replace node", []string{"engine", "replace-node-with-placeholder", "--id", "3"}},
		{"engine remove node", []string{"engine", "remove-node", "--id", "3"}},
		{"engine port", []string{"engine", "port-snapshot", "--id", "4"}},
		{"engine link", []string{"engine", "link-snapshot", "--id", "5"}},
		{"engine remove link", []string{"engine", "remove-link", "--id", "5"}},
		{"engine add node", []string{"engine", "add-node", "--class", "test/Node", "--name", "node"}},
		{"engine add link", []string{"engine", "add-link", "--port-a", "4", "--port-b", "5"}},
		{"engine popup", []string{"engine", "remove-node-popup", "--id", "3", "--popup", "9"}},
		{"engine popups", []string{"engine", "remove-node-popups", "--id", "3"}},
		{"engine popup type", []string{"engine", "remove-node-popups-by-type", "--id", "3", "--type", "notice"}},
		{"engine copy", []string{"engine", "copy", "--ids", "3, 4"}},
		{"engine paste", []string{"engine", "paste", "--data", "copy-data"}},
		{"engine inbox", []string{"engine", "send-inbox", "--id", "3", "--payload", `{"ok":true}`}},
		{"engine formular", []string{"engine", "send-node-formular-msg", "--id", "3", "--message", `{"type":"noop"}`}},
		{"engine undo", []string{"engine", "undo"}},
		{"engine redo", []string{"engine", "redo"}},
		{"log subscribe", []string{"log", "subscribe"}},
		{"log unsubscribe", []string{"log", "unsubscribe", "--subscription-id", "2"}},
		{"kv stores", []string{"kv", "stores"}},
		{"kv entries", []string{"kv", "entries", "--plugin", "plugin", "--path", "/a/b/"}},
		{"kv get", []string{"kv", "get", "--key", "key"}},
		{"kv ensure", []string{"kv", "ensure", "--path", "a"}},
		{"kv delete", []string{"kv", "delete", "--key", "key"}},
		{"kv drop", []string{"kv", "drop", "--path", "a"}},
		{"kv unsubscribe", []string{"kv", "unsubscribe", "--subscription-id", "2"}},
		{"graph delete link", []string{"graph", "delete-link", "--id", "5"}},
		{"graph add link", []string{"graph", "add-link", "--port-a", "4", "--port-b", "5"}},
		{"graph class description", []string{"graph", "class-description", "--class", "test/Node"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", map[string]any{"ok": true})}}
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), tt.args, strings.NewReader(""), &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
				return ft, nil
			})
			if err != nil {
				t.Fatalf("run(%q) error = %v; stderr = %s", tt.args, err, stderr.String())
			}
			if len(ft.writes) != 1 {
				t.Fatalf("run(%q) writes = %d, want 1", tt.args, len(ft.writes))
			}
		})
	}
}

func TestSurfaceHelpAndUnknownCommands(t *testing.T) {
	for _, surface := range []string{"admin", "config", "dns", "engine", "graph", "kv", "log", "secrets", "snapshots", "tls", "workers"} {
		t.Run(surface, func(t *testing.T) {
			ft := &fakeTransport{}
			var stdout bytes.Buffer
			err := run(context.Background(), []string{surface, "--help"}, nil, &stdout, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
				return ft, nil
			})
			if err != nil || stdout.Len() == 0 || !ft.closed {
				t.Fatalf("help result: error=%v output=%q closed=%t", err, stdout.String(), ft.closed)
			}
		})
	}

	var dialed bool
	err := run(context.Background(), []string{"unknown"}, nil, io.Discard, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
		dialed = true
		return &fakeTransport{}, nil
	})
	if err == nil || !dialed {
		t.Fatalf("unknown command error = %v, dialed = %t", err, dialed)
	}
}

func TestCommandParsingValidation(t *testing.T) {
	var stderr bytes.Buffer
	if workspace, class, err := parseClass("class", []string{"--workspace", "w", "--class", "c"}, &stderr); err != nil || workspace != "w" || class != "c" {
		t.Fatalf("parseClass() = %q, %q, %v", workspace, class, err)
	}
	if workspace, id, err := parseID("id", []string{"--workspace", "w", "--id", "9"}, &stderr); err != nil || workspace != "w" || id != 9 {
		t.Fatalf("parseID() = %q, %d, %v", workspace, id, err)
	}
	if got := splitCSV(" a, ,b "); len(got) != 2 || got[0] != "a" || got[1] != "b" || splitCSV(" ") != nil {
		t.Fatalf("splitCSV() = %#v", got)
	}
	if _, err := parseKVPath("a//b"); err == nil {
		t.Fatal("parseKVPath accepted an empty segment")
	}
	if got, err := parseKVPath("///"); err != nil || got != nil {
		t.Fatalf("parseKVPath root = %#v, %v", got, err)
	}
	if err := parseNoArgs("test", []string{"extra"}, &stderr); err == nil {
		t.Fatal("parseNoArgs accepted a positional argument")
	}
}

func TestCommandValidationErrors(t *testing.T) {
	tests := [][]string{
		{"admin", "renew-mutation"}, {"admin", "release-mutation"}, {"admin", "unknown"},
		{"engine", "unsubscribe-notifications"}, {"engine", "request-full-snapshot"},
		{"engine", "subscribe-node-menu", "--id", "1"}, {"engine", "node-class-snapshot"},
		{"engine", "node-snapshot"}, {"engine", "port-snapshot"}, {"engine", "add-node"},
		{"engine", "add-link", "--port-a", "1"}, {"engine", "set-node-position", "--id", "1"},
		{"engine", "set-node-name", "--name", "old"}, {"engine", "remove-node-popup"},
		{"engine", "remove-nodes"}, {"engine", "copy"}, {"engine", "send-inbox", "--id", "1", "--payload", "{"},
		{"engine", "send-node-formular-msg", "--id", "1", "--message", "{"}, {"engine", "unknown"},
		{"graph", "node"}, {"graph", "delete-node"}, {"graph", "delete-nodes"},
		{"graph", "add-link", "--port-a", "1"}, {"graph", "delete-link"}, {"graph", "menu"},
		{"graph", "apply-menu", "--id", "1", "--json", "{"},
		{"graph", "click-menu-button", "--id", "1"}, {"graph", "class-description"}, {"graph", "unknown"},
		{"kv", "get"}, {"kv", "set"}, {"kv", "delete"}, {"kv", "unsubscribe"},
		{"kv", "entries", "--path", "a//b"}, {"kv", "unknown"},
		{"log", "unsubscribe"}, {"log", "unknown"},
		{"dns", "keys"}, {"dns", "get"}, {"dns", "set"}, {"dns", "delete"}, {"dns", "drop"}, {"dns", "unsubscribe"}, {"dns", "unknown"},
		{"secrets", "set"}, {"secrets", "rename"}, {"secrets", "delete"}, {"secrets", "unsubscribe"}, {"secrets", "unknown"},
		{"tls", "set-cert"}, {"tls", "rename-cert"}, {"tls", "delete-cert"}, {"tls", "set-policy"}, {"tls", "rename-policy"}, {"tls", "delete-policy"}, {"tls", "unsubscribe"}, {"tls", "unknown"},
		{"snapshots", "create", "extra"}, {"snapshots", "delete"}, {"snapshots", "rename"}, {"snapshots", "restore"}, {"snapshots", "unknown"},
		{"workers", "unknown"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			ft := &fakeTransport{}
			err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
				return ft, nil
			})
			if err == nil {
				t.Fatalf("run(%q) succeeded", args)
			}
		})
	}
}

func TestCommandFlagParseErrors(t *testing.T) {
	commands := [][]string{
		{"admin", "hello"}, {"admin", "ping"}, {"admin", "colormap"},
		{"admin", "acquire-mutation"}, {"admin", "renew-mutation"}, {"admin", "release-mutation"},
		{"engine", "watch"}, {"engine", "subscribe-notifications"},
		{"engine", "unsubscribe-notifications"}, {"engine", "request-full-snapshot"},
		{"engine", "subscribe-node-menu"}, {"engine", "unsubscribe-node-menu"},
		{"engine", "workspace-snapshot"}, {"engine", "workers-snapshot"},
		{"engine", "node-class-snapshot"}, {"engine", "node-class-long-description"},
		{"engine", "remove-nodes-by-class"}, {"engine", "node-snapshot"},
		{"engine", "replace-node-with-placeholder"}, {"engine", "remove-node"},
		{"engine", "port-snapshot"}, {"engine", "link-snapshot"}, {"engine", "remove-link"},
		{"engine", "remove-nodes"}, {"engine", "add-node"}, {"engine", "add-link"},
		{"engine", "set-node-position"}, {"engine", "set-node-name"},
		{"engine", "remove-node-popup"}, {"engine", "remove-node-popups"},
		{"engine", "remove-node-popups-by-type"}, {"engine", "copy"}, {"engine", "paste"},
		{"engine", "send-inbox"}, {"engine", "send-node-formular-msg"},
		{"engine", "undo"}, {"engine", "redo"},
		{"log", "subscribe"}, {"log", "unsubscribe"},
		{"raw"},
		{"config", "get"}, {"config", "snapshot"}, {"config", "apply"}, {"config", "update"},
		{"dns", "caches"}, {"dns", "watch-caches"}, {"dns", "keys"}, {"dns", "watch-keys"},
		{"dns", "get"}, {"dns", "watch-value"}, {"dns", "set"}, {"dns", "delete"},
		{"dns", "drop"}, {"dns", "unsubscribe"},
		{"kv", "stores"}, {"kv", "entries"}, {"kv", "get"}, {"kv", "set"},
		{"kv", "ensure"}, {"kv", "delete"}, {"kv", "drop"}, {"kv", "subscribe"},
		{"kv", "watch"}, {"kv", "unsubscribe"},
		{"secrets", "list"}, {"secrets", "watch"}, {"secrets", "set"},
		{"secrets", "set-nodes"}, {"secrets", "rename"}, {"secrets", "delete"},
		{"secrets", "unsubscribe"},
		{"snapshots", "create"}, {"snapshots", "list"}, {"snapshots", "export"},
		{"snapshots", "import"}, {"snapshots", "rename"}, {"snapshots", "restore"},
		{"snapshots", "delete"},
		{"tls", "certs"}, {"tls", "policies"}, {"tls", "snapshot"}, {"tls", "watch"},
		{"tls", "set-cert"}, {"tls", "rename-cert"}, {"tls", "delete-cert"},
		{"tls", "set-policy"}, {"tls", "rename-policy"}, {"tls", "delete-policy"},
		{"tls", "builtin-ca"}, {"tls", "reload-os"}, {"tls", "unsubscribe"},
		{"workers", "tree"},
	}
	for _, command := range commands {
		args := append(append([]string(nil), command...), "--definitely-unknown")
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
				return &fakeTransport{}, nil
			})
			if err == nil {
				t.Fatalf("run(%q) accepted an unknown flag", args)
			}
		})
	}
}

func TestRunSetupErrors(t *testing.T) {
	var dialed bool
	if err := run(context.Background(), []string{"--url", "://bad", "admin", "ping"}, nil, io.Discard, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
		dialed = true
		return nil, nil
	}); err == nil || dialed {
		t.Fatalf("invalid endpoint error = %v, dialed = %t", err, dialed)
	}
	want := errors.New("dial failed")
	if err := run(context.Background(), []string{"admin", "ping"}, nil, io.Discard, io.Discard, func(context.Context, endpoint) (apiTransport, error) {
		return nil, want
	}); !errors.Is(err, want) {
		t.Fatalf("dial error = %v, want %v", err, want)
	}
	if _, _, err := parseGlobal([]string{"--unknown"}, io.Discard); err == nil {
		t.Fatal("parseGlobal accepted an unknown flag")
	}
}

func mustEvent(t *testing.T, surface api.Surface, method string, payload any) api.Message {
	t.Helper()
	msg, err := api.NewEvent(surface, method, payload)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
