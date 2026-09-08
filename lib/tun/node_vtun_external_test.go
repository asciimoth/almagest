package tunnodes

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/almagest/lib/testkit/tuntest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestVTunNodeEmitsNetworkAndTunAndRebuildsOnFormApply(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	netReceiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	tunReceiver := tuntest.NewReceiver(3)
	if _, err := w.AddNode(tunReceiver, "test/TunReceiver", "tun receiver"); err != nil {
		t.Fatalf("AddNode tun receiver: %v", err)
	}

	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(tunReceiver.In, vtunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	firstNetwork := networktest.ReceivePayload(t, netReceiver.Events).Network
	firstTun := tuntest.ReceivePayload(t, tunReceiver.Events).Tun
	firstNetworkWrapper, ok := firstNetwork.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("first network = %T, want *gonnect.DetachedNetwork", firstNetwork)
	}

	w.SendNodeFormularMsg(vtunID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(vtunID)},
		BlockID:     "config",
		Values: map[string]any{
			"name":           "vtun-test",
			"localAddrs":     "10.123.0.2",
			"dnsServers":     "1.1.1.1",
			"mtu":            1300,
			"endpointCh":     16,
			"eventCh":        4,
			"mwo":            1,
			"mro":            2,
			"noLoopbackAddr": true,
			"disableSACK":    true,
		},
	})

	secondNetwork := networktest.ReceivePayload(t, netReceiver.Events).Network
	secondTun := tuntest.ReceivePayload(t, tunReceiver.Events).Tun
	secondNetworkWrapper, ok := secondNetwork.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("second network = %T, want *gonnect.DetachedNetwork", secondNetwork)
	}
	if firstNetworkWrapper.GetWrapped() == secondNetworkWrapper.GetWrapped() {
		t.Fatal("VTun form apply reused wrapped VTun, want rebuilt instance")
	}
	if nodekit.SameTunSource(firstTun, secondTun) {
		t.Fatal("VTun form apply reused TUN source, want rebuilt instance")
	}
	if up, err := firstNetworkWrapper.IsUp(); err != nil || up {
		t.Fatalf("old network wrapper IsUp = %v, %v; want false, nil", up, err)
	}
}

func TestVTunEquivalentTunConfigDoesNotRebuildActiveVTun(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)
	netReceiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	tunReceiver := tuntest.NewReceiver(3)
	tunReceiverID, err := w.AddNode(tunReceiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode tun receiver: %v", err)
	}

	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	tunLink, _, err := w.AddLink(tunReceiver.In, vtunSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	_ = tuntest.ReceivePayload(t, tunReceiver.Events)
	networktest.AssertNoEvent(t, netReceiver.Events)

	addrs := strings.Fields(testkit.ReadonlyMenuFieldString(t, state, vtunID, "effective", "localAddrs"))
	if len(addrs) == 0 {
		t.Fatal("effective localAddrs is empty, want dynamic VTun address")
	}

	w.EmitEvent(tunReceiverID, tunLink, TunConfig{
		Addrs: addrs,
		MTU:   DefaultVTunMTU,
		DNS:   DefaultVTunDNSServer,
	})

	networktest.AssertNoEvent(t, netReceiver.Events)
}

func TestVTunGeneratedAddrsAreNotSaved(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)
	testkit.AssertLeadingBlock(t, state, vtunID, "effective")
	netReceiver := networktest.NewReceiver(1)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)

	localAddrs := testkit.ReadonlyMenuFieldString(t, state, vtunID, "effective", "localAddrs")
	if len(strings.Fields(localAddrs)) == 0 {
		t.Fatal("effective localAddrs is empty, want engine allocation cached")
	}
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "localAddrs", localAddrs)
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "dnsServers", DefaultVTunDNSServer)
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "mtu", DefaultVTunMTU)
	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw := configer.String(cfg.View(configer.Path{"vtun"}), configer.Path{"localAddrs"}, "")
	if raw != "" {
		t.Fatalf("saved localAddrs = %q, want empty generated value omitted", raw)
	}
}

func TestVTunUsesDefaultDNSWhenNotConfigured(t *testing.T) {
	node := &vtunNode{config: defaultVTunConfig()}
	runtimeConfig, opts, err := node.buildRuntimeConfig()
	if err != nil {
		t.Fatalf("build runtime config: %v", err)
	}
	want := netip.MustParseAddr(DefaultVTunDNSServer)
	if len(runtimeConfig.dnsServers) != 1 || runtimeConfig.dnsServers[0] != want {
		t.Fatalf("runtime DNS servers = %v, want [%s]", runtimeConfig.dnsServers, want)
	}
	if len(opts.DnsServers) != 1 || opts.DnsServers[0] != want {
		t.Fatalf("VTun DNS servers = %v, want [%s]", opts.DnsServers, want)
	}
}

