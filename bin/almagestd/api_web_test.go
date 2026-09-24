package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/coder/websocket"
)

func TestWebsocketAdminPingAndInvalidMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admin := &AdminAPI{projectVersionStatus: api.ProjectVersionStatus{Version: "test"}}
	mux := http.NewServeMux()
	admin.registerAPIHandlers(ctx, mux, nil, nil, nil)
	server := httptest.NewServer(mux)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	event, err := api.UnmarshalMessage(data)
	if err != nil || event.Method != string(api.EventAdminProjectVersionStatus) {
		t.Fatalf("initial event = %#v, error = %v", event, err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ignored")); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	_, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := api.UnmarshalMessage(data)
	if err != nil || invalid.Error == nil || invalid.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("invalid response = %#v, error = %v", invalid, err)
	}

	request := mustRequest(t, "ping", api.SurfaceAdmin, string(api.MethodAdminPing), api.PingRequest{Nonce: "websocket"})
	data, err = api.MarshalMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
	_, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.UnmarshalMessage(data)
	if err != nil || response.Error != nil || response.ID != "ping" {
		t.Fatalf("ping response = %#v, error = %v", response, err)
	}
}

func TestWebsocketOriginPolicy(t *testing.T) {
	for _, test := range []struct {
		name             string
		allowCrossOrigin bool
		origin           func(string) string
		wantAccepted     bool
	}{
		{
			name:         "same origin",
			origin:       func(serverURL string) string { return serverURL },
			wantAccepted: true,
		},
		{
			name:   "different loopback port",
			origin: func(string) string { return "http://127.0.0.1:1" },
		},
		{
			name: "different scheme",
			origin: func(serverURL string) string {
				return "https" + strings.TrimPrefix(serverURL, "http")
			},
		},
		{
			name:             "cross origin enabled",
			allowCrossOrigin: true,
			origin:           func(string) string { return "https://example.invalid" },
			wantAccepted:     true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			admin := &AdminAPI{webAllowCrossOrigin: test.allowCrossOrigin}
			mux := http.NewServeMux()
			admin.registerAPIHandlers(t.Context(), mux, nil, nil, nil)
			server := httptest.NewServer(mux)
			defer server.Close()

			url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws"
			conn, response, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{
				HTTPHeader: http.Header{"Origin": []string{test.origin(server.URL)}},
			})
			if response != nil && response.Body != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if test.wantAccepted {
				if err != nil {
					t.Fatalf("Dial() error = %v, want accepted origin", err)
				}
				defer conn.CloseNow() //nolint:errcheck
				return
			}
			if err == nil {
				conn.CloseNow() //nolint:errcheck
				t.Fatal("Dial() accepted a cross-origin request")
			}
			if response == nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("Dial() response = %#v, want HTTP 403", response)
			}
		})
	}
}

func TestWriteWebsocketError(t *testing.T) {
	var got api.Message
	err := writeWebsocketError(api.Message{ID: "request"}, api.ErrorInternal, "failed", func(msg api.Message) error {
		got = msg
		return nil
	})
	if err != nil || got.ID != "request" || got.Error == nil || got.Error.Code != api.ErrorInternal {
		t.Fatalf("writeWebsocketError = %#v, %v", got, err)
	}
}
