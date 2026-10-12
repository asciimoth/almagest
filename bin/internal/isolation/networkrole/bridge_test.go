package networkrole

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestBridgeStreamsClearsBootstrapDeadlines(t *testing.T) {
	leftClient, leftBridge := net.Pipe()
	rightBridge, rightClient := net.Pipe()
	defer func() { _ = leftClient.Close() }()
	defer func() { _ = rightClient.Close() }()

	expired := time.Now().Add(-time.Second)
	for _, stream := range []net.Conn{leftBridge, rightBridge} {
		if err := stream.SetDeadline(expired); err != nil {
			t.Fatal(err)
		}
	}
	var group sync.WaitGroup
	bridgeStreams(&group, leftBridge, rightBridge)

	assertBridgeTransfer(t, leftClient, rightClient, "left to right")
	assertBridgeTransfer(t, rightClient, leftClient, "right to left")
	_ = leftClient.Close()
	_ = rightClient.Close()
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after both clients closed")
	}
}

func assertBridgeTransfer(t *testing.T, source, destination net.Conn, message string) {
	t.Helper()
	if err := destination.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(source, message)
		written <- err
	}()
	data := make([]byte, len(message))
	if _, err := io.ReadFull(destination, data); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if string(data) != message {
		t.Fatalf("bridge payload = %q, want %q", data, message)
	}
}