func TestVTunUsesSuggestedDNSWhenNotConfigured(t *testing.T) {
	node := &vtunNode{
		config:    defaultVTunConfig(),
		tunConfig: TunConfig{DNS: "1.1.1.1"},
	}
	runtimeConfig, opts, err := node.buildRuntimeConfig()
	if err != nil {
		t.Fatalf("build runtime config: %v", err)
	}
	want := netip.MustParseAddr("1.1.1.1")
	if !slices.Equal(runtimeConfig.dnsServers, []netip.Addr{want}) {
		t.Fatalf("runtime DNS servers = %v, want [%s]", runtimeConfig.dnsServers, want)
	}
	if !slices.Equal(opts.DnsServers, []netip.Addr{want}) {
		t.Fatalf("VTun DNS servers = %v, want [%s]", opts.DnsServers, want)
	}
}

func TestVTunConfiguredDNSOverridesSuggestion(t *testing.T) {
	config := defaultVTunConfig()
	config.dnsServers = "9.9.9.9\n149.112.112.112"
	node := &vtunNode{
		config:    config,
		tunConfig: TunConfig{DNS: "1.1.1.1"},
	}
	runtimeConfig, _, err := node.buildRuntimeConfig()
	if err != nil {
		t.Fatalf("build runtime config: %v", err)
	}
	want := []netip.Addr{
		netip.MustParseAddr("9.9.9.9"),
		netip.MustParseAddr("149.112.112.112"),
	}
	if !slices.Equal(runtimeConfig.dnsServers, want) {
		t.Fatalf("runtime DNS servers = %v, want %v", runtimeConfig.dnsServers, want)
	}
}

func TestVTunUsesSuggestedSourceRoutes(t *testing.T) {
	defaultSource := netip.MustParseAddr("10.20.0.2")
	tailnetSource := netip.MustParseAddr("100.64.0.2")
	suggested := []TunSourceRoute{
		{Destination: netip.MustParsePrefix("0.0.0.0/0"), Source: defaultSource},
		{Destination: netip.MustParsePrefix("100.64.0.0/10"), Source: tailnetSource},
	}
	node := &vtunNode{
		config: defaultVTunConfig(),
		tunConfig: TunConfig{
			Addrs:        []string{defaultSource.String(), tailnetSource.String()},
			SourceRoutes: suggested,
		},
	}

	runtimeConfig, opts, err := node.buildRuntimeConfig()
	if err != nil {
		t.Fatalf("build runtime config: %v", err)
	}
	want := []netstackvtun.SourceRoute{
		{Destination: suggested[0].Destination, Source: defaultSource},
		{Destination: suggested[1].Destination, Source: tailnetSource},
	}
	if !slices.Equal(runtimeConfig.sourceRoutes, want) {
		t.Fatalf("runtime source routes = %#v, want %#v", runtimeConfig.sourceRoutes, want)
	}
	if !slices.Equal(opts.SourceRoutes, want) {
		t.Fatalf("VTun source routes = %#v, want %#v", opts.SourceRoutes, want)
	}
}

func TestVTunExplicitLocalAddrsDisableSuggestedSourceRoutes(t *testing.T) {
	config := defaultVTunConfig()
	config.localAddrs = "192.0.2.2"
	node := &vtunNode{
		config: config,
		tunConfig: TunConfig{
			Addrs: []string{"10.20.0.2", "100.64.0.2"},
			SourceRoutes: []TunSourceRoute{{
				Destination: netip.MustParsePrefix("100.64.0.0/10"),
				Source:      netip.MustParseAddr("100.64.0.2"),
			}},
		},
	}

	runtimeConfig, opts, err := node.buildRuntimeConfig()
	if err != nil {
		t.Fatalf("build runtime config: %v", err)
	}
	if len(runtimeConfig.sourceRoutes) != 0 || len(opts.SourceRoutes) != 0 {
		t.Fatalf("source routes = %#v, %#v; want none with explicit local addrs", runtimeConfig.sourceRoutes, opts.SourceRoutes)
	}
}

