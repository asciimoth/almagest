//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

func TestDialLocalUserPipeReconnectsAndVerifiesOwner(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("almagest-cli-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	path := `\\.\pipe\` + name
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"O:%sD:P(A;;GA;;;%s)", user.User.Sid.String(), user.User.Sid.String(),
	))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&namedpipe.ListenConfig{SecurityDescriptor: descriptor}).Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close() //nolint:errcheck

	for range 2 {
		type acceptResult struct {
			conn net.Conn
			err  error
		}
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, err := listener.Accept()
			accepted <- acceptResult{conn: conn, err: err}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := dialLocalPipe(ctx, endpoint{
			kind:      endpointPipe,
			pipeTrust: pipeTrustUser,
			pipeName:  name,
		})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var serverConn net.Conn
		select {
		case result := <-accepted:
			if result.err != nil {
				_ = conn.Close()
				t.Fatal(result.err)
			}
			serverConn = result.conn
		case <-time.After(5 * time.Second):
			_ = conn.Close()
			t.Fatal("pipe listener did not accept connection")
		}
		if err := serverConn.Close(); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDialLocalMachinePipeRejectsUserOwnedServer(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("current identity is Local System")
	}
	name := fmt.Sprintf("almagest-cli-untrusted-%d-%d", os.Getpid(), time.Now().UnixNano())
	path := `\\.\pipe\` + name
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"O:%sD:P(A;;GA;;;%s)", user.User.Sid.String(), user.User.Sid.String(),
	))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&namedpipe.ListenConfig{SecurityDescriptor: descriptor}).Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialLocalPipe(ctx, endpoint{
		kind:      endpointPipe,
		pipeTrust: pipeTrustMachine,
		pipeName:  name,
	})
	if err == nil {
		_ = conn.Close()
		t.Fatal("machine pipe dial accepted a user-owned server")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("machine pipe dial error = %v, want access denied", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case serverConn := <-accepted:
		if serverConn != nil {
			_ = serverConn.Close()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe listener did not stop")
	}
}
