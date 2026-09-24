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

func TestWebsocketRejectsDisallowedOrigin(t *testing.T) {
	admin := &AdminAPI{}
	request := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	request.Header.Set("Origin", "https://example.invalid")
	recorder := httptest.NewRecorder()
	admin.serveWebsocket(context.Background(), recorder, request, nil, nil, nil)
	if recorder.Code == http.StatusSwitchingProtocols {
		t.Fatal("serveWebsocket accepted a disallowed origin")
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
