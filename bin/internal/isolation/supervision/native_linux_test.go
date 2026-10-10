//go:build linux

package supervision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeReconcilerFailsClosedForLinuxIdentity(t *testing.T) {
	var ns unix.Stat_t
	if err := unix.Stat("/proc/self/ns/net", &ns); err != nil {
		t.Fatal(err)
	}
	loopback, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	reconciler := NativeReconciler{}
	present := testEffect("present")
	present.State = EffectOwned
	present.Identity = fmt.Sprintf("linux-netns-v1:%x:%x:%d", uint64(ns.Dev), ns.Ino, loopback.Index)
	state, err := reconciler.Inspect(context.Background(), present)
	if err != nil || state != IdentityUnknown {
		t.Fatalf("present identity = %v, %v", state, err)
	}
	absent := present
	absent.Identity = fmt.Sprintf("linux-netns-v1:%x:%x:%d", uint64(ns.Dev), ns.Ino, int(^uint32(0)>>1))
	state, err = reconciler.Inspect(context.Background(), absent)
	if err != nil || state != IdentityAbsent {
		t.Fatalf("absent identity = %v, %v", state, err)
	}
	foreign := present
	foreign.Identity = fmt.Sprintf("linux-netns-v1:%x:%x:%d", uint64(ns.Dev), ns.Ino+1, loopback.Index)
	state, err = reconciler.Inspect(context.Background(), foreign)
	if err != nil || state != IdentityForeign {
		t.Fatalf("foreign identity = %v, %v", state, err)
	}
	invalid := present
	invalid.Identity = "linux-netns-v1:bad"
	if _, err := reconciler.Inspect(context.Background(), invalid); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("invalid identity error = %v", err)
	}
}

func FuzzNativeReconcilerIdentity(f *testing.F) {
	f.Add("")
	f.Add("linux-netns-v1:0:0:1")
	f.Add("windows-adapter-v1:0000000000000001:guid")
	f.Fuzz(func(t *testing.T, identity string) {
		effect := testEffect("fuzz")
		effect.State, effect.Identity = EffectOwned, identity
		state, err := (NativeReconciler{}).Inspect(context.Background(), effect)
		if err == nil && state != IdentityAbsent && state != IdentityUnknown && state != IdentityForeign {
			t.Fatalf("invalid successful state %v", state)
		}
	})
}
