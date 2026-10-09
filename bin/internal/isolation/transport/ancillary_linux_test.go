//go:build linux

package transport

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

func TestLinuxAncillaryValidation(t *testing.T) {
	peer := localLinuxPeer()
	credentials := unix.UnixCredentials(&unix.Ucred{Pid: peer.PID, Uid: peer.UID, Gid: peer.GID})
	for _, tc := range []struct {
		name   string
		oob    []byte
		flags  int
		valid  bool
		closed []int
	}{
		{"credentials", credentials, 0, true, nil},
		{"missing credentials", nil, 0, false, nil},
		{"duplicate credentials", append(append([]byte{}, credentials...), credentials...), 0, false, nil},
		{"wrong PID", unix.UnixCredentials(&unix.Ucred{Pid: peer.PID + 1, Uid: peer.UID, Gid: peer.GID}), 0, false, nil},
		{"wrong UID", unix.UnixCredentials(&unix.Ucred{Pid: peer.PID, Uid: peer.UID + 1, Gid: peer.GID}), 0, false, nil},
		{"wrong GID", unix.UnixCredentials(&unix.Ucred{Pid: peer.PID, Uid: peer.UID, Gid: peer.GID + 1}), 0, false, nil},
		{"truncated control", credentials, unix.MSG_CTRUNC, false, nil},
		{"truncated payload", credentials, unix.MSG_TRUNC, false, nil},
		{"rights", unix.UnixRights(101, 102), 0, false, []int{101, 102}},
		{"rights with credentials", append(append([]byte{}, credentials...), unix.UnixRights(101, 102)...), 0, false, []int{101, 102}},
		{"rights then broken header", append(unix.UnixRights(101, 102), 1), 0, false, []int{101, 102}},
		{"rights then rights", append(unix.UnixRights(101), unix.UnixRights(102)...), 0, false, []int{101, 102}},
		{"truncated rights", unix.UnixRights(101, 102), unix.MSG_CTRUNC, false, []int{101, 102}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var closed []int
			err := validateAncillary(tc.oob, tc.flags, peer, func(fd int) { closed = append(closed, fd) })
			if (err == nil) != tc.valid || (err != nil && !errors.Is(err, protocol.ErrInvalidContract)) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(closed, tc.closed) {
				t.Fatalf("closed %v, want %v", closed, tc.closed)
			}
		})
	}
}

func TestLinuxBindRejectsInvalidEndpoints(t *testing.T) {
	for _, peer := range []LinuxPeer{{PID: -1}, {PID: 0}, {PID: 1, OutsidePIDNamespace: true}} {
		a, b, err := SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		if s, err := BindSocket(a, peer); !errors.Is(err, protocol.ErrInvalidContract) || s != nil {
			t.Fatalf("invalid peer: %v", err)
		}
		if _, err := a.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("rejected endpoint was not consumed")
		}
	}
	a, b, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := BindSocket(a, localLinuxPeer()); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal("non-socket accepted")
	}
	if _, err := a.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("non-socket was not consumed")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[1]) }()
	f := os.NewFile(uintptr(fds[0]), "datagram-test")
	if _, err := BindSocket(f, localLinuxPeer()); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal("datagram accepted")
	}
	if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("datagram was not consumed")
	}
}
