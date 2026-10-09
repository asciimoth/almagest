//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

func localLinuxPeer() LinuxPeer {
	return LinuxPeer{PID: int32(os.Getpid()), UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
}

func linuxLanes(t *testing.T) (Lanes, Lanes) {
	t.Helper()
	var left, right [3]Stream
	for i := range left {
		a, b, err := SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		left[i], err = BindSocket(a, localLinuxPeer())
		if err != nil {
			t.Fatal(err)
		}
		right[i], err = BindSocket(b, localLinuxPeer())
		if err != nil {
			t.Fatal(err)
		}
	}
	l, r := Lanes{left[0], left[1], left[2]}, Lanes{right[0], right[1], right[2]}
	t.Cleanup(l.Close)
	t.Cleanup(r.Close)
	return l, r
}

func TestLinuxPrivateControl(t *testing.T) {
	l, r := linuxLanes(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan *Peer, 1)
	fail := make(chan error, 1)
	go func() {
		p, err := New(ctx, r, testSession(), func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
			return protocol.StatusResult{LiveResources: 1}, nil
		})
		if err != nil {
			fail <- err
			return
		}
		ready <- p
	}()
	a, err := New(ctx, l, testSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	select {
	case b := <-ready:
		defer func() { _ = b.Close() }()
	case err := <-fail:
		t.Fatal(err)
	}
	var out protocol.StatusResult
	if err := a.Call(ctx, protocol.OperationStatus, readRequest(), &out); err != nil || out.LiveResources != 1 {
		t.Fatal(err)
	}
}

func TestLinuxRejectsRightsAndCredentials(t *testing.T) {
	for _, rights := range []bool{false, true} {
		a, b, err := SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		peer := localLinuxPeer()
		if !rights {
			peer.UID++
		}
		s, err := BindSocket(a, peer)
		if err != nil {
			t.Fatal(err)
		}
		var oob []byte
		if rights {
			oob = unix.UnixRights(int(b.Fd()))
		}
		if _, err := unix.SendmsgN(int(b.Fd()), []byte{1}, oob, nil, 0); err != nil {
			t.Fatal(err)
		}
		_ = s.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := s.Read(make([]byte, 1)); err == nil {
			t.Fatal("unexpected ancillary accepted")
		}
		_ = s.Close()
		_ = b.Close()
	}
}

func TestLinuxAncillaryClosesDescriptors(t *testing.T) {
	a, b, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	fd, err := unix.Dup(int(a.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAncillary(unix.UnixRights(fd), unix.MSG_CTRUNC, localLinuxPeer()); err == nil {
		t.Fatal("rights accepted")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Fatalf("descriptor leaked: %v", err)
	}
}

func TestLinuxPairHasNoListener(t *testing.T) {
	a, b, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	addr, err := unix.Getsockname(int(a.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if name := addr.(*unix.SockaddrUnix).Name; name != "" && name != "@" {
		t.Fatalf("pair is named: %q", name)
	}
	accepting, err := unix.GetsockoptInt(int(a.Fd()), unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil || accepting != 0 {
		t.Fatalf("pair accepts connections: %v", err)
	}
	flags, err := unix.FcntlInt(a.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("endpoint is inheritable")
	}
}

func TestLinuxFinalChildCredentials(t *testing.T) {
	if os.Getenv("ALMAGEST_CONTROL_TEST_CHILD") == "1" {
		// This branch runs only in the disposable test child. It proves that
		// message credentials follow the final identity, not pair creation.
		if os.Getuid() == 0 {
			if err := unix.Setgroups(nil); err != nil {
				os.Exit(2)
			}
			if err := unix.Setresgid(65534, 65534, 65534); err != nil {
				os.Exit(2)
			}
			if err := unix.Setresuid(65534, 65534, 65534); err != nil {
				os.Exit(2)
			}
		}
		f := os.NewFile(3, "child-control")
		if _, err := f.Write([]byte{42}); err != nil {
			os.Exit(3)
		}
		_ = f.Close()
		os.Exit(0)
	}
	a, b, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxFinalChildCredentials$")
	cmd.Env = append(os.Environ(), "ALMAGEST_CONTROL_TEST_CHILD=1")
	cmd.ExtraFiles = []*os.File{b}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	peer := localLinuxPeer()
	peer.PID = int32(cmd.Process.Pid)
	if os.Getuid() == 0 {
		peer.UID, peer.GID = 65534, 65534
	}
	s, err := BindSocket(a, peer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = b.Close()
	_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got [1]byte
	if _, err := io.ReadFull(s, got[:]); err != nil || got[0] != 42 {
		t.Fatalf("final child identity: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxStreamFragmentation(t *testing.T) {
	l, r := linuxLanes(t)
	data := bytes.Repeat([]byte{42}, 65536)
	go func() {
		for _, b := range data {
			if _, err := l.Control.Write([]byte{b}); err != nil {
				return
			}
		}
	}()
	_ = r.Control.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(data))
	if _, err := io.ReadFull(r.Control, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("stream corrupted")
	}
}

func TestLinuxAncestorCredentials(t *testing.T) {
	if os.Getenv("ALMAGEST_CONTROL_NAMESPACE_CHILD") == "1" {
		if os.Getpid() != 1 {
			t.Fatal("child has no private PID namespace")
		}
		peer := LinuxPeer{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), OutsidePIDNamespace: true}
		s, err := BindSocket(os.NewFile(3, "namespace-control"), peer)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
		var got [1]byte
		if _, err := io.ReadFull(s, got[:]); err != nil || got[0] != 42 {
			t.Fatalf("ancestor credentials: %v", err)
		}
		return
	}
	a, b, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLinuxAncestorCredentials$")
	cmd.Env = append(os.Environ(), "ALMAGEST_CONTROL_NAMESPACE_CHILD=1")
	cmd.ExtraFiles = []*os.File{b}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("run in Docker with CAP_SYS_ADMIN and a namespace-capable seccomp profile")
		}
		t.Fatal(err)
	}
	_ = b.Close()
	if _, err := a.Write([]byte{42}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("namespace child: %v: %s", err, output.Bytes())
	}
}

func FuzzAncillary(f *testing.F) {
	f.Add([]byte{}, 0)
	f.Add(unix.UnixCredentials(&unix.Ucred{}), 0)
	f.Add(unix.UnixRights(123), unix.MSG_CTRUNC)
	f.Fuzz(func(t *testing.T, b []byte, flags int) {
		// Fuzz metadata, never close fuzz-selected native descriptors.
		if len(b) > 2048 {
			return
		}
		_ = validateAncillary(b, flags, localLinuxPeer(), func(int) {})
	})
}
