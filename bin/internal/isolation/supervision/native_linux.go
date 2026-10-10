//go:build linux

package supervision

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// NativeReconciler inspects Linux TUN identities after N restarts. A TUN file
// normally closes with N and removes the interface. If the recorded interface
// index is still present, the reconciler deliberately reports unknown: an
// index can be reused and is not sufficient authority to delete the object.
type NativeReconciler struct{}

func (NativeReconciler) Inspect(ctx context.Context, effect Effect) (IdentityState, error) {
	if err := ctx.Err(); err != nil {
		return IdentityUnknown, err
	}
	parts := strings.Split(effect.Identity, ":")
	if len(parts) != 4 || parts[0] != "linux-netns-v1" {
		return IdentityUnknown, ErrInvalidJournal
	}
	device, err := strconv.ParseUint(parts[1], 16, 64)
	if err != nil {
		return IdentityUnknown, ErrInvalidJournal
	}
	inode, err := strconv.ParseUint(parts[2], 16, 64)
	if err != nil {
		return IdentityUnknown, ErrInvalidJournal
	}
	index, err := strconv.Atoi(parts[3])
	if err != nil || index <= 0 {
		return IdentityUnknown, ErrInvalidJournal
	}
	var current unix.Stat_t
	if err := unix.Stat("/proc/self/ns/net", &current); err != nil {
		return IdentityUnknown, err
	}
	if uint64(current.Dev) != device || current.Ino != inode {
		return IdentityForeign, nil
	}
	if _, err := net.InterfaceByIndex(index); err == nil {
		return IdentityUnknown, nil
	} else if _, ok := err.(*net.OpError); ok || strings.Contains(err.Error(), "no such network interface") {
		return IdentityAbsent, nil
	} else {
		return IdentityUnknown, fmt.Errorf("inspect interface index %d: %w", index, err)
	}
}

func (NativeReconciler) Cleanup(context.Context, Effect) error {
	return ErrRecoveryUncertain
}
