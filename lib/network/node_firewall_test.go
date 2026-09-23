package networknodes

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestNetworkFirewallNodeMergesLiveObjectPoliciesAndReattaches(t *testing.T) {
	w := newNetworkFirewallTestWorkspace(t)
	source := addFirewallTestNode(t, w, remapperTestNetworkSourceNodeType, "source")
	firewall := addFirewallTestNode(t, w, NodeTypeNetworkFirewall, "firewall")
	tcpConfig := addFirewallTestNode(t, w, std.NodeTypeObjectConstant, "tcp-config")
	udpConfig := addFirewallTestNode(t, w, std.NodeTypeObjectConstant, "udp-config")
	receiver := networktest.NewReceiver(8)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	applyFirewallObject(t, w, tcpConfig, `{"exclude":[{"network":"tcp","ports":[80]}]}`)
	applyFirewallObject(t, w, udpConfig, `{"exclude":[{"network":"udp","ports":[53]}]}`)
	sourceSnapshot := testkit.MustNodeSnapshot(t, w, source)
	firewallSnapshot := testkit.MustNodeSnapshot(t, w, firewall)
	inputLink := addFirewallTestLink(t, w, sourceSnapshot.LeftPorts[0], firewallSnapshot.RightPorts[0], TypeNetwork)
	addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, tcpConfig).RightPorts[0], std.TypeObject)
	udpConfigLink := addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, udpConfig).RightPorts[0], std.TypeObject)
	outputLink := addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[0], receiver.In, TypeNetwork)

	first := networktest.ReceivePayload(t, receiver.Events)
	wrapped, ok := first.Network.(*gonnect.Firewall)
	if !ok {
		t.Fatalf("output = %T, want *gonnect.Firewall", first.Network)
	}
	assertFirewallBlocks(t, wrapped.GetConfig(), "tcp", "127.0.0.1:80", true)
	assertFirewallBlocks(t, wrapped.GetConfig(), "udp", "127.0.0.1:53", true)

	applyFirewallObject(t, w, tcpConfig, `{"exclude":[{"network":"tcp","ports":[443]}]}`)
	networktest.AssertNoEvent(t, receiver.Events)
	assertFirewallBlocks(t, wrapped.GetConfig(), "tcp", "127.0.0.1:80", false)
	assertFirewallBlocks(t, wrapped.GetConfig(), "tcp", "127.0.0.1:443", true)

	w.RemoveLink(udpConfigLink)
	assertFirewallBlocks(t, wrapped.GetConfig(), "udp", "127.0.0.1:53", false)
	udpConfigLink = addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, udpConfig).RightPorts[0], std.TypeObject)
	assertFirewallBlocks(t, wrapped.GetConfig(), "udp", "127.0.0.1:53", true)

	w.RemoveLink(inputLink)
	assertUnavailableNetworkPayload(t, receiver.Events)
	assertNodeLabel(t, w, firewall, "waiting for network")
	inputLink = addFirewallTestLink(t, w, sourceSnapshot.LeftPorts[0], firewallSnapshot.RightPorts[0], TypeNetwork)
	second := receiveLatestReadyNetworkPayload(t, receiver.Events)
	if second.Network == first.Network {
		t.Fatal("network input reattach reused the closed firewall wrapper")
	}
	assertFirewallBlocks(t, second.Network.(*gonnect.Firewall).GetConfig(), "tcp", "127.0.0.1:443", true)

	w.RemoveLink(outputLink)
	outputLink = addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[0], receiver.In, TypeNetwork)
	third := receiveLatestReadyNetworkPayload(t, receiver.Events)
	if third.Network == second.Network {
		t.Fatal("network output reattach reused the closed firewall wrapper")
	}
	_ = inputLink
	_ = outputLink
	_ = udpConfigLink
}

func assertUnavailableNetworkPayload(t *testing.T, events <-chan NetworkPayload) {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Network != nil || payload.DatagramMTU != nil || payload.CapabilityRevision != 0 {
			t.Fatalf("unavailable network payload = %+v, want zero payload", payload)
		}
	default:
		t.Fatal("no unavailable network payload received")
	}
}

func receiveLatestReadyNetworkPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	var payload NetworkPayload
	for {
		select {
		case next := <-events:
			if next.Network != nil {
				payload = next
			}
		default:
			if payload.Network == nil {
				t.Fatal("no ready network payload received")
			}
			return payload
		}
	}
}

