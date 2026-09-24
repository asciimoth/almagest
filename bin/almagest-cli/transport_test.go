package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/coder/websocket"
)

func TestStreamTransportReadHonorsContextDeadline(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close() //nolint:errcheck
	defer serverConn.Close() //nolint:errcheck

	transport := &streamTransport{
		conn:  clientConn,
		codec: api.NewReadWriteCodec(clientConn),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := transport.Read(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Read error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Read returned after %s, want prompt context cancellation", elapsed)
	}
}

func TestRealDialUnixRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close() //nolint:errcheck
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close() //nolint:errcheck
		codec := api.NewReadWriteCodec(conn)
		msg, readErr := codec.Read()
		if readErr != nil {
			serverDone <- readErr
			return
		}
		serverDone <- codec.Write(msg)
	}()

	transport, err := realDial(context.Background(), endpoint{kind: endpointUnix, path: path})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := api.NewEvent(api.SurfaceAdmin, "event", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Write(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got, err := transport.Read(context.Background())
	if err != nil || got.Method != "event" {
		t.Fatalf("Read = %#v, %v", got, err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRealDialWebsocketRoundTripSkipsBinary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageBinary, []byte("ignored"))
		_ = conn.Write(r.Context(), websocket.MessageText, data)
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()

	transport, err := realDial(context.Background(), endpoint{kind: endpointWeb, url: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := api.NewEvent(api.SurfaceAdmin, "event", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Write(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got, err := transport.Read(context.Background())
	if err != nil || got.Method != "event" {
		t.Fatalf("Read = %#v, %v", got, err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
}
