package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
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
