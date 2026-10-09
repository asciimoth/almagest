//go:build windows

package transport

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/windows"
)

func TestWindowsPrivateControl(t *testing.T) {
	var left, right [3]Stream
	for i := range left {
		a, b, err := PipePair()
		if err != nil {
			t.Fatal(err)
		}
		left[i], err = BindPipe(a)
		if err != nil {
			t.Fatal(err)
		}
		right[i], err = BindPipe(b)
		if err != nil {
			t.Fatal(err)
		}
	}
	l, r := Lanes{left[0], left[1], left[2]}, Lanes{right[0], right[1], right[2]}
	defer l.Close()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		p, err := New(ctx, r, testSession(), func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
			return protocol.StatusResult{}, nil
		})
		if err == nil {
			defer func() { _ = p.Close() }()
			<-ctx.Done()
		}
		ready <- err
	}()
	p, err := New(ctx, l, testSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err := p.Call(ctx, protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPipeDeadlineAndHandleAccess(t *testing.T) {
	a, b, err := PipePair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	if err := a.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(make([]byte, 1)); !os.IsTimeout(err) {
		t.Fatalf("deadline: %v", err)
	}
	if err := b.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(make([]byte, 1024*1024)); !os.IsTimeout(err) {
		t.Fatalf("blocked write deadline: %v", err)
	}
}

func TestWindowsPipeRejectsOtherProcess(t *testing.T) {
	if name := os.Getenv("ALMAGEST_CONTROL_TEST_PIPE"); name != "" {
		path, err := windows.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.CreateFile(path, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			_ = windows.CloseHandle(h)
			t.Fatal("unrelated process connected")
		}
		if err != windows.ERROR_PIPE_BUSY && err != windows.ERROR_ACCESS_DENIED {
			t.Fatalf("unexpected connection failure: %v", err)
		}
		return
	}
	a, b, err := PipePair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsPipeRejectsOtherProcess$")
	cmd.Env = append(os.Environ(), "ALMAGEST_CONTROL_TEST_PIPE="+a.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unrelated client: %v: %s", err, out)
	}
}
