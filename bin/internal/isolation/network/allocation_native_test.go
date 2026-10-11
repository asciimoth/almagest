package network

import (
	"context"
	"net"
	"reflect"
	"testing"

	"github.com/asciimoth/gonnect/sysnet"
)

func nativeSystemCapabilityParity(t *testing.T, system sysnet.System) {
	t.Helper()
	b, _ := testBroker(t)
	b.backend = &SystemBackend{system: system}
	adapter := startClient(t, b).System()
	t.Cleanup(func() { _ = adapter.Close() })
	want := system.Capabilities()
	got := adapter.Capabilities()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities differ\ndirect: %#v\nremote: %#v", want, got)
	}
}

// Compare availability and allocation invariants against the same native
// allocator under the same credentials. Actual values can differ because the
// native pool selection is randomized; caller-supplied reservations must still
// exclude the exact same address or subnet in both directions.
func nativeAllocationParity(t *testing.T, system sysnet.System) {
	t.Helper()
	b, _ := testBroker(t)
	b.backend = &SystemBackend{system: system}
	c := startClient(t, b)
	remote := c.Allocator()
	nativeIP := system.AllocIP()
	nativeSubnet := system.AllocSubnet()
	if nativeIP == nil || nativeSubnet == nil {
		t.Fatal("native system did not expose its allocator interfaces")
	}
	for _, tc := range []struct {
		name   string
		direct func() (net.IP, *net.IPNet)
		remote func() (net.IP, *net.IPNet)
		ipv4   bool
	}{
		{"ipv4", nativeIP.AllocIP4, remote.AllocIP4, true},
		{"ipv6", nativeIP.AllocIP6, remote.AllocIP6, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directIP, directParent := tc.direct()
			if directIP != nil {
				nativeIP.FreeIP(directIP)
			}
			ip, parent := tc.remote()
			if (directIP == nil) != (ip == nil) {
				t.Fatalf("availability: direct=%v remote=%v error=%v", directIP, ip, remote.Err())
			}
			if ip == nil {
				return
			}
			if parent == nil || directParent == nil || !parent.Contains(ip) || (ip.To4() != nil) != tc.ipv4 {
				t.Fatalf("invalid native allocation: %v %v", ip, parent)
			}
			remote.ReserveIP(ip)
			remote.FreeIP(ip)
			// A direct allocation must not obtain R's still-reserved address.
			other, _ := tc.direct()
			if other != nil {
				defer nativeIP.FreeIP(other)
			}
			if other.Equal(ip) {
				t.Fatal("one Free lost both runtime reserve counts")
			}
			remote.FreeIP(ip)
		})
	}
	for _, tc := range []struct {
		name   string
		bits   int
		direct func(int) *net.IPNet
		remote func(int) *net.IPNet
	}{
		{"subnet4", 28, nativeSubnet.AllocSubnet4, remote.AllocSubnet4},
		{"subnet6", 120, nativeSubnet.AllocSubnet6, remote.AllocSubnet6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			direct := tc.direct(tc.bits)
			if direct != nil {
				nativeSubnet.FreeSubnet(direct)
			}
			n := tc.remote(tc.bits)
			if (direct == nil) != (n == nil) {
				t.Fatalf("subnet availability: direct=%v remote=%v error=%v", direct, n, remote.Err())
			}
			if n == nil {
				return
			}
			if bits, _ := n.Mask.Size(); bits != tc.bits {
				t.Fatalf("wrong prefix: %v", n)
			}
			remote.ReserveSubnet(n)
			remote.FreeSubnet(n)
			other := tc.direct(tc.bits)
			if other != nil {
				defer nativeSubnet.FreeSubnet(other)
			}
			if other != nil && (other.Contains(n.IP) || n.Contains(other.IP)) {
				t.Fatal("runtime reservation overlaps a native allocation")
			}
			remote.FreeSubnet(n)
		})
	}
	remote.FreeAllIP()
	remote.FreeAllSubnets()
	if err := remote.Err(); err != nil {
		t.Fatal(err)
	}
	status, err := c.Status(context.Background())
	if err != nil || status.LiveResources != 0 {
		t.Fatalf("reservation cleanup: %+v %v", status, err)
	}
}
