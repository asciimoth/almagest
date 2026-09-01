package wireguardnodes_test

import (
	"fmt"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestWGConfigClassPortsMenuAndPersistence(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWGConfig, "wg-config")
	e.SubscribeMenus(node)

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if len(snapshot.LeftPorts) != 0 || len(snapshot.RightPorts) != 2 {
		t.Fatalf("ports = left %d right %d, want zero left and two right", len(snapshot.LeftPorts), len(snapshot.RightPorts))
	}
	assertPortShape(t, e, snapshot.RightPorts[0], "right", "WireGuard", TypeWGCtrl)
	assertPortShape(t, e, snapshot.RightPorts[1], "right", "Network", TypeNetwork)

	for _, blockID := range []string{"status", "peers"} {
		if _, ok := e.MenuBlock(node, blockID); !ok {
			t.Fatalf("menu block %q missing", blockID)
		}
	}
	item, ok := e.MenuItem(node, "peers", "peers")
	if !ok || item.Field == nil || item.Kind != formular.FieldArray {
		t.Fatalf("peers item = %#v, want array field", item)
	}
	for _, fieldID := range []string{"presharedKeySecret", "endpoint", "allowedIPs", "amneziaOverride"} {
		if !wireGuardTemplateHasField(item, fieldID) {
			t.Fatalf("peers template missing %q", fieldID)
		}
	}

	_, peerKey := wireGuardTestKeyPair(t, 185)
	applyWireGuardPeers(t, e.Engine, node, []wireGuardPeerConfig{{
		id: "saved", publicKey: peerKey, endpoint: "127.0.0.1:51820", allowedIPs: "10.185.0.1/32",
	}})
	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	peer := savedWireGuardPeerConfig(t, cfg, "wg-config", 0)
	if peer["publicKey"] != peerKey || peer["endpoint"] != "127.0.0.1:51820" {
		t.Fatalf("saved WGConfig peer = %#v", peer)
	}

	restored := newNodeE2EWithConfig(t, cfg)
	if _, ok := restored.Engine.NodeByName(E2EWorkspace, "wg-config"); !ok {
		t.Fatal("restored WGConfig node is missing")
	}
	restoredCfg := configer.NewMemory(nil)
	if err := restored.Engine.SaveConfig(restoredCfg); err != nil {
		t.Fatalf("SaveConfig restored WGConfig: %v", err)
	}
	restoredPeer := savedWireGuardPeerConfig(t, restoredCfg, "wg-config", 0)
	if restoredPeer["publicKey"] != peerKey || restoredPeer["endpoint"] != "127.0.0.1:51820" {
		t.Fatalf("restored WGConfig peer = %#v", restoredPeer)
	}
}

func TestWGConfigMergesWithMenuAndMultipleObjectInputsAndRelinks(t *testing.T) {
	e := NewNodeE2E(t)
	wg := e.AddNode(NodeTypeWireGuard, "wg")
	firstConfig := e.AddNode(NodeTypeWGConfig, "wg-config-first")
	secondConfig := e.AddNode(NodeTypeWGConfig, "wg-config-second")
	firstObject := e.AddNode(std.NodeTypeObjectConstant, "object-first")
	secondObject := e.AddNode(std.NodeTypeObjectConstant, "object-second")
	mock := e.AddNode(NodeTypeMockNetwork, "network")
	e.SubscribeMenus(wg, firstConfig, secondConfig)

	keys := make([]string, 5)
	for i := range keys {
		_, keys[i] = wireGuardTestKeyPair(t, byte(186+i))
	}
	applyWireGuardPeers(t, e.Engine, wg, []wireGuardPeerConfig{{id: "menu", publicKey: keys[0]}})
	e.ApplyStdConstantValue(firstObject, wireGuardObjectPeersJSON(t, []wireGuardPeerConfig{{id: "object-a", publicKey: keys[1]}}))
	e.ApplyStdConstantValue(secondObject, wireGuardObjectPeersJSON(t, []wireGuardPeerConfig{{id: "object-b", publicKey: keys[2]}}))
	applyWireGuardPeers(t, e.Engine, firstConfig, []wireGuardPeerConfig{{
		id: "config-a", publicKey: keys[3], endpoint: "127.0.0.1:51821",
	}})
	applyWireGuardPeers(t, e.Engine, secondConfig, []wireGuardPeerConfig{{
		id: "config-b", publicKey: keys[4], endpoint: "127.0.0.1:51822",
	}})

	e.MustAddTypedLinkByPortName(std.TypeObject, firstObject, "right", "output", wg, "left", "Peers")
	e.MustAddTypedLinkByPortName(std.TypeObject, secondObject, "right", "output", wg, "left", "Peers")
	firstCtrl := e.MustAddTypedLinkByPortName(TypeWGCtrl, wg, "left", "WGCtrl", firstConfig, "right", "WireGuard")
	e.MustAddTypedLinkByPortName(TypeWGCtrl, wg, "left", "WGCtrl", secondConfig, "right", "WireGuard")
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 3, 5)

	// Menu and object rebuilds must not delete either controller contribution.
	applyWireGuardPeers(t, e.Engine, wg, nil)
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 2, 4)
	applyWireGuardPeers(t, e.Engine, wg, []wireGuardPeerConfig{{id: "menu", publicKey: keys[0]}})
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 3, 5)

	e.Engine.RemoveLink(E2EWorkspace, firstCtrl)
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 3, 4)
	firstCtrl = e.MustAddTypedLinkByPortName(TypeWGCtrl, wg, "left", "WGCtrl", firstConfig, "right", "WireGuard")
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 3, 5)

	assertWGConfigState(t, e, firstConfig, "network detached; peer traffic is dropped")
	networkLink := e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", firstConfig, "right", "Network")
	assertWGConfigState(t, e, firstConfig, "up")
	e.Engine.RemoveLink(E2EWorkspace, networkLink)
	assertWGConfigState(t, e, firstConfig, "network detached; peer traffic is dropped")
	networkLink = e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", firstConfig, "right", "Network")
	assertWGConfigState(t, e, firstConfig, "up")
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 3, 5)

	_ = firstCtrl
	_ = networkLink
}

