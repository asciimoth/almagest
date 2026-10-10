//go:build windows

package supervision

import (
	"context"
	"strconv"
	"strings"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/windows"
)

// NativeReconciler verifies that a recorded Wintun identity disappeared after
// N exited. A still-present adapter is left for operator inspection because
// this generic recovery layer does not own a driver-specific removal handle.
type NativeReconciler struct{}

func (NativeReconciler) Inspect(ctx context.Context, effect Effect) (IdentityState, error) {
	if err := ctx.Err(); err != nil {
		return IdentityUnknown, err
	}
	parts := strings.SplitN(effect.Identity, ":", 3)
	if len(parts) != 3 || parts[0] != "windows-adapter-v1" || parts[2] == "" {
		return IdentityUnknown, ErrInvalidJournal
	}
	luid, err := strconv.ParseUint(parts[1], 16, 64)
	if err != nil || luid == 0 {
		return IdentityUnknown, ErrInvalidJournal
	}
	size := uint32(16 * 1024)
	for range 3 {
		if size > 1024*1024 {
			return IdentityUnknown, protocol.ErrLimitExceeded
		}
		buffer := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err = windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_ALL_INTERFACES, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return IdentityUnknown, err
		}
		for adapter, count := first, 0; adapter != nil && count < protocol.MaxInterfaceSnapshotItems; adapter, count = adapter.Next, count+1 {
			if adapter.Luid == luid && windows.BytePtrToString(adapter.AdapterName) == parts[2] {
				return IdentityUnknown, nil
			}
		}
		return IdentityAbsent, nil
	}
	return IdentityUnknown, protocol.ErrLimitExceeded
}

func (NativeReconciler) Cleanup(context.Context, Effect) error { return ErrRecoveryUncertain }
