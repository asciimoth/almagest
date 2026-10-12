//go:build linux

package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"github.com/vishvananda/netlink"
)

func TestLinuxNetworkTun(t *testing.T) {
	if os.Getenv("ALMAGEST_ISOLATION_NATIVE_TEST") != "1" {
		t.Skip("requires the disposable Docker network namespace")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("native test requires Docker")
	}
	system, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{Tun: true, DynTun: true, TunNames: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Error(err)
		}
	})
	backend, err := NewSystemBackend(system, CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := testBroker(t)
	b.backend = backend
	ctx := context.Background()
	foreign := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "alm-foreign"}}
	if err := netlink.LinkAdd(foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(foreign) })
	_, err = b.CreateTun(ctx, protocol.CreateTunRequest{Meta: meta(b, "collision"), Options: protocol.TunOptions{Name: "alm-foreign", MTU: 1400}})
	if !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("collision: %v", err)
	}
	if _, err := netlink.LinkByName("alm-foreign"); err != nil {
		t.Fatal("foreign interface removed", err)
	}
	created, err := b.CreateTun(ctx, protocol.CreateTunRequest{Meta: meta(b, "native"), Options: protocol.TunOptions{Name: "alm-owned", MTU: 1400, Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/24")}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.State.Name != "alm-owned" || created.State.MTU != 1400 || len(created.State.Addresses) == 0 {
		t.Fatal(created)
	}
	updated, err := b.UpdateTun(ctx, protocol.UpdateTunRequest{Meta: meta(b, "mtu"), Resource: created.Resource, Kind: protocol.TunUpdateMTU, MTU: 1500})
	if err != nil {
		t.Fatal(err)
	}
	state, err := b.GetResourceState(ctx, protocol.ResourceRequest{Meta: meta(b, ""), Resource: updated.CurrentResource})
	if err != nil || state.MTU != 1500 {
		t.Fatalf("state %+v: %v", state, err)
	}
	_, err = b.UpdateTun(ctx, protocol.UpdateTunRequest{Meta: meta(b, "rename_collision"), Resource: updated.CurrentResource, Kind: protocol.TunUpdateName, Name: "alm-foreign"})
	if !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal(err)
	}
	// An administrator's change must not be overwritten by broker cleanup.
	ownedLink, err := netlink.LinkByName("alm-owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMTU(ownedLink, 1300); err != nil {
		t.Fatal(err)
	}
	_, err = b.CloseResource(ctx, protocol.ResourceRequest{Meta: meta(b, "external_change"), Resource: updated.CurrentResource})
	if !errors.Is(err, protocol.ErrRecoveryRequired) {
		t.Fatalf("external change: %v", err)
	}
	changed, err := netlink.LinkByName("alm-owned")
	if err != nil || changed.Attrs().MTU != 1300 {
		t.Fatalf("external change overwritten: %v", err)
	}
	// The test administrator restores the expected state for explicit cleanup.
	if err := netlink.LinkSetMTU(ownedLink, 1500); err != nil {
		t.Fatal(err)
	}
	_, err = b.CloseResource(ctx, protocol.ResourceRequest{Meta: meta(b, "close_native"), Resource: updated.CurrentResource})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := netlink.LinkByName("alm-owned"); err == nil {
		t.Fatal("owned interface remained")
	}
	if _, err := netlink.LinkByName("alm-foreign"); err != nil {
		t.Fatal("foreign interface removed", err)
	}
}

func TestLinuxDefaultTunControl(t *testing.T) {
	if os.Getenv("ALMAGEST_ISOLATION_DEFAULT_TEST") != "1" {
		t.Skip("requires a disposable Docker namespace with native routing")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("native test requires Docker")
	}
	// Build a local underlay in the container's network namespace. There is
	// no dependency on Internet access or on the host's route table.
	underlay := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "alm-underlay"}}
	if err := netlink.LinkAdd(underlay); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(underlay) })
	addr, err := netlink.ParseAddr("198.18.240.1/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(underlay, addr); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: underlay.Attrs().Index, Gw: net.ParseIP("198.18.240.254")}); err != nil {
		t.Fatal(err)
	}
	system, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{Tun: true, DefaultTun: true, DynTun: true, DynDefaultTun: true, Routing: true, DNSControl: true}, DNS: linuxsysnet.DNSConfig{Mode: linuxsysnet.DNSModeDirect}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Error(err)
		}
	})
	backend, err := NewSystemBackend(system, CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := testBroker(t)
	b.backend = backend
	req := protocol.CreateTunRequest{Meta: meta(b, "default"), Default: true, Options: protocol.TunOptions{MTU: 1400, Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")}}}
	first, err := b.CreateTun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Meta = meta(b, "equivalent")
	same, err := b.CreateTun(context.Background(), req)
	if err != nil || same.Resource != first.Resource {
		t.Fatalf("reuse %+v: %v", same, err)
	}
	req.Meta = meta(b, "replacement")
	req.Options.MTU = 1500
	second, err := b.CreateTun(context.Background(), req)
	if err != nil || second.Resource.ID != first.Resource.ID || second.Resource.Generation != first.Resource.Generation+1 {
		t.Fatalf("replacement %+v: %v", second, err)
	}
	endpoint, _ := registerDNS(t, b)
	attached, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "dns-on"), Tun: second.Resource, Enabled: true, Endpoint: endpoint})
	if err != nil || attached.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("DNS attach %+v: %v", attached, err)
	}
	detached, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "dns-off"), Tun: attached.CurrentResource})
	if err != nil || detached.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("DNS detach %+v: %v", detached, err)
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
