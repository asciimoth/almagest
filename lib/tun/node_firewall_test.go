package tunnodes

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	gonnecttun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestTunFirewallNodeMergesLiveObjectPoliciesAndReattaches(t *testing.T) {
	providers := newTestTunProviderRegistry()
	w := newTunFirewallTestWorkspace(t, providers)
	provider := addTunFirewallTestNode(t, w, testTunProviderNodeType, "provider")
	firewall := addTunFirewallTestNode(t, w, NodeTypeTunFirewall, "firewall")
	tcpConfig := addTunFirewallTestNode(t, w, std.NodeTypeObjectConstant, "tcp-config")
	udpConfig := addTunFirewallTestNode(t, w, std.NodeTypeObjectConstant, "udp-config")
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	if _, err := w.AddNode(receiver, "test/TunReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	applyTunFirewallObject(t, w, tcpConfig, `{"exclude":[{"network":"tcp","ports":[80]}]}`)
	applyTunFirewallObject(t, w, udpConfig, `{"exclude":[{"network":"udp","ports":[53]}]}`)
	providerSnapshot := testkit.MustNodeSnapshot(t, w, provider)
	firewallSnapshot := testkit.MustNodeSnapshot(t, w, firewall)
	inputLink := addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[0], providerSnapshot.RightPorts[0], TypeTun)
	addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, tcpConfig).RightPorts[0], std.TypeObject)
	udpConfigLink := addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, udpConfig).RightPorts[0], std.TypeObject)
	outputLink := addTunFirewallTestLink(t, w, receiver.in, firewallSnapshot.RightPorts[0], TypeTun)

	first := receiveTunPayload(t, receiver.events)
	wrapped, ok := first.Tun.(*gonnecttun.Firewall)
	if !ok {
		t.Fatalf("output = %T, want *tun.Firewall", first.Tun)
	}
	assertTunFirewallBlocks(t, wrapped.GetConfig(), "tcp", "10.0.0.2:80", true)
	assertTunFirewallBlocks(t, wrapped.GetConfig(), "udp", "10.0.0.2:53", true)

	applyTunFirewallObject(t, w, tcpConfig, `{"exclude":[{"network":"tcp","ports":[443]}]}`)
	assertNoTunFirewallEvent(t, receiver.events)
	assertTunFirewallBlocks(t, wrapped.GetConfig(), "tcp", "10.0.0.2:80", false)
	assertTunFirewallBlocks(t, wrapped.GetConfig(), "tcp", "10.0.0.2:443", true)

	w.RemoveLink(udpConfigLink)
	assertTunFirewallBlocks(t, wrapped.GetConfig(), "udp", "10.0.0.2:53", false)
	udpConfigLink = addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, udpConfig).RightPorts[0], std.TypeObject)
	assertTunFirewallBlocks(t, wrapped.GetConfig(), "udp", "10.0.0.2:53", true)

	w.RemoveLink(inputLink)
	assertTunFirewallNodeLabel(t, w, firewall, "waiting for TUN")
	inputLink = addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[0], providerSnapshot.RightPorts[0], TypeTun)
	second := receiveTunPayload(t, receiver.events)
	if second.Tun == first.Tun {
		t.Fatal("TUN input reattach reused the closed firewall wrapper")
	}

	w.RemoveLink(outputLink)
	outputLink = addTunFirewallTestLink(t, w, receiver.in, firewallSnapshot.RightPorts[0], TypeTun)
	third := receiveTunPayload(t, receiver.events)
	if third.Tun == second.Tun {
		t.Fatal("TUN output reattach reused the closed firewall wrapper")
	}
	_ = providers.peer(t, provider)
	_ = inputLink
	_ = outputLink
	_ = udpConfigLink
}