func TestVTunUpdatesOnlySourceRoutesInPlaceAndRejectsConflictAtomically(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	netReceiver := networktest.NewReceiver(4)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	tunReceiver := tuntest.NewReceiver(4)
	tunReceiverID, err := w.AddNode(tunReceiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode tun receiver: %v", err)
	}

	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	tunLink, _, err := w.AddLink(tunReceiver.In, vtunSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	_ = tuntest.ReceivePayload(t, tunReceiver.Events)

	defaultSource := netip.MustParseAddr("10.20.0.2")
	tailnetSource := netip.MustParseAddr("100.64.0.2")
	base := TunConfig{
		Addrs: []string{defaultSource.String(), tailnetSource.String()},
		SourceRoutes: []TunSourceRoute{
			{Destination: netip.MustParsePrefix("0.0.0.0/0"), Source: defaultSource},
			{Destination: netip.MustParsePrefix("100.64.0.0/10"), Source: tailnetSource},
		},
	}
	w.EmitEvent(tunReceiverID, tunLink, base)
	networkPayload := networktest.ReceivePayload(t, netReceiver.Events)
	_ = tuntest.ReceivePayload(t, tunReceiver.Events)
	detached, ok := networkPayload.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("network = %T, want *gonnect.DetachedNetwork", networkPayload.Network)
	}
	device, ok := detached.GetWrapped().(*netstackvtun.VTun)
	if !ok {
		t.Fatalf("wrapped network = %T, want *vtun.VTun", detached.GetWrapped())
	}

	replacement := base.Clone()
	replacement.SourceRoutes = append(replacement.SourceRoutes, TunSourceRoute{
		Destination: netip.MustParsePrefix("100.80.0.0/16"),
		Source:      defaultSource,
	})
	w.EmitEvent(tunReceiverID, tunLink, replacement)
	networktest.AssertNoEvent(t, netReceiver.Events)
	tuntest.AssertNoEvent(t, tunReceiver.Events)
	wantReplacement := []netstackvtun.SourceRoute{
		{Destination: replacement.SourceRoutes[0].Destination, Source: defaultSource},
		{Destination: replacement.SourceRoutes[1].Destination, Source: tailnetSource},
		{Destination: replacement.SourceRoutes[2].Destination, Source: defaultSource},
	}
	if got := device.SourceRoutes(); !slices.Equal(got, wantReplacement) {
		t.Fatalf("source routes after live update = %#v, want %#v", got, wantReplacement)
	}

	conflicting := replacement.Clone()
	conflicting.SourceRoutes = append(conflicting.SourceRoutes, TunSourceRoute{
		Destination: netip.MustParsePrefix("100.64.0.0/10"),
		Source:      defaultSource,
	})
	w.EmitEvent(tunReceiverID, tunLink, conflicting)
	networktest.AssertNoEvent(t, netReceiver.Events)
	tuntest.AssertNoEvent(t, tunReceiver.Events)
	if got := device.SourceRoutes(); !slices.Equal(got, wantReplacement) {
		t.Fatalf("source routes after rejected update = %#v, want unchanged %#v", got, wantReplacement)
	}
	if got := testkit.MustNodeSnapshot(t, w, vtunID).Label; got != "invalid config" {
		t.Fatalf("VTun label after conflicting routes = %q, want invalid config", got)
	}

	w.EmitEvent(tunReceiverID, tunLink, replacement)
	if got := testkit.MustNodeSnapshot(t, w, vtunID).Label; got != defaultVTunName {
		t.Fatalf("VTun label after valid route recovery = %q, want %q", got, defaultVTunName)
	}
}

func TestVTunNodeValidationReportsStatus(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "localAddrs"},
		Value:       "not-an-ip",
	})
	testkit.AssertMenuItemStatus(t, state, vtunID, "localAddrs", formular.StatusError)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "localAddrs"},
		Value:       "",
	})
	testkit.AssertMenuItemStatus(t, state, vtunID, "localAddrs", formular.StatusOK)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       DefaultVTunMTU,
	})
	testkit.AssertMenuItemStatus(t, state, vtunID, "mtu", formular.StatusOK)
}

func newVTunNodeTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	runtime := vtunTestRuntime{ctx: context.Background(), sys: &sysnetdebug.System{}}
	if err := w.AddNodeClass(VTunClass{Runtime: runtime}); err != nil {
		t.Fatalf("AddNodeClass VTun: %v", err)
	}
	return w
}

type vtunTestRuntime struct {
	ctx context.Context
	sys sysnet.System
}

func (r vtunTestRuntime) Context() context.Context { return r.ctx }
func (vtunTestRuntime) Pool() bufpool.Pool         { return nil }
func (r vtunTestRuntime) System() sysnet.System    { return r.sys }
func (vtunTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (vtunTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (vtunTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (vtunTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (vtunTestRuntime) WorkerSpawner(uint64) WorkerSpawner {
	return nil
}
func (vtunTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
func (r vtunTestRuntime) AllocIP() subnet.IPAllocator {
	allocator, ok := r.sys.(interface {
		AllocIP() subnet.IPAllocator
	})
	if !ok {
		return nil
	}
	return allocator.AllocIP()
}
