//go:build linux

package transport

import (
	"net"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

// SocketPair returns unnamed, close-on-exec stream endpoints. Bootstrap passes
// only the role's endpoint through its exact descriptor list and closes copies.
// PASSCRED is enabled before a child can write, not after a handshake starts.
func SocketPair() (*os.File, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	for _, fd := range fds {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
			_ = unix.Close(fds[0])
			_ = unix.Close(fds[1])
			return nil, nil, err
		}
	}
	return os.NewFile(uintptr(fds[0]), "isolation-private"), os.NewFile(uintptr(fds[1]), "isolation-private"), nil
}

// LinuxPeer is fixed by trusted launch state after credential changes. PID is
// expressed in the receiver's PID namespace (never the child's claimed PID).
// Bootstrap must separately verify sandbox restrictions and keep the child
// process identity alive. SO_PEERCRED at pair creation is not used as proof.
type LinuxPeer struct {
	PID      int32
	UID, GID uint32
	// A child in a nested PID namespace sees an ancestor broker as PID zero.
	// This flag must come from trusted namespace setup, never from the wire.
	// Exact inherited endpoint ownership remains the process identity proof.
	OutsidePIDNamespace bool
}

type unixStream struct {
	*net.UnixConn
	peer LinuxPeer
}

// BindSocket consumes f, duplicates it into Go's poller, and verifies the
// endpoint kind. Every Read checks kernel credentials and rejects all rights.
func BindSocket(f *os.File, peer LinuxPeer) (Stream, error) {
	defer func() { _ = f.Close() }()
	if peer.PID < 0 || (peer.PID == 0) != peer.OutsidePIDNamespace {
		return nil, protocol.ErrInvalidContract
	}
	typ, err := unix.GetsockoptInt(int(f.Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_STREAM {
		return nil, protocol.ErrInvalidContract
	}
	// Bootstrap already enables PASSCRED before launch. R cannot setsockopt;
	// do not repeat a mutation merely to bind an already configured endpoint.
	credentials, err := unix.GetsockoptInt(int(f.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED)
	if err != nil {
		return nil, err
	}
	if credentials == 0 {
		if err := unix.SetsockoptInt(int(f.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
			return nil, err
		}
	}
	c, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}
	u, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, protocol.ErrInvalidContract
	}
	return &unixStream{u, peer}, nil
}

func (s *unixStream) Read(b []byte) (int, error) {
	// Linux accepts at most 253 rights per sendmsg. Reserve enough room to
	// close every installed descriptor even when other metadata is invalid.
	oob := make([]byte, unix.CmsgSpace(253*4)+unix.CmsgSpace(unix.SizeofUcred))
	n, oobn, flags, _, err := s.ReadMsgUnix(b, oob)
	if err != nil {
		return n, err
	}
	if n == 0 && oobn == 0 {
		return 0, nil
	}
	if err := checkAncillary(oob[:oobn], flags, s.peer); err != nil {
		_ = s.Close()
		return 0, err
	}
	return n, nil
}

func checkAncillary(oob []byte, flags int, peer LinuxPeer) error {
	return validateAncillary(oob, flags, peer, func(fd int) { unix.CloseOnExec(fd); _ = unix.Close(fd) })
}

func validateAncillary(oob []byte, flags int, peer LinuxPeer, closeFD func(int)) error {
	valid := flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) == 0
	credentials := 0
	// Parse each header independently. A malformed later header must not leak
	// rights already installed by the kernel in an earlier valid header.
	for len(oob) > 0 {
		header, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			return protocol.ErrInvalidContract
		}
		oob = rest
		m := unix.SocketControlMessage{Header: header, Data: data}
		if header.Level != unix.SOL_SOCKET {
			valid = false
			continue
		}
		switch header.Type {
		case unix.SCM_RIGHTS:
			// Kernel records use whole int32 descriptors. Still bound malformed
			// metadata before calling x/sys, whose parser assumes alignment.
			if len(m.Data)%4 != 0 {
				m.Data = m.Data[:len(m.Data)/4*4]
			}
			fds, err := unix.ParseUnixRights(&m)
			if err == nil {
				for _, fd := range fds {
					closeFD(fd)
				}
			}
			valid = false
		case unix.SCM_CREDENTIALS:
			if len(m.Data) != unix.SizeofUcred {
				valid = false
				continue
			}
			cred, err := unix.ParseUnixCredentials(&m)
			credentials++
			if err != nil || cred.Pid != peer.PID || cred.Uid != peer.UID || cred.Gid != peer.GID {
				valid = false
			}
		default:
			valid = false
		}
	}
	if !valid || credentials != 1 {
		return protocol.ErrInvalidContract
	}
	return nil
}