func TestTunFirewallMenuSaveRestoreAndCopyPaste(t *testing.T) {
	w := newTunFirewallTestWorkspace(t, nil)
	state, sub := testkit.SubscribeMenuState(t, w)
	t.Cleanup(func() { w.UnsubscribeNotifications(sub) })
	firewall := addTunFirewallTestNode(t, w, NodeTypeTunFirewall, "firewall")
	firstConfig := addTunFirewallTestNode(t, w, std.NodeTypeObjectConstant, "first-config")
	secondConfig := addTunFirewallTestNode(t, w, std.NodeTypeObjectConstant, "second-config")
	w.SubscribeNodeMenu(firewall, sub)

	menuRules := `{"exclude":[{"network":"tcp","ports":[8080]}]}`
	applyTunFirewallMenu(t, w, firewall, menuRules)
	applyTunFirewallObject(t, w, firstConfig, `{"exclude":[{"network":"udp","ports":[5353]}]}`)
	applyTunFirewallObject(t, w, secondConfig, `{"include":[{"network":"tcp","ports":[8443]}]}`)
	firewallSnapshot := testkit.MustNodeSnapshot(t, w, firewall)
	addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, firstConfig).RightPorts[0], std.TypeObject)
	addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, secondConfig).RightPorts[0], std.TypeObject)

	menu := mustTunFirewallMenu(t, state, firewall)
	configItem, ok := testkit.FindItem(testkit.MustFindBlock(t, menu, TunFirewallConfigBlockID), TunFirewallRulesField)
	if !ok || configItem.Field == nil || !configItem.Multiline || configItem.Placeholder == "" {
		t.Fatalf("config field = %#v, want multiline field with placeholder", configItem)
	}
	effective := testkit.ReadonlyMenuFieldString(t, state, firewall, TunFirewallEffectiveBlockID, TunFirewallEffectiveField)
	for _, want := range []string{`8080`, `5353`, `8443`} {
		if !strings.Contains(effective, want) {
			t.Fatalf("effective config = %s, want %s", effective, want)
		}
	}

	clip := w.Copy([]uint64{firewall, firstConfig, secondConfig})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := w.Paste(clip)
	if len(pasted) != 3 {
		t.Fatalf("Paste returned %d nodes, want 3", len(pasted))
	}
	pastedFirewall := tunFirewallNodeByClass(t, w, pasted, NodeTypeTunFirewall)
	pastedSnapshot := testkit.MustNodeSnapshot(t, w, pastedFirewall)
	if got := len(mustTunFirewallPortSnapshot(t, w, pastedSnapshot.LeftPorts[1]).Links); got != 2 {
		t.Fatalf("pasted firewall config links = %d, want 2", got)
	}
	w.SubscribeNodeMenu(pastedFirewall, sub)
	pastedEffective := testkit.ReadonlyMenuFieldString(t, state, pastedFirewall, TunFirewallEffectiveBlockID, TunFirewallEffectiveField)
	for _, want := range []string{`8080`, `5353`, `8443`} {
		if !strings.Contains(pastedEffective, want) {
			t.Fatalf("pasted effective config = %s, want %s", pastedEffective, want)
		}
	}

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	assertStructuredTunFirewallRules(
		t,
		cfg,
		configer.Path{"firewall", TunFirewallRulesField},
	)
	restored, err := pasta.WorkspaceFromConfig([]pasta.NodeClass{
		TunFirewallClass{},
		std.ObjectConstantClass{},
	}, cfg, logging.NewDebugLogFactory(1024))
	if err != nil {
		t.Fatalf("WorkspaceFromConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	restoredFirewall, ok := restored.NodeIDByName("firewall")
	if !ok {
		t.Fatal("restored firewall missing")
	}
	restoredSnapshot := testkit.MustNodeSnapshot(t, restored, restoredFirewall)
	if got := len(mustTunFirewallPortSnapshot(t, restored, restoredSnapshot.LeftPorts[1]).Links); got != 2 {
		t.Fatalf("restored firewall config links = %d, want 2", got)
	}
	restoredState, restoredSub := testkit.SubscribeMenuState(t, restored)
	t.Cleanup(func() { restored.UnsubscribeNotifications(restoredSub) })
	restored.SubscribeNodeMenu(restoredFirewall, restoredSub)
	restoredEffective := testkit.ReadonlyMenuFieldString(t, restoredState, restoredFirewall, TunFirewallEffectiveBlockID, TunFirewallEffectiveField)
	for _, want := range []string{`8080`, `5353`, `8443`} {
		if !strings.Contains(restoredEffective, want) {
			t.Fatalf("restored effective config = %s, want %s", restoredEffective, want)
		}
	}
}

func assertStructuredTunFirewallRules(t *testing.T, cfg configer.Config, path configer.Path) {
	t.Helper()
	raw, err := cfg.Get(path)
	if err != nil {
		t.Fatalf("get saved TUN firewall rules: %v", err)
	}
	if _, ok := raw.(string); ok {
		t.Fatalf("saved TUN firewall rules = %T, want structured subgraph", raw)
	}
	root, ok := configer.MapStringAny(raw)
	if !ok {
		t.Fatalf("saved TUN firewall rules = %T, want config map", raw)
	}
	if _, ok := root["exclude"]; !ok {
		t.Fatalf("saved TUN firewall rules have no exclude subgraph: %#v", root)
	}
}

func TestTunFirewallForwardsAndForgetsDownstreamTunConfig(t *testing.T) {
	w := newTunFirewallTestWorkspace(t, nil)
	firewall := addTunFirewallTestNode(t, w, NodeTypeTunFirewall, "firewall")
	upstream := &tunConfigReceiverNode{configs: make(chan TunConfig, 4)}
	upstreamID, err := w.AddNode(upstream, "test/TunConfigReceiver", "upstream")
	if err != nil {
		t.Fatalf("AddNode upstream: %v", err)
	}
	downstream := &tunPayloadReceiver{events: make(chan TunPayload, 1)}
	downstreamID, err := w.AddNode(downstream, "test/TunReceiver", "downstream")
	if err != nil {
		t.Fatalf("AddNode downstream: %v", err)
	}
	firewallSnapshot := testkit.MustNodeSnapshot(t, w, firewall)
	addTunFirewallTestLink(t, w, firewallSnapshot.LeftPorts[0], upstream.out, TypeTun)
	discardTunConfig(t, upstream.configs)
	outputLink := addTunFirewallTestLink(t, w, downstream.in, firewallSnapshot.RightPorts[0], TypeTun)
	want := TunConfig{Addrs: []string{"10.42.0.1/24"}, Routes: []string{"10.43.0.0/16"}, MTU: 1280, DNS: "1.1.1.1"}
	w.EmitEvent(downstreamID, outputLink, want)
	assertTunConfigReceived(t, upstream.configs, want)

	w.RemoveLink(outputLink)
	assertTunConfigReceived(t, upstream.configs, TunConfig{})
	_ = upstreamID
}

func TestTunFirewallIgnoresStaleInputFailure(t *testing.T) {
	currentSource, currentPeer := gonnecttun.Pipe(1, 1500, 0, 0)
	staleSource, stalePeer := gonnecttun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = currentSource.Close()
		_ = currentPeer.Close()
		_ = staleSource.Close()
		_ = stalePeer.Close()
	})
	current := newTunInputFailureObserver(currentSource, nil)
	stale := newTunInputFailureObserver(staleSource, nil)
	node := &tunFirewallNode{
		inLink:   41,
		tun:      currentSource,
		inputTun: current,
		inputGen: 7,
	}

	for _, failed := range []tunFirewallInputFailed{
		{link: 41, generation: 6, attachment: current, err: gonnecttun.ErrDetachedTunFailed},
		{link: 41, generation: 7, attachment: stale, err: gonnecttun.ErrDetachedTunFailed},
		{link: 42, generation: 7, attachment: current, err: gonnecttun.ErrDetachedTunFailed},
	} {
		if err := node.OnInbox(pasta.InboxMessage{Payload: failed}); err != nil {
			t.Fatalf("OnInbox(%+v): %v", failed, err)
		}
		if node.tun != currentSource || node.inputTun != current || node.inputGen != 7 {
			t.Fatal("stale failure changed the current firewall input")
		}
	}
}

