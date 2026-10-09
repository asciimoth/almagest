//go:build linux

package network

import (
	"context"
	"errors"
	"net"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tun"
	"golang.org/x/sys/unix"
)

type linuxIdentity struct {
	device tun.Tun
	index  int
}

// CaptureNativeIdentity pins the backend-owned TUN file and its interface
// index. TUNGETIFF must still resolve that file on every use; a new interface
// with the old name is insufficient. The caller retains the file exclusively.
func CaptureNativeIdentity(ctx context.Context, device tun.Tun) (NativeIdentity, error) {
	id := &linuxIdentity{device: device}
	index, err := id.current(ctx)
	if err != nil {
		return nil, err
	}
	id.index = index
	return id, nil
}

func (id *linuxIdentity) current(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	file := id.device.File()
	if file == nil {
		return 0, protocol.ErrCapabilityUnsupported
	}
	ifr, err := unix.NewIfreq("")
	if err != nil {
		return 0, err
	}
	if err := unix.IoctlIfreq(int(file.Fd()), unix.TUNGETIFF, ifr); err != nil {
		return 0, err
	}
	iface, err := net.InterfaceByName(ifr.Name())
	if err != nil {
		return 0, err
	}
	return iface.Index, nil
}

func (id *linuxIdentity) Verify(ctx context.Context) error {
	index, err := id.current(ctx)
	if err != nil || index != id.index {
		return errors.Join(protocol.ErrRecoveryRequired, err)
	}
	return nil
}

// InterfaceState avoids the tuntap name cache after sysnet renames the device.
// The descriptor identity check still precedes the index-based readback.
func (id *linuxIdentity) InterfaceState(ctx context.Context) (string, int, error) {
	if err := id.Verify(ctx); err != nil {
		return "", 0, err
	}
	iface, err := net.InterfaceByIndex(id.index)
	if err != nil {
		return "", 0, err
	}
	return iface.Name, iface.MTU, nil
}
