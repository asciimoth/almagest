//go:build unix

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

func TestUnixAdminStreamPing(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := listenUnixAdmin(socketPath, AdminPeerFiltersConfig{
		UIDs: []uint32{uint32(os.Getuid())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()                   //nolint
	defer cleanupUnixAdminSocket(socketPath) //nolint

	admin := &AdminAPI{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Go(func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		admin.serveAPIStream(ctx, conn, nil, nil, nil)
	})

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint

	codec := api.NewReadWriteCodec(conn)
	event, err := codec.Read()
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != api.MessageEvent ||
		event.Surface != api.SurfaceAdmin ||
		event.Method != string(api.EventAdminProjectVersionStatus) {
		t.Fatalf("initial message = %#v, want project_version_status event", event)
	}

	req, err := api.NewRequest("1", api.SurfaceAdmin, string(api.MethodAdminPing), api.PingRequest{
		Nonce: "unix",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.Write(req); err != nil {
		t.Fatal(err)
	}

	resp, err := codec.Read()
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "1" || resp.Error != nil {
		t.Fatalf("ping response = %#v, want successful id=1 response", resp)
	}
	pong, err := api.DecodePayload[api.PingResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if pong.Nonce != "unix" {
		t.Fatalf("PingResponse.Nonce = %q, want unix", pong.Nonce)
	}

	cancel()
	_ = conn.Close()
	wg.Wait()
}
