//go:build windows

package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/sys/windows"
)

func TestWindowsNetworkTun(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires the privileged disposable Windows VM phase")
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(host, "ALM-WINVM") {
		t.Skip("native isolation tests run only in ALM-WINVM")
	}
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{AdapterNamePrefix: "alm-m15"})
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
	b.journal, err = supervision.OpenJournal(filepath.Join(t.TempDir(), "native-recovery.json"), 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) == 0 {
		t.Fatal("missing VM interfaces")
	}
	_, err = b.CreateTun(ctx, protocol.CreateTunRequest{Meta: meta(b, "collision"), Options: protocol.TunOptions{Name: interfaces[0].Name}})
	if !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("collision: %v", err)
	}
	req := protocol.CreateTunRequest{Meta: meta(b, "native"), Options: protocol.TunOptions{MTU: 1400, Addresses: []netip.Prefix{netip.MustParsePrefix("198.18.242.1/32")}}}
	regular, err := b.CreateTun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := b.UpdateTun(ctx, protocol.UpdateTunRequest{Meta: meta(b, "mtu"), Resource: regular.Resource, Kind: protocol.TunUpdateMTU, MTU: 1500})
	if err != nil {
		t.Fatal(err)
	}
	state, err := b.GetResourceState(ctx, protocol.ResourceRequest{Meta: meta(b, ""), Resource: updated.CurrentResource})
	if err != nil || state.MTU != 1500 {
		t.Fatalf("state %+v: %v", state, err)
	}
	_, err = b.CloseResource(ctx, protocol.ResourceRequest{Meta: meta(b, "close"), Resource: updated.CurrentResource})
	if err != nil {
		t.Fatal(err)
	}
	req.Meta = meta(b, "default")
	req.Default = true
	req.Options.Addresses = []netip.Prefix{netip.MustParsePrefix("198.18.241.1/32")}
	first, err := b.CreateTun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Meta = meta(b, "equivalent")
	same, err := b.CreateTun(ctx, req)
	if err != nil || same.Resource != first.Resource {
		t.Fatalf("equivalent %+v: %v", same, err)
	}
	req.Meta = meta(b, "replacement")
	req.Options.MTU = 1500
	second, err := b.CreateTun(ctx, req)
	if err != nil || second.Resource.ID != first.Resource.ID || second.Resource.Generation != first.Resource.Generation+1 {
		t.Fatalf("replacement %+v: %v", second, err)
	}
	endpoint, _ := registerDNS(t, b)
	attached, err := b.ConfigureDNS(ctx, protocol.ConfigureDNSRequest{Meta: meta(b, "dns-on"), Tun: second.Resource, Enabled: true, Endpoint: endpoint})
	if err != nil || attached.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("DNS attach %+v: %v", attached, err)
	}
	detached, err := b.ConfigureDNS(ctx, protocol.ConfigureDNSRequest{Meta: meta(b, "dns-off"), Tun: attached.CurrentResource})
	if err != nil || detached.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("DNS detach %+v: %v", detached, err)
	}
	if err := b.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if records := b.journal.Snapshot(); len(records) != 0 {
		t.Fatalf("broker cleanup retained recovery records: %#v", records)
	}
}

func TestWindowsTunRelay(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires privileged Windows VM")
	}
	host, err := os.Hostname()
	if err != nil || !strings.EqualFold(host, "ALM-WINVM") {
		t.Skip("requires ALM-WINVM")
	}
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{AdapterNamePrefix: "alm-m16"})
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
	b.journal, err = supervision.OpenJournal(filepath.Join(t.TempDir(), "relay-recovery.json"), 64)
	if err != nil {
		t.Fatal(err)
	}
	exerciseTunRelay(t, b)
	if records := b.journal.Snapshot(); len(records) != 0 {
		t.Fatalf("relay cleanup retained recovery records: %#v", records)
	}
}
