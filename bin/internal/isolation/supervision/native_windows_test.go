//go:build windows

package supervision

import (
	"context"
	"errors"
	"testing"
)

func TestWindowsNativeReconcilerFailsClosed(t *testing.T) {
	reconciler := NativeReconciler{}
	invalid := testEffect("invalid")
	invalid.State, invalid.Identity = EffectOwned, "windows-adapter-v1:bad"
	if _, err := reconciler.Inspect(context.Background(), invalid); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("invalid identity error = %v", err)
	}
	absent := invalid
	absent.Identity = "windows-adapter-v1:ffffffffffffffff:identity-that-does-not-exist"
	state, err := reconciler.Inspect(context.Background(), absent)
	if err != nil || state != IdentityAbsent {
		t.Fatalf("absent identity = %v, %v", state, err)
	}
	if err := reconciler.Cleanup(context.Background(), absent); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("unsafe generic cleanup error = %v", err)
	}
}
