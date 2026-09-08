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
	reads  []api.Message
	writes []api.Message
	closed bool
}

func (t *fakeTransport) Read(context.Context) (api.Message, error) {
	if len(t.reads) == 0 {
		return api.Message{}, io.EOF
	}
	msg := t.reads[0]
	t.reads = t.reads[1:]
	return msg, nil
}

func (t *fakeTransport) Write(_ context.Context, msg api.Message) error {
	t.writes = append(t.writes, msg)
	return nil
}

func (t *fakeTransport) Close() error {
	t.closed = true
	return nil
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

func TestRunSnapshotCommandsSendCreateListRenameRestoreAndDeleteRequests(t *testing.T) {
	info := api.SnapshotInfo{
		UUID:      "11111111-1111-4111-8111-111111111111",
		Timestamp: 1788955200000000000,
		Name:      "same name",
	}
	transport := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", info),
		mustResponse(t, "1", api.SnapshotsListResponse{Snapshots: []api.SnapshotInfo{info}}),
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
	if err := run(context.Background(), []string{"snapshots", "rename", "--name", "renamed", info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot rename failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run(context.Background(), []string{"snapshots", "restore", info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot restore failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run(context.Background(), []string{"snapshots", "delete", "--uuid", info.UUID}, nil, &stdout, &stderr, dial); err != nil {
		t.Fatalf("snapshot delete failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(transport.writes) != 5 {
		t.Fatalf("snapshot writes = %d, want 5", len(transport.writes))
	}
	wantMethods := []api.SnapshotsMethod{
		api.MethodSnapshotsCreate,
		api.MethodSnapshotsList,
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
	renameRequest, err := api.DecodePayload[api.SnapshotsRenameRequest](transport.writes[2].Payload)
	if err != nil || renameRequest.UUID != info.UUID || renameRequest.Name != "renamed" {
		t.Fatalf("rename request = %#v, err=%v", renameRequest, err)
	}
	restoreRequest, err := api.DecodePayload[api.SnapshotUUIDRequest](transport.writes[3].Payload)
	if err != nil || restoreRequest.UUID != info.UUID {
		t.Fatalf("restore request = %#v, err=%v", restoreRequest, err)
	}
	deleteRequest, err := api.DecodePayload[api.SnapshotUUIDRequest](transport.writes[4].Payload)
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
		})}),
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
		})}),
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

func mustEvent(t *testing.T, surface api.Surface, method string, payload any) api.Message {
	t.Helper()
	msg, err := api.NewEvent(surface, method, payload)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
