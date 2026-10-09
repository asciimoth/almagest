//go:build linux

package admingate

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinuxUnixListenerPeerGateModeAndCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := Listen(EndpointConfig{ID: 1, Kind: EndpointUnix, Address: path, Filters: Filters{UIDs: []uint32{uint32(os.Getuid())}}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode = %v, %v", info.Mode().Perm(), err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("current UID was denied")
	}
	_ = client.Close()
	_ = server.Close()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remained after Close: %v", err)
	}
}

func TestLinuxUnixListenerRejectsUnsafePathAndPeer(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-socket")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(EndpointConfig{ID: 1, Kind: EndpointUnix, Address: file}); err == nil {
		t.Fatal("non-socket target was replaced")
	}
	if _, err := Listen(EndpointConfig{ID: 1, Kind: EndpointUnix, Address: "relative.sock"}); err == nil {
		t.Fatal("relative Unix socket path was accepted")
	}

	path := filepath.Join(root, "denied.sock")
	listener, err := Listen(EndpointConfig{ID: 1, Kind: EndpointUnix, Address: path, Filters: Filters{UIDs: []uint32{uint32(os.Getuid()) + 1}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err = client.Read(make([]byte, 1)); err == nil {
		t.Fatal("disallowed Unix peer remained connected")
	}
	_ = client.Close()
}
