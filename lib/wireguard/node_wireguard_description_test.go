package wireguardnodes

import "testing"

func TestWireGuardClassKeepsConciseBuiltInFallback(t *testing.T) {
	if got, want := (WireGuardClass{}).LongDescription(), "Owns a userspace WireGuard device and its tunnel data path."; got != want {
		t.Fatalf("LongDescription() = %q, want %q", got, want)
	}
}

func TestWGConfigClassKeepsConciseBuiltInFallback(t *testing.T) {
	if got, want := (WGConfigClass{}).LongDescription(), "Configures peers on an attached WireGuard device."; got != want {
		t.Fatalf("LongDescription() = %q, want %q", got, want)
	}
}
