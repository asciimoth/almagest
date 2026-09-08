package tailscalenodes

import (
	"context"
	"crypto/tls"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
	wgotailscale "github.com/asciimoth/wgo-tailscale"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func TestReconcileUsesControlNetworkAndWireGuardDefaultUDPForDirectPeers(t *testing.T) {
	device := tailscaleTestDevice(t)
	controller := wgdevice.DetachDevice(device)
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	spawner := &tailscaleTestSpawner{}
	factory := &tailscaleTestClientFactory{snapshot: wgotailscale.Snapshot{State: wgotailscale.StateNeedsAuthentication}}
	node := &tailscaleNode{
		Base:      nodekit.Base{WorkerSpawner: spawner},
		cache:     &tailscaleCache{},
		newClient: factory.newClient,
	}
	node.generation.Store(1)
	ctx, cancel := context.WithCancel(context.Background())
	result, send := node.reconcile(tailscaleRuntimeRequest{
		generation: 1,
		config: tailscaleConfig{
			enabled:    true,
			controlURL: "https://control.example.test",
			hostname:   "alpha",
			listenPort: 41641,
			storageID:  tailscaleStorageID("alpha"),
		},
		controller: controller,
		network:    network,
		tlsConfig:  &tls.Config{ServerName: "control.example.test"},
		nodeName:   "alpha",
		ctx:        ctx,
	})
	if !send || result.err != nil || result.runtimeID == 0 {
		t.Fatalf("reconcile = send %t, result %#v", send, result)
	}
	call := factory.onlyCall(t)
	if wrapped, ok := call.network.(*tailscaleNetwork); !ok || wrapped.Network != network {
		t.Fatal("client did not receive the TailscaleCtrl Network input")
	}
	if call.controller != controller {
		t.Fatal("client did not receive the detached WireGuard API")
	}
	if !call.options.UseDefaultTransportForDirectPeers {
		t.Fatal("direct peers do not use the WireGuard default UDP transport")
	}
	if call.options.TransportID == wgdevice.DefaultTransportID || call.options.TransportID == "" {
		t.Fatalf("DERP transport ID = %q, want a non-default named transport", call.options.TransportID)
	}
	if call.options.ListenPort != 41641 || call.options.TLSConfig == nil || call.options.Cache.Load == nil || call.options.Cache.Store == nil {
		t.Fatalf("client options = %#v", call.options)
	}
	cancel()
	node.stopRuntime()
	if !factory.clients[0].isClosed() {
		t.Fatal("runtime client remained open after shutdown")
	}
}

func TestFirewallObjectConvertsTailscaleACLToIncomingPolicy(t *testing.T) {
	bits := 32
	view := wgotailscale.ACLView{Rules: []wgotailscale.ACLRule{{
		SourceIPs:   []string{"100.64.0.1"},
		SourceBits:  []int{32},
		IPProtocols: []int{6},
		Destinations: []wgotailscale.ACLDestination{{
			IP: "100.64.0.2", Bits: &bits,
			Ports: wgotailscale.PortRange{First: 443, Last: 443},
		}},
	}}}
	config, err := nodekit.FirewallConfigFromObject(firewallObject(view.FirewallConfig()))
	if err != nil {
		t.Fatalf("parse generated firewall object: %v", err)
	}
	if len(config.Exclude) != 0 || len(config.Include) != 1 {
		t.Fatalf("generated firewall config = %#v", config)
	}
	rule := config.Include[0]
	if rule.Network != "tcp" || len(rule.Hosts) != 1 || rule.Hosts[0] != "100.64.0.1/32" ||
		len(rule.LocalHosts) != 1 || rule.LocalHosts[0] != "100.64.0.2/32" ||
		len(rule.Ports) != 1 || rule.Ports[0] != 443 {
		t.Fatalf("generated firewall rule = %#v", rule)
	}
}

func TestFirewallObjectNormalizesMappedIPv4ACLSelectors(t *testing.T) {
	destinationBits := 120
	view := wgotailscale.ACLView{Rules: []wgotailscale.ACLRule{{
		SourceIPs:   []string{"::ffff:192.0.2.0/120"},
		SourceBits:  []int{120},
		IPProtocols: []int{6},
		Destinations: []wgotailscale.ACLDestination{{
			IP: "::ffff:198.51.100.0", Bits: &destinationBits,
			Ports: wgotailscale.PortRange{First: 443, Last: 443},
		}},
	}}}
	config, err := nodekit.FirewallConfigFromObject(firewallObject(view.FirewallConfig()))
	if err != nil {
		t.Fatalf("parse generated firewall object: %v", err)
	}
	if !config.AllowsIncomingIP(
		6,
		netip.MustParseAddrPort("192.0.2.20:12345"),
		netip.MustParseAddrPort("198.51.100.20:443"),
	) {
		t.Fatal("mapped IPv4 ACL selectors did not allow equivalent IPv4 traffic")
	}
}

func TestStaleRuntimeAndWireGuardResultsAreIgnored(t *testing.T) {
	node := &tailscaleNode{
		config:              defaultTailscaleConfig(),
		activeRuntimeID:     8,
		acceptRuntimeEvents: true,
		wgApplySequence:     12,
		firewall:            emptyFirewallObject(),
		dnsResolver:         &magicDNSResolver{},
	}
	node.handleRuntimeEvent(tailscaleRuntimeEvent{runtimeID: 7, snapshot: wgotailscale.Snapshot{State: wgotailscale.StateRunning}})
	if node.snapshot.State != "" {
		t.Fatalf("stale runtime event changed snapshot to %q", node.snapshot.State)
	}
	node.handleWGApplyResult(tailscaleWGApplyResult{runtimeID: 8, sequence: 11})
	if node.wgConfigured {
		t.Fatal("stale WireGuard apply marked the node configured")
	}
}

func TestTailscaleStateWaitsForCurrentWireGuardConfig(t *testing.T) {
	device := tailscaleTestDevice(t)
	controller := wgdevice.DetachDevice(device)
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(controller.Close)
	t.Cleanup(func() { _ = network.Close() })

	node := &tailscaleNode{
		config:          defaultTailscaleConfig(),
		ctrlLink:        1,
		netLink:         2,
		controller:      controller,
		network:         network,
		activeRuntimeID: 8,
		snapshot:        wgotailscale.Snapshot{State: wgotailscale.StateRunning},
		wgApplySequence: 12,
		wgApplyPending:  true,
	}
	if got := node.stateText(); got != "configuring WireGuard" {
		t.Fatalf("state before WireGuard apply = %q", got)
	}

	node.handleWGApplyResult(tailscaleWGApplyResult{runtimeID: 8, sequence: 12})
	if got := node.stateText(); got != "up" {
		t.Fatalf("state after WireGuard apply = %q", got)
	}

	node.wgApplyPending = true
	node.wgConfigured = false
	node.handleWGApplyResult(tailscaleWGApplyResult{
		runtimeID: 8,
		sequence:  12,
		err:       errors.New("test apply failure"),
	})
	if got := node.stateText(); got != "error" {
		t.Fatalf("state after WireGuard apply failure = %q", got)
	}
}

func TestTailscaleWGInterfaceConfigIncludesAppliedPeerRoutes(t *testing.T) {
	snapshot := wgotailscale.Snapshot{Network: wgotailscale.NetworkConfiguration{
		Addresses: []netip.Prefix{
			netip.MustParsePrefix("fd7a:115c:a1e0::84/128"),
			netip.MustParsePrefix("100.82.132.84/32"),
			netip.MustParsePrefix("100.82.132.84/32"),
			{},
		},
		Routes: []wgotailscale.Route{
			{Prefix: netip.MustParsePrefix("fd7a:115c:a1e0::/48")},
			{Prefix: netip.MustParsePrefix("100.97.143.122/32")},
			{Prefix: netip.MustParsePrefix("10.20.0.0/16")},
			{Prefix: netip.MustParsePrefix("100.97.143.122/32")},
			{},
		},
	}}
	want := wireguardnodes.WGCtrlInterfaceConfig{
		OwnedIPs: []string{"100.82.132.84/32", "fd7a:115c:a1e0::84/128"},
		Routes:   []string{"10.20.0.0/16", "100.97.143.122/32", "fd7a:115c:a1e0::/48"},
	}
	if got := tailscaleWGInterfaceConfig(snapshot); !sameWGConfig(got, want) {
		t.Fatalf("WireGuard interface config = %#v, want %#v", got, want)
	}

	changedRoutes := want.Clone()
	changedRoutes.Routes = append(changedRoutes.Routes, "192.0.2.0/24")
	if sameWGConfig(want, changedRoutes) {
		t.Fatal("configs with different route sets compare equal")
	}
}

func TestTailscaleRuntimeEventWatcherWaitsForActivation(t *testing.T) {
	t.Run("cancel before activation", func(t *testing.T) {
		client := newTailscaleTestClient(wgotailscale.Snapshot{State: wgotailscale.StateRunning})
		events := make(chan wgotailscale.Event, 1)
		events <- wgotailscale.Event{Kind: wgotailscale.EventState}
		activate := make(chan struct{})
		done := make(chan struct{})
		watcherDone := make(chan struct{})
		var sent atomic.Int64
		go func() {
			watchTailscaleRuntimeEvents(client, events, activate, done, func(wgotailscale.Snapshot) {
				sent.Add(1)
			})
			close(watcherDone)
		}()
		close(done)
		<-watcherDone
		if got := sent.Load(); got != 0 {
			t.Fatalf("snapshots sent before activation = %d", got)
		}
	})

	t.Run("deliver after activation", func(t *testing.T) {
		want := wgotailscale.Snapshot{State: wgotailscale.StateRunning, Revision: 7}
		client := newTailscaleTestClient(want)
		events := make(chan wgotailscale.Event, 1)
		activate := make(chan struct{})
		done := make(chan struct{})
		watcherDone := make(chan struct{})
		snapshots := make(chan wgotailscale.Snapshot, 1)
		go func() {
			watchTailscaleRuntimeEvents(client, events, activate, done, func(snapshot wgotailscale.Snapshot) {
				snapshots <- snapshot
			})
			close(watcherDone)
		}()
		close(activate)
		events <- wgotailscale.Event{Kind: wgotailscale.EventState}
		if got := <-snapshots; got.State != want.State || got.Revision != want.Revision {
			t.Fatalf("delivered snapshot = %#v, want %#v", got, want)
		}
		close(done)
		<-watcherDone
	})
}

type tailscaleTestClientFactory struct {
	mu       sync.Mutex
	snapshot wgotailscale.Snapshot
	calls    []tailscaleTestClientCall
	clients  []*tailscaleTestClient
}

type tailscaleTestClientCall struct {
	network    gonnect.Network
	controller wgdevice.DeviceAPI
	options    wgotailscale.Options
}

func (f *tailscaleTestClientFactory) newClient(network gonnect.Network, controller wgdevice.DeviceAPI, options wgotailscale.Options) (Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	client := newTailscaleTestClient(f.snapshot)
	f.calls = append(f.calls, tailscaleTestClientCall{network: network, controller: controller, options: options})
	f.clients = append(f.clients, client)
	return client, nil
}

func (f *tailscaleTestClientFactory) onlyCall(t *testing.T) tailscaleTestClientCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("client calls = %d, want 1", len(f.calls))
	}
	return f.calls[0]
}

