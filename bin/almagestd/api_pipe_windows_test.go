//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

func TestWindowsAdminPipeStreamCollisionAndShutdown(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\almagest-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := listenWindowsAdminPipe(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close() //nolint:errcheck
	if duplicate, err := listenWindowsAdminPipe(path); err == nil {
		_ = duplicate.Close()
		t.Fatal("second listener claimed an occupied pipe")
	}

	admin := &AdminAPI{pipeListener: listener}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	admin.Run(ctx, cancel, &wg, nil, nil, nil)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := (&namedpipe.DialConfig{ExpectedOwner: user.User.Sid}).DialContext(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	codec := api.NewReadWriteCodec(conn)
	if _, err := codec.Read(); err != nil {
		t.Fatal(err)
	}
	request, err := api.NewRequest("1", api.SurfaceAdmin, string(api.MethodAdminPing), api.PingRequest{Nonce: "pipe"})
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.Write(request); err != nil {
		t.Fatal(err)
	}
	response, err := codec.Read()
	if err != nil || response.ID != "1" || response.Error != nil {
		t.Fatalf("pipe ping response = %#v, %v", response, err)
	}

	cancel()
	wg.Wait()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLocalPipePathRejectsRemoteAndNestedNames(t *testing.T) {
	for _, path := range []string{
		`\\server\pipe\almagest-admin`,
		`\\.\pipe\parent\child`,
		`\\.\pipe\..`,
	} {
		if err := validateLocalPipePath(path); err == nil {
			t.Fatalf("validateLocalPipePath(%q) succeeded", path)
		}
	}
}
