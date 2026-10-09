//go:build windows

package network

import (
	"context"
	"errors"
	"net"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tun"
	"golang.org/x/sys/windows"
)

type windowsIdentity struct {
	device tun.Tun
	luid   uint64
	guid   string
}

// CaptureNativeIdentity retains the Windows LUID and adapter GUID. The
// sysnet-windows backend also checks its retained native identity inside each
// configuration and cleanup operation. Runtime input cannot supply either ID.
func CaptureNativeIdentity(ctx context.Context, device tun.Tun) (NativeIdentity, error) {
	id := &windowsIdentity{device: device}
	luid, guid, err := id.current(ctx)
	if err != nil {
		return nil, err
	}
	id.luid, id.guid = luid, guid
	return id, nil
}

func (id *windowsIdentity) current(ctx context.Context) (uint64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	name, err := id.device.Name()
	if err != nil {
		return 0, "", err
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, "", err
	}
	// The native snapshot has a hard byte/work bound and finite retries.
	size := uint32(16 * 1024)
	for range 3 {
		if size > 1024*1024 {
			return 0, "", protocol.ErrLimitExceeded
		}
		buffer := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err = windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_ALL_INTERFACES, 0, first, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return 0, "", err
		}
		for adapter, count := first, 0; adapter != nil && count < protocol.MaxInterfaceSnapshotItems; adapter, count = adapter.Next, count+1 {
			if int(adapter.IfIndex) == iface.Index || int(adapter.Ipv6IfIndex) == iface.Index {
				return adapter.Luid, windows.BytePtrToString(adapter.AdapterName), nil
			}
		}
		return 0, "", protocol.ErrRecoveryRequired
	}
	return 0, "", protocol.ErrLimitExceeded
}

func (id *windowsIdentity) Verify(ctx context.Context) error {
	luid, guid, err := id.current(ctx)
	if err != nil || luid != id.luid || guid != id.guid {
		return errors.Join(protocol.ErrRecoveryRequired, err)
	}
	return nil
}