func newTunFirewallTestWorkspace(t *testing.T, providers *testTunProviderRegistry) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	for _, class := range []pasta.NodeClass{
		TunFirewallClass{},
		testTunProviderClass{registry: providers},
		std.ObjectConstantClass{},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w
}

func addTunFirewallTestNode(t *testing.T, w *pasta.Workspace, class, name string) uint64 {
	t.Helper()
	id, err := w.AddNodeByClass(class, name)
	if err != nil {
		t.Fatalf("AddNodeByClass %s: %v", class, err)
	}
	return id
}

func addTunFirewallTestLink(t *testing.T, w *pasta.Workspace, a, b uint64, wantType string) uint64 {
	t.Helper()
	link, linkType, err := w.AddLink(a, b)
	if err != nil {
		t.Fatalf("AddLink %d <-> %d: %v", a, b, err)
	}
	if linkType != wantType {
		t.Fatalf("link type = %q, want %q", linkType, wantType)
	}
	return link
}

func applyTunFirewallObject(t *testing.T, w *pasta.Workspace, node uint64, text string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "state",
		Values:      map[string]any{"value": text},
	})
}

func applyTunFirewallMenu(t *testing.T, w *pasta.Workspace, node uint64, text string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     TunFirewallConfigBlockID,
		Values:      map[string]any{TunFirewallRulesField: text},
	})
}

func assertTunFirewallBlocks(t *testing.T, config *gonnect.FirewallConfig, network, address string, want bool) {
	t.Helper()
	if got := config.BlocksOutgoing(network, address); got != want {
		t.Fatalf("BlocksOutgoing(%q, %q) = %v, want %v; config=%#v", network, address, got, want, config)
	}
}

func assertNoTunFirewallEvent(t *testing.T, events <-chan TunPayload) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected TUN payload: %#v", event)
	default:
	}
}

func assertTunFirewallNodeLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	if got := testkit.MustNodeSnapshot(t, w, node).Label; got != want {
		t.Fatalf("node label = %q, want %q", got, want)
	}
}

func mustTunFirewallMenu(t *testing.T, state *formular.MenuSnapshotState, node uint64) formular.MenuSnapshotMessage {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	return snapshot
}

func mustTunFirewallPortSnapshot(t *testing.T, w *pasta.Workspace, port uint64) pasta.PortSnapshot {
	t.Helper()
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		t.Fatalf("port snapshot %d missing", port)
	}
	return snapshot
}

func tunFirewallNodeByClass(t *testing.T, w *pasta.Workspace, nodes []uint64, class string) uint64 {
	t.Helper()
	for _, node := range nodes {
		if snapshot, ok := w.NodeSnapshot(node); ok && snapshot.Class == class {
			return node
		}
	}
	t.Fatalf("no pasted node with class %q", class)
	return 0
}