func TestWGConfigSelectorToggleTransfersControllerOwnership(t *testing.T) {
	e := NewNodeE2E(t)
	wg := e.AddNode(NodeTypeWireGuard, "wg")
	first := e.AddNode(NodeTypeWGConfig, "wg-config-first")
	second := e.AddNode(NodeTypeWGConfig, "wg-config-second")
	selector := e.AddNode(std.NodeTypeSelect, "selector")
	state := e.AddNode(std.NodeTypeBoolConstant, "selector-state")
	e.SubscribeMenus(wg, first, second)

	_, firstKey := wireGuardTestKeyPair(t, 192)
	_, secondKeyA := wireGuardTestKeyPair(t, 193)
	_, secondKeyB := wireGuardTestKeyPair(t, 194)
	applyWireGuardPeers(t, e.Engine, first, []wireGuardPeerConfig{{id: "first", publicKey: firstKey}})
	applyWireGuardPeers(t, e.Engine, second, []wireGuardPeerConfig{
		{id: "second-a", publicKey: secondKeyA},
		{id: "second-b", publicKey: secondKeyB},
	})

	e.MustAddTypedLinkByPortName(TypeWGCtrl, wg, "left", "WGCtrl", selector, "right", "Out")
	e.MustAddTypedLinkByPortName(TypeWGCtrl, selector, "left", "In 0", first, "right", "WireGuard")
	e.MustAddTypedLinkByPortName(TypeWGCtrl, selector, "left", "In 1", second, "right", "WireGuard")
	e.MustAddTypedLinkByPortName(std.TypeBool, state, "right", "output", selector, "left", "Selector")
	waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 0, 1)

	for i := 0; i < 3; i++ {
		e.ApplyStdConstantValue(state, true)
		waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 0, 2)
		e.ApplyStdConstantValue(state, false)
		waitWireGuardConfiguredAndRuntimePeerCounts(t, e, wg, 0, 1)
	}
}

func waitWireGuardConfiguredAndRuntimePeerCounts(t *testing.T, e *NodeE2E, node uint64, configured, runtime int) {
	t.Helper()
	configuredText := fmt.Sprint(configured)
	runtimeText := fmt.Sprint(runtime)
	Eventually(t, func() bool {
		configuredItem, configuredOK := e.MenuItem(node, "status", "peers")
		runtimeItem, runtimeOK := e.MenuItem(node, "status", "runtimePeers")
		return configuredOK && runtimeOK && configuredItem.Field != nil && runtimeItem.Field != nil &&
			fmt.Sprint(configuredItem.Value) == configuredText && fmt.Sprint(runtimeItem.Value) == runtimeText
	}, "WireGuard node %d configured peers %d and runtime peers %d", node, configured, runtime)
}

func assertWGConfigState(t *testing.T, e *NodeE2E, node uint64, want string) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "status", "state")
		return ok && item.Field != nil && fmt.Sprint(item.Value) == want
	}, "WGConfig node %d state %q", node, want)
}
