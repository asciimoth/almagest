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

func TestVTunEffectiveAddressFamiliesReflectRuntimeAddresses(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)
	netReceiver := networktest.NewReceiver(4)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "addressFamilies", "ipv4\nipv6")

	values := vtunExternalTestFormValues("ipv4-vtun")
	values["localAddrs"] = "192.0.2.2"
	values["noLoopbackAddr"] = true
	w.SendNodeFormularMsg(vtunID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(vtunID)},
		BlockID:     "config",
		Values:      values,
	})
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "addressFamilies", "ipv4")

	values = vtunExternalTestFormValues("ipv6-vtun")
	values["localAddrs"] = "2001:db8::2"
	values["noLoopbackAddr"] = true
	w.SendNodeFormularMsg(vtunID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(vtunID)},
		BlockID:     "config",
		Values:      values,
	})
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "addressFamilies", "ipv6")
}

func vtunExternalTestFormValues(name string) map[string]any {
	return map[string]any{
		"name":           name,
		"localAddrs":     "",
		"dnsServers":     DefaultVTunDNSServer,
		"mtu":            DefaultVTunMTU,
		"endpointCh":     defaultVTunEndpointCh,
		"eventCh":        defaultVTunEventCh,
		"noLoopbackAddr": false,
		"disableSACK":    false,
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

func TestVTunAutomaticMTUAndPositiveCeiling(t *testing.T) {
	config := defaultVTunConfig()
	if config.mtu != 0 {
		t.Fatalf("default VTun MTU = %d, want automatic value 0", config.mtu)
	}
	node := &vtunNode{config: config, tunConfig: TunConfig{MTU: 1280}}
	if mtu, _ := node.effectiveMTU(); mtu != 1280 {
		t.Fatalf("automatic effective MTU = %d, want 1280", mtu)
	}
	node.config.mtu = 1400
	if mtu, _ := node.effectiveMTU(); mtu != 1280 {
		t.Fatalf("effective MTU with larger ceiling = %d, want 1280", mtu)
	}
	node.config.mtu = 1200
	if mtu, constraint := node.effectiveMTU(); mtu != 1200 || constraint.Reason != "user MTU ceiling" {
		t.Fatalf("effective MTU with smaller ceiling = %d, %+v; want user ceiling 1200", mtu, constraint)
	}
	node.config.mtu = 0
	node.tunConfig = TunConfig{}
	if mtu, constraint := node.effectiveMTU(); mtu != DefaultVTunMTU || !strings.Contains(constraint.Reason, "fallback") {
		t.Fatalf("fallback MTU = %d, %+v; want %d", mtu, constraint, DefaultVTunMTU)
	}
}

func TestVTunConfigAcceptsAutomaticMTUAndRejectsNegative(t *testing.T) {
	config := defaultVTunConfig()
	config.mtu = 0
	if err := validateVTunConfig(config); err != nil {
		t.Fatalf("validateVTunConfig(MTU 0) error = %v", err)
	}
	config.mtu = -1
	if err := validateVTunConfig(config); err == nil {
		t.Fatal("validateVTunConfig() accepted a negative MTU")
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

func TestVTunCompletesSuggestedSourceRouteFamiliesWithDefaults(t *testing.T) {
	default4 := netip.MustParseAddr("100.82.132.84")
	default6 := netip.MustParseAddr("fd7a:115c:a1e0::962f:8455")
	tail4 := TunSourceRoute{
		Destination: netip.MustParsePrefix("100.64.0.0/10"),
		Source:      default4,
	}
	tail6 := TunSourceRoute{
		Destination: netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
		Source:      default6,
	}
	node := &vtunNode{
		config: defaultVTunConfig(),
		tunConfig: TunConfig{
			Addrs:        []string{default4.String() + "/32", default6.String() + "/128"},
			Routes:       []string{tail4.Destination.String(), tail6.Destination.String()},
			SourceRoutes: []TunSourceRoute{tail4, tail6},
		},
	}

	runtimeConfig, opts, err := node.buildRuntimeConfig()
	if err != nil {
		t.Fatalf("build runtime config: %v", err)
	}
	want := []netstackvtun.SourceRoute{
		{Destination: tail4.Destination, Source: default4},
		{Destination: tail6.Destination, Source: default6},
		{Destination: netip.MustParsePrefix("0.0.0.0/0"), Source: default4},
		{Destination: netip.MustParsePrefix("::/0"), Source: default6},
	}
	if !slices.Equal(runtimeConfig.sourceRoutes, want) {
		t.Fatalf("runtime source routes = %#v, want %#v", runtimeConfig.sourceRoutes, want)
	}
	device, err := opts.Build()
	if err != nil {
		t.Fatalf("build VTun: %v", err)
	}
	t.Cleanup(func() { _ = device.Close() })
	dnsConn, err := device.DialUDPAddrPort(
		netip.AddrPort{}, netip.MustParseAddrPort("8.8.8.8:53"),
	)
	if err != nil {
		t.Fatalf("dial DNS through inferred default source route: %v", err)
	}
	if err := dnsConn.Close(); err != nil {
		t.Fatalf("close DNS connection: %v", err)
	}
}

func TestVTunCompletesSourceRoutesThroughTunMiddleware(t *testing.T) {
	tests := []struct {
		name  string
		class func(Runtime) pasta.NodeClass
	}{
		{
			name: "TunFirewall",
			class: func(runtime Runtime) pasta.NodeClass {
				return TunFirewallClass{Runtime: runtime}
			},
		},
		{
			name: "LogTun",
			class: func(runtime Runtime) pasta.NodeClass {
				return DebugLogTunClass{Runtime: runtime}
			},
		},
		{
			name: "TunJoiner default input",
			class: func(runtime Runtime) pasta.NodeClass {
				return TunJoinerClass{Runtime: runtime}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
			t.Cleanup(w.Close)
			runtime := vtunTestRuntime{ctx: context.Background(), sys: &sysnetdebug.System{}}
			for _, class := range []pasta.NodeClass{
				VTunClass{Runtime: runtime},
				test.class(runtime),
			} {
				if err := w.AddNodeClass(class); err != nil {
					t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
				}
			}

			vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
			if err != nil {
				t.Fatalf("AddNodeByClass VTun: %v", err)
			}
			middlewareID, err := w.AddNodeByClass(test.class(runtime).ClassName(), "middleware")
			if err != nil {
				t.Fatalf("AddNodeByClass middleware: %v", err)
			}
			netReceiver := networktest.NewReceiver(4)
			if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
				t.Fatalf("AddNode network receiver: %v", err)
			}
			consumer := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
			consumerID, err := w.AddNode(consumer, "test/TunReceiver", "consumer")
			if err != nil {
				t.Fatalf("AddNode consumer: %v", err)
			}

			vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
			middlewareSnapshot := testkit.MustNodeSnapshot(t, w, middlewareID)
			if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
				t.Fatalf("AddLink VTun network: %v", err)
			}
			_ = networktest.ReceivePayload(t, netReceiver.Events)
			if _, _, err := w.AddLink(middlewareSnapshot.LeftPorts[0], vtunSnapshot.RightPorts[0]); err != nil {
				t.Fatalf("AddLink VTun to middleware: %v", err)
			}
			outputLink, _, err := w.AddLink(consumer.in, middlewareSnapshot.RightPorts[0])
			if err != nil {
				t.Fatalf("AddLink middleware to consumer: %v", err)
			}
			_ = receiveTunPayload(t, consumer.events)

			source4 := netip.MustParseAddr("100.82.132.84")
			source6 := netip.MustParseAddr("fd7a:115c:a1e0::962f:8455")
			specific4 := TunSourceRoute{
				Destination: netip.MustParsePrefix("100.64.0.0/10"),
				Source:      source4,
			}
			specific6 := TunSourceRoute{
				Destination: netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
				Source:      source6,
			}
			w.EmitEvent(consumerID, outputLink, TunConfig{
				Addrs:        []string{source4.String() + "/32", source6.String() + "/128"},
				Routes:       []string{specific4.Destination.String(), specific6.Destination.String()},
				SourceRoutes: []TunSourceRoute{specific4, specific6},
			})

			network := networktest.ReceivePayload(t, netReceiver.Events).Network
			detached, ok := network.(*gonnect.DetachedNetwork)
			if !ok {
				t.Fatalf("network = %T, want *gonnect.DetachedNetwork", network)
			}
			device, ok := detached.GetWrapped().(*netstackvtun.VTun)
			if !ok {
				t.Fatalf("wrapped network = %T, want *vtun.VTun", detached.GetWrapped())
			}
			want := []netstackvtun.SourceRoute{
				{Destination: specific4.Destination, Source: source4},
				{Destination: specific6.Destination, Source: source6},
				{Destination: netip.MustParsePrefix("0.0.0.0/0"), Source: source4},
				{Destination: netip.MustParsePrefix("::/0"), Source: source6},
			}
			if got := device.SourceRoutes(); !slices.Equal(got, want) {
				t.Fatalf("VTun source routes = %#v, want %#v", got, want)
			}
			for _, target := range []netip.AddrPort{
				netip.MustParseAddrPort("8.8.8.8:53"),
				netip.MustParseAddrPort("[2001:4860:4860::8888]:53"),
			} {
				conn, err := device.DialUDPAddrPort(netip.AddrPort{}, target)
				if err != nil {
					t.Fatalf("DialUDPAddrPort(%s): %v", target, err)
				}
				if err := conn.Close(); err != nil {
					t.Fatalf("close UDP connection to %s: %v", target, err)
				}
			}
		})
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
