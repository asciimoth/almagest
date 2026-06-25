package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
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