func TestNetworkFirewallMenuSaveRestoreAndCopyPaste(t *testing.T) {
	w := newNetworkFirewallTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	t.Cleanup(func() { w.UnsubscribeNotifications(sub) })
	firewall := addFirewallTestNode(t, w, NodeTypeNetworkFirewall, "firewall")
	firstConfig := addFirewallTestNode(t, w, std.NodeTypeObjectConstant, "first-config")
	secondConfig := addFirewallTestNode(t, w, std.NodeTypeObjectConstant, "second-config")
	w.SubscribeNodeMenu(firewall, sub)

	menuRules := `{"exclude":[{"network":"tcp","ports":[8080]}]}`
	applyFirewallMenu(t, w, firewall, menuRules)
	applyFirewallObject(t, w, firstConfig, `{"exclude":[{"network":"udp","ports":[5353]}]}`)
	applyFirewallObject(t, w, secondConfig, `{"include":[{"network":"tcp","ports":[8443]}]}`)
	firewallSnapshot := testkit.MustNodeSnapshot(t, w, firewall)
	addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, firstConfig).RightPorts[0], std.TypeObject)
	addFirewallTestLink(t, w, firewallSnapshot.LeftPorts[1], testkit.MustNodeSnapshot(t, w, secondConfig).RightPorts[0], std.TypeObject)

	configItem, ok := testkit.FindItem(testkit.MustFindBlock(t, mustFirewallMenu(t, state, firewall), NetworkFirewallConfigBlockID), NetworkFirewallRulesField)
	if !ok {
		t.Fatal("firewall rules menu item missing")
	}
	if configItem.Field == nil || !configItem.Multiline || configItem.Placeholder == "" {
		t.Fatalf("config field = %#v, want multiline field with placeholder", configItem)
	}
	effective := testkit.ReadonlyMenuFieldString(t, state, firewall, NetworkFirewallEffectiveBlockID, NetworkFirewallEffectiveField)
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
	pastedFirewall := workspaceNodeByClass(t, w, pasted, NodeTypeNetworkFirewall)
	if got := len(testkit.MustNodeSnapshot(t, w, pastedFirewall).LeftPorts); got != 2 {
		t.Fatalf("pasted firewall left ports = %d, want 2", got)
	}
	if got := len(mustFirewallPortSnapshot(t, w, testkit.MustNodeSnapshot(t, w, pastedFirewall).LeftPorts[1]).Links); got != 2 {
		t.Fatalf("pasted firewall config links = %d, want 2", got)
	}
	w.SubscribeNodeMenu(pastedFirewall, sub)
	pastedEffective := testkit.ReadonlyMenuFieldString(t, state, pastedFirewall, NetworkFirewallEffectiveBlockID, NetworkFirewallEffectiveField)
	for _, want := range []string{`8080`, `5353`, `8443`} {
		if !strings.Contains(pastedEffective, want) {
			t.Fatalf("pasted effective config = %s, want %s", pastedEffective, want)
		}
	}

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	assertStructuredFirewallRules(
		t,
		cfg,
		configer.Path{"firewall", NetworkFirewallRulesField},
	)
	restored, err := pasta.WorkspaceFromConfig([]pasta.NodeClass{
		NetworkFirewallClass{},
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
	if got := len(mustFirewallPortSnapshot(t, restored, restoredSnapshot.LeftPorts[1]).Links); got != 2 {
		t.Fatalf("restored firewall config links = %d, want 2", got)
	}
	restoredState, restoredSub := testkit.SubscribeMenuState(t, restored)
	t.Cleanup(func() { restored.UnsubscribeNotifications(restoredSub) })
	restored.SubscribeNodeMenu(restoredFirewall, restoredSub)
	restoredEffective := testkit.ReadonlyMenuFieldString(t, restoredState, restoredFirewall, NetworkFirewallEffectiveBlockID, NetworkFirewallEffectiveField)
	for _, want := range []string{`8080`, `5353`, `8443`} {
		if !strings.Contains(restoredEffective, want) {
			t.Fatalf("restored effective config = %s, want %s", restoredEffective, want)
		}
	}
}

func assertStructuredFirewallRules(t *testing.T, cfg configer.Config, path configer.Path) {
	t.Helper()
	raw, err := cfg.Get(path)
	if err != nil {
		t.Fatalf("get saved firewall rules: %v", err)
	}
	if _, ok := raw.(string); ok {
		t.Fatalf("saved firewall rules = %T, want structured subgraph", raw)
	}
	root, ok := configer.MapStringAny(raw)
	if !ok {
		t.Fatalf("saved firewall rules = %T, want config map", raw)
	}
	if _, ok := root["exclude"]; !ok {
		t.Fatalf("saved firewall rules have no exclude subgraph: %#v", root)
	}
}

func newNetworkFirewallTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	for _, class := range []pasta.NodeClass{
		NetworkFirewallClass{},
		remapperTestNetworkSourceClass{},
		std.ObjectConstantClass{},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w
}

func addFirewallTestNode(t *testing.T, w *pasta.Workspace, class, name string) uint64 {
	t.Helper()
	id, err := w.AddNodeByClass(class, name)
	if err != nil {
		t.Fatalf("AddNodeByClass %s: %v", class, err)
	}
	return id
}

func addFirewallTestLink(t *testing.T, w *pasta.Workspace, a, b uint64, wantType string) uint64 {
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

func applyFirewallObject(t *testing.T, w *pasta.Workspace, node uint64, text string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formMessageBase(formular.MessageFormApply, node),
		BlockID:     "state",
		Values:      map[string]any{"value": text},
	})
}

func applyFirewallMenu(t *testing.T, w *pasta.Workspace, node uint64, text string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formMessageBase(formular.MessageFormApply, node),
		BlockID:     NetworkFirewallConfigBlockID,
		Values:      map[string]any{NetworkFirewallRulesField: text},
	})
}

func assertFirewallBlocks(t *testing.T, config *gonnect.FirewallConfig, network, address string, want bool) {
	t.Helper()
	if got := config.BlocksOutgoing(network, address); got != want {
		t.Fatalf("BlocksOutgoing(%q, %q) = %v, want %v; config=%#v", network, address, got, want, config)
	}
}

func mustFirewallMenu(t *testing.T, state *formular.MenuSnapshotState, node uint64) formular.MenuSnapshotMessage {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	return snapshot
}

func mustFirewallPortSnapshot(t *testing.T, w *pasta.Workspace, port uint64) pasta.PortSnapshot {
	t.Helper()
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		t.Fatalf("port snapshot %d missing", port)
	}
	return snapshot
}