func (f *tailscaleTestClientFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *tailscaleTestClientFactory) client(index int) *tailscaleTestClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.clients) {
		return nil
	}
	return f.clients[index]
}

type tailscaleTestClient struct {
	mu          sync.Mutex
	snapshot    wgotailscale.Snapshot
	events      chan wgotailscale.Event
	eventsOnce  sync.Once
	closed      atomic.Bool
	resumeCalls []uint64
}

func newTailscaleTestClient(snapshot wgotailscale.Snapshot) *tailscaleTestClient {
	return &tailscaleTestClient{snapshot: snapshot, events: make(chan wgotailscale.Event, 8)}
}

func (*tailscaleTestClient) Start(context.Context) error { return nil }
func (c *tailscaleTestClient) Close() error {
	c.closed.Store(true)
	c.closeEvents()
	return nil
}
func (c *tailscaleTestClient) Snapshot() wgotailscale.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}
func (c *tailscaleTestClient) Subscribe(int) (<-chan wgotailscale.Event, func()) {
	return c.events, c.closeEvents
}
func (c *tailscaleTestClient) ResumeInteraction(id uint64) error {
	c.mu.Lock()
	c.resumeCalls = append(c.resumeCalls, id)
	c.mu.Unlock()
	return nil
}
func (c *tailscaleTestClient) closeEvents() {
	c.eventsOnce.Do(func() { close(c.events) })
}
func (c *tailscaleTestClient) isClosed() bool { return c != nil && c.closed.Load() }
func (c *tailscaleTestClient) resumed(id uint64) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.resumeCalls) == 1 && c.resumeCalls[0] == id
}

func tailscaleTestDevice(t *testing.T) *wgdevice.Device {
	t.Helper()
	device := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
	key, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate WireGuard key: %v", err)
	}
	if err := device.SetPrivateKey(key); err != nil {
		t.Fatalf("set WireGuard key: %v", err)
	}
	t.Cleanup(device.Close)
	return device
}

func eventuallyTailscale(t *testing.T, check func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}
