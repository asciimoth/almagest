//go:build unix

package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

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
