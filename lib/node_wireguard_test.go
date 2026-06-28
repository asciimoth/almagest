package almagest

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	batchudp "github.com/asciimoth/batchudp"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	wgdevice "github.com/asciimoth/wgo/device"
)

func TestWireGuardClassPortsAndMenuBlocks(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if len(snapshot.LeftPorts) != 1 || len(snapshot.RightPorts) != 1 {
		t.Fatalf("ports = left %d right %d, want one of each", len(snapshot.LeftPorts), len(snapshot.RightPorts))
	}
	assertPortShape(t, e, snapshot.LeftPorts[0], "left", "Tun", TypeTun)
	assertPortShape(t, e, snapshot.RightPorts[0], "right", "Network", TypeNetwork)

	for _, blockID := range []string{"status", "config", "peers"} {
		if _, ok := e.MenuBlock(node, blockID); !ok {
			t.Fatalf("menu block %q missing", blockID)
		}
	}
	item, ok := e.MenuItem(node, "status", "publicKey")
	if !ok || item.Field == nil || !item.Readonly {
		t.Fatalf("status publicKey item = %#v, want readonly field", item)
	}
	item, ok = e.MenuItem(node, "config", "privateKey")
	if !ok || item.Field == nil || !item.Secret {
		t.Fatalf("config privateKey item = %#v, want secret field", item)
	}
	item, ok = e.MenuItem(node, "config", "ownedIPs")
	if !ok || item.Field == nil || !item.Multiline {
		t.Fatalf("config ownedIPs item = %#v, want multiline field", item)
	}
	if item, ok = e.MenuItem(node, "config", "fwmark"); ok {
		t.Fatalf("config fwmark item = %#v, want removed field", item)
	}
	item, ok = e.MenuItem(node, "peers", "peers")
	if !ok || item.Field == nil || item.Kind != formular.FieldArray {
		t.Fatalf("peers item = %#v, want array field", item)
	}
	if item, ok = e.MenuItem(node, "status", "activePeers"); ok {
		t.Fatalf("status activePeers item = %#v, want removed stale runtime field", item)
	}
	if item, ok = e.MenuItem(node, "status", "latestHandshake"); ok {
		t.Fatalf("status latestHandshake item = %#v, want removed stale runtime field", item)
	}
}

func TestWireGuardClassLongDescriptionDocumentsPlainSocksLimit(t *testing.T) {
	desc := wireGuardClass{}.LongDescription()
	for _, want := range []string{
		"standard SOCKS5 UDP ASSOCIATE",
		"does not let the client request the relay listen port",
		"?gost",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("WireGuard long description missing %q: %s", want, desc)
		}
	}
}

func TestWireGuardAutoGeneratesPrivateKey(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)

	freshKey := waitWireGuardPrivateKey(t, e, node)
	assertWireGuardPublicKeyPresent(t, e, node)
	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig fresh node: %v", err)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "privateKey"}); err != nil || got != freshKey {
		t.Fatalf("saved fresh privateKey = %#v, %v; want %q", got, err, freshKey)
	}

	for _, tc := range []struct {
		name     string
		nodeData map[string]any
	}{
		{name: "missing", nodeData: map[string]any{"Class": NodeTypeWireGuard}},
		{name: "empty", nodeData: map[string]any{"Class": NodeTypeWireGuard, "privateKey": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restored := newNodeE2EWithConfig(t, configer.NewMemory(map[string]any{
				"nodes": map[string]any{"wg": tc.nodeData},
			}))
			restoredNode, ok := restored.Engine.NodeByName(E2EWorkspace, "wg")
			if !ok {
				t.Fatal("restored node wg missing")
			}
			restored.SubscribeMenus(restoredNode)
			restoredKey := waitWireGuardPrivateKey(t, restored, restoredNode)
			assertWireGuardPublicKeyPresent(t, restored, restoredNode)

			restoredCfg := configer.NewMemory(nil)
			if err := restored.Engine.SaveConfig(restoredCfg); err != nil {
				t.Fatalf("SaveConfig restored node: %v", err)
			}
			if got, err := restoredCfg.Get(configer.Path{"nodes", "wg", "privateKey"}); err != nil || got != restoredKey {
				t.Fatalf("saved restored privateKey = %#v, %v; want %q", got, err, restoredKey)
			}
		})
	}
}

func TestWireGuardConfigPersistsAcrossSaveRestoreAndCopyPaste(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)
	privateKey, publicKey := wireGuardTestKeyPair(t, 1)
	_, peerPublicKey := wireGuardTestKeyPair(t, 2)
	ownedIPs := "10.10.10.2/32\nfd00:10:10::2/128"
	applyWireGuardConfigWithOwnedIPs(t, e.Engine, node, privateKey, 51820, ownedIPs, []wireGuardPeerConfig{{
		id:                          "peer-main",
		publicKey:                   peerPublicKey,
		presharedKey:                "",
		endpoint:                    "127.0.0.1:51821",
		allowedIPs:                  "10.10.20.1/32",
		persistentKeepaliveInterval: 15,
	}})

	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "status", "publicKey")
		return ok && item.Value == publicKey
	}, "wireguard public key status")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "privateKey"}); err != nil || got != privateKey {
		t.Fatalf("saved privateKey = %#v, %v; want %q", got, err, privateKey)
	}
	gotPort, err := cfg.Get(configer.Path{"nodes", "wg", "listenPort"})
	if err != nil {
		t.Fatalf("saved listenPort missing: %v", err)
	}
	if got, ok := intFromAny(gotPort); !ok || got != 51820 {
		t.Fatalf("saved listenPort = %#v, want 51820", gotPort)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "ownedIPs"}); err != nil || got != ownedIPs {
		t.Fatalf("saved ownedIPs = %#v, %v; want %q", got, err, ownedIPs)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "fwmark"}); err == nil {
		t.Fatalf("saved fwmark = %#v, want missing removed field", got)
	}

	restored := newNodeE2EWithConfig(t, cfg)
	restoredNode, ok := restored.Engine.NodeByName(E2EWorkspace, "wg")
	if !ok {
		t.Fatal("restored node wg missing")
	}
	restored.SubscribeMenus(restoredNode)
	Eventually(t, func() bool {
		item, ok := restored.MenuItem(restoredNode, "status", "publicKey")
		return ok && item.Value == publicKey
	}, "restored wireguard public key status")
	assertWireGuardPeerField(t, restored, restoredNode, "publicKey", peerPublicKey)
	assertWireGuardPeerField(t, restored, restoredNode, "endpoint", "127.0.0.1:51821")
	assertWireGuardPeerField(t, restored, restoredNode, "allowedIPs", "10.10.20.1/32")
	assertWireGuardConfigField(t, restored, restoredNode, "ownedIPs", ownedIPs)

	clip := e.Engine.Copy(E2EWorkspace, []uint64{node})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 1 {
		t.Fatalf("Paste nodes = %#v, want one node", pasted)
	}
	pastedNode := pasted[0]
	e.SubscribeMenus(pastedNode)
	Eventually(t, func() bool {
		item, ok := e.MenuItem(pastedNode, "status", "publicKey")
		return ok && item.Value == publicKey
	}, "pasted wireguard public key status")
	assertWireGuardConfigField(t, e, pastedNode, "ownedIPs", ownedIPs)
	assertWireGuardPeerField(t, e, pastedNode, "publicKey", peerPublicKey)
}

func TestWireGuardValidationReportsStatus(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "privateKey"},
		Value:       "bad",
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKey")
		return ok && item.Status == formular.StatusError
	}, "private key validation error")

	privateKey, _ := wireGuardTestKeyPair(t, 3)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "privateKey"},
		Value:       privateKey,
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKey")
		return ok && item.Status == formular.StatusOK
	}, "private key validation ok")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "ownedIPs"},
		Value:       "not-an-ip",
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "ownedIPs")
		return ok && item.Status == formular.StatusError
	}, "owned IPs validation error")
}

func TestWireGuardKeysUseBase64AndAcceptUnpaddedOrLegacyHex(t *testing.T) {
	var privateKey wgdevice.NoisePrivateKey
	for i := range privateKey {
		privateKey[i] = byte(i + 1)
	}
	privateKey[0] &= 248
	privateKey[31] = (privateKey[31] & 127) | 64

	privateKeyBase64 := encodeWireGuardKey(privateKey[:])
	if len(privateKeyBase64) != 44 || !strings.HasSuffix(privateKeyBase64, "=") {
		t.Fatalf("private key base64 = %q, want 44 chars with trailing padding", privateKeyBase64)
	}
	privateKeyUnpadded := strings.TrimSuffix(privateKeyBase64, "=")
	if len(privateKeyUnpadded) != 43 {
		t.Fatalf("unpadded private key length = %d, want 43", len(privateKeyUnpadded))
	}
	for _, value := range []string{privateKeyBase64, privateKeyUnpadded, hex.EncodeToString(privateKey[:])} {
		got, err := parseWireGuardPrivateKey(value)
		if err != nil {
			t.Fatalf("parseWireGuardPrivateKey(%q): %v", value, err)
		}
		if !got.Equals(privateKey) {
			t.Fatalf("parseWireGuardPrivateKey(%q) = %x, want %x", value, got, privateKey)
		}
		if normalized := normalizeWireGuardKey(value); normalized != privateKeyBase64 {
			t.Fatalf("normalizeWireGuardKey(%q) = %q, want %q", value, normalized, privateKeyBase64)
		}
	}

	publicKeyBase64 := wireGuardPublicKeyBase64(privateKey)
	publicKeyUnpadded := strings.TrimSuffix(publicKeyBase64, "=")
	publicKey, err := parseWireGuardPublicKey(publicKeyBase64)
	if err != nil {
		t.Fatalf("parseWireGuardPublicKey(padded): %v", err)
	}
	if got, err := parseWireGuardPublicKey(publicKeyUnpadded); err != nil || !got.Equals(publicKey) {
		t.Fatalf("parseWireGuardPublicKey(unpadded) = %x, %v; want %x", got, err, publicKey)
	}
	if normalized := normalizeWireGuardKey(publicKeyUnpadded); normalized != publicKeyBase64 {
		t.Fatalf("normalizeWireGuardKey(public unpadded) = %q, want %q", normalized, publicKeyBase64)
	}

	var presharedKey wgdevice.NoisePresharedKey
	for i := range presharedKey {
		presharedKey[i] = byte(255 - i)
	}
	presharedKeyBase64 := encodeWireGuardKey(presharedKey[:])
	presharedKeyUnpadded := strings.TrimSuffix(presharedKeyBase64, "=")
	if got, err := parseWireGuardPresharedKey(presharedKeyUnpadded); err != nil || got != presharedKey {
		t.Fatalf("parseWireGuardPresharedKey(unpadded) = %x, %v; want %x", got, err, presharedKey)
	}
}

func TestWireGuardOwnedIPAddrsNormalizeAndDedupe(t *testing.T) {
	got := wireGuardOwnedIPAddrs("10.30.0.2 10.30.0.2/32\n10.30.0.0/24\nfd00:30::2")
	want := []string{"10.30.0.2/32", "10.30.0.0/24", "fd00:30::2/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wireGuardOwnedIPAddrs() = %#v, want %#v", got, want)
	}

	if got := wireGuardOwnedIPAddrs("bad"); got != nil {
		t.Fatalf("wireGuardOwnedIPAddrs(invalid) = %#v, want nil", got)
	}
}

func TestWireGuardNetworkAttachmentDoesNotOpenSocketsUntilTunAttached(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwok()}
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(testTunProviderClass{registry: newTestTunProviderRegistry()}); err != nil {
		t.Fatalf("AddNodeClass TunProvider: %v", err)
	}

	wgID, err := w.AddNodeByClass("test/WireGuard", "wg")
	if err != nil {
		t.Fatalf("AddNodeByClass WireGuard: %v", err)
	}
	networkID, err := w.AddNodeByClass("test/WireGuardNetworkSource", "network")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkSource: %v", err)
	}
	tunID, err := w.AddNodeByClass(testTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := mustNodeSnapshot(t, w, wgID)
	networkSnapshot := mustNodeSnapshot(t, w, networkID)
	tunSnapshot := mustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	Eventually(t, func() bool {
		return wg.currentNetwork != nil && wg.currentBind != nil
	}, "wireguard network attached")
	if got := network.listenCount(); got != 0 {
		t.Fatalf("network listen count after network-only attach = %d, want 0", got)
	}
	if _, ok := wg.currentBind.(*batchudp.StdNetBind); ok {
		t.Fatal("WireGuard currentBind is concrete *batchudp.StdNetBind; want port abstraction wrapper")
	}

	privateKey, _ := wireGuardTestKeyPair(t, 4)
	_, peerPublicKey := wireGuardTestKeyPair(t, 5)
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, wgID),
		BlockID:     "config",
		Values: map[string]any{
			"privateKey": privateKey,
			"listenPort": 51820,
		},
	})
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, wgID),
		BlockID:     "peers",
		Values: map[string]any{
			"peers": wireGuardPeerValues([]wireGuardPeerConfig{{
				id:         "peer",
				publicKey:  peerPublicKey,
				endpoint:   "127.0.0.1:51821",
				allowedIPs: "10.10.20.1/32",
			}}),
		},
	})
	if got := network.listenCount(); got != 0 {
		t.Fatalf("network listen count after config with no TUN = %d, want 0", got)
	}

	tunLink, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	Eventually(t, func() bool {
		return network.listenCount() > 0
	}, "wireguard opens network after TUN attaches")

	listensWithBothLinks := network.listenCount()
	w.RemoveLink(tunLink)
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, wgID),
		BlockID:     "config",
		Values: map[string]any{
			"privateKey": privateKey,
			"listenPort": 51822,
		},
	})
	if got := network.listenCount(); got != listensWithBothLinks {
		t.Fatalf("network listen count after TUN detach config = %d, want %d", got, listensWithBothLinks)
	}
}

func TestWireGuardRetriesDeviceBringUpAfterNetworkListenFailure(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwok()}
	network.failNextListens(2, errors.New("temporary listen failure"))
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(testTunProviderClass{registry: newTestTunProviderRegistry()}); err != nil {
		t.Fatalf("AddNodeClass TunProvider: %v", err)
	}

	wgID, err := w.AddNodeByClass("test/WireGuard", "wg")
	if err != nil {
		t.Fatalf("AddNodeByClass WireGuard: %v", err)
	}
	networkID, err := w.AddNodeByClass("test/WireGuardNetworkSource", "network")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkSource: %v", err)
	}
	tunID, err := w.AddNodeByClass(testTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := mustNodeSnapshot(t, w, wgID)
	networkSnapshot := mustNodeSnapshot(t, w, networkID)
	tunSnapshot := mustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	Eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "error" && network.listenCount() >= 1
	}, "wireguard initial listen failure")

	Eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "up" && wg.runtimeError == "" && network.listenCount() >= 3
	}, "wireguard retry after transient listen failure")
}

func TestWireGuardRetriesBindAfterReceiveEOF(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{
		Network: &readEOFWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwok()},
	}
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(testTunProviderClass{registry: newTestTunProviderRegistry()}); err != nil {
		t.Fatalf("AddNodeClass TunProvider: %v", err)
	}

	wgID, err := w.AddNodeByClass("test/WireGuard", "wg")
	if err != nil {
		t.Fatalf("AddNodeByClass WireGuard: %v", err)
	}
	networkID, err := w.AddNodeByClass("test/WireGuardNetworkSource", "network")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkSource: %v", err)
	}
	tunID, err := w.AddNodeByClass(testTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := mustNodeSnapshot(t, w, wgID)
	networkSnapshot := mustNodeSnapshot(t, w, networkID)
	tunSnapshot := mustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	Eventually(t, func() bool {
		return network.listenCount() >= 1
	}, "wireguard initial listen")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if network.listenCount() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WireGuard did not retry bind after receive EOF; listen count = %d, want at least 2", network.listenCount())
}

func applyWireGuardConfig(t *testing.T, engine Engine, node uint64, privateKey string, listenPort int, peers []wireGuardPeerConfig) {
	t.Helper()
	applyWireGuardConfigWithOwnedIPs(t, engine, node, privateKey, listenPort, "", peers)
}

func applyWireGuardConfigWithOwnedIPs(t *testing.T, engine Engine, node uint64, privateKey string, listenPort int, ownedIPs string, peers []wireGuardPeerConfig) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"privateKey": privateKey,
			"listenPort": listenPort,
			"ownedIPs":   ownedIPs,
		},
	})
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "peers",
		Values: map[string]any{
			"peers": wireGuardPeerValues(peers),
		},
	})
}

func wireGuardPeerValues(peers []wireGuardPeerConfig) []formular.ArrayElementValue {
	out := make([]formular.ArrayElementValue, 0, len(peers))
	for i, peer := range peers {
		peer.normalize(i)
		out = append(out, formular.ArrayElementValue{
			ID:       peer.id,
			Template: "peer",
			Values: map[string]any{
				"publicKey":                   peer.publicKey,
				"presharedKey":                peer.presharedKey,
				"endpoint":                    peer.endpoint,
				"allowedIPs":                  peer.allowedIPs,
				"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
			},
		})
	}
	return out
}

func assertWireGuardPeerField(t *testing.T, e *NodeE2E, node uint64, fieldID string, want any) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "peers", "peers")
		if !ok || item.Field == nil || len(item.Elements) == 0 {
			return false
		}
		for _, child := range item.Elements[0].Items {
			if child.ID == fieldID {
				return reflect.DeepEqual(child.Value, want)
			}
		}
		return false
	}, "wireguard peer field %s = %#v", fieldID, want)
}

func assertWireGuardConfigField(t *testing.T, e *NodeE2E, node uint64, fieldID string, want any) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", fieldID)
		return ok && reflect.DeepEqual(item.Value, want)
	}, "wireguard config field %s = %#v", fieldID, want)
}

func waitWireGuardPrivateKey(t *testing.T, e *NodeE2E, node uint64) string {
	t.Helper()
	var got string
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKey")
		if !ok || item.Field == nil {
			return false
		}
		value, ok := stringFromAny(item.Value)
		if !ok || value == "" {
			return false
		}
		key, err := parseWireGuardPrivateKey(value)
		if err != nil || key.IsZero() {
			return false
		}
		got = value
		return true
	}, "wireguard generated private key")
	return got
}

func assertWireGuardPublicKeyPresent(t *testing.T, e *NodeE2E, node uint64) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "status", "publicKey")
		if !ok || item.Field == nil {
			return false
		}
		value, ok := stringFromAny(item.Value)
		if !ok || value == "" {
			return false
		}
		_, err := parseWireGuardPublicKey(value)
		return err == nil
	}, "wireguard generated public key")
}

func wireGuardTestKeyPair(t *testing.T, seed byte) (privateKeyBase64, publicKeyBase64 string) {
	t.Helper()
	var privateKey wgdevice.NoisePrivateKey
	for i := range privateKey {
		privateKey[i] = seed + byte(i) + 1
	}
	privateKey[0] &= 248
	privateKey[31] = (privateKey[31] & 127) | 64
	publicKeyBase64 = wireGuardPublicKeyBase64(privateKey)
	if publicKeyBase64 == "" {
		t.Fatal("wireGuardPublicKeyBase64 returned empty key")
	}
	return encodeWireGuardKey(privateKey[:]), publicKeyBase64
}

type captureWireGuardClass struct {
	node **wireGuardNode
}

func (captureWireGuardClass) ClassName() string        { return "test/WireGuard" }
func (captureWireGuardClass) ShortDescription() string { return wireGuardClass{}.ShortDescription() }
func (captureWireGuardClass) LongDescription() string  { return wireGuardClass{}.LongDescription() }
func (captureWireGuardClass) DefaultNodeParams() pasta.NodeClassParams {
	return wireGuardClass{}.DefaultNodeParams()
}
func (c captureWireGuardClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultWireGuardConfig()
	config.read(cfg)
	if err := config.ensurePrivateKey(); err != nil {
		return nil, err
	}
	node := &wireGuardNode{config: config}
	if c.node != nil {
		*c.node = node
	}
	return node, nil
}

type recordingWireGuardTestNetwork struct {
	gonnect.Network
	mu          sync.Mutex
	listenCalls int
	failListens int
	listenErr   error
}

type readEOFWireGuardTestNetwork struct {
	gonnect.Network
}

func (n *readEOFWireGuardTestNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	conn, err := n.Network.ListenUDP(ctx, network, laddr)
	if err != nil {
		return nil, err
	}
	return readEOFWireGuardTestUDPConn{UDPConn: conn}, nil
}

func (n *readEOFWireGuardTestNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, laddr string) (gonnect.UDPConn, error) {
	conn, err := n.Network.ListenUDPConfig(ctx, lc, network, laddr)
	if err != nil {
		return nil, err
	}
	return readEOFWireGuardTestUDPConn{UDPConn: conn}, nil
}

type readEOFWireGuardTestUDPConn struct {
	gonnect.UDPConn
}

func (c readEOFWireGuardTestUDPConn) ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, netip.AddrPort{}, io.EOF
}

func (n *recordingWireGuardTestNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	n.recordListen()
	if err := n.consumeListenFailure(); err != nil {
		return nil, err
	}
	return n.Network.ListenUDP(ctx, network, laddr)
}

func (n *recordingWireGuardTestNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, laddr string) (gonnect.UDPConn, error) {
	n.recordListen()
	if err := n.consumeListenFailure(); err != nil {
		return nil, err
	}
	return n.Network.ListenUDPConfig(ctx, lc, network, laddr)
}

func (n *recordingWireGuardTestNetwork) Close() error { return nil }

func (n *recordingWireGuardTestNetwork) recordListen() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.listenCalls++
}

func (n *recordingWireGuardTestNetwork) listenCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.listenCalls
}

func (n *recordingWireGuardTestNetwork) failNextListens(count int, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failListens = count
	n.listenErr = err
}

func (n *recordingWireGuardTestNetwork) consumeListenFailure() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failListens <= 0 {
		return nil
	}
	n.failListens--
	return n.listenErr
}

type wireGuardTestNetworkSourceClass struct {
	network NetworkCloser
}

func (wireGuardTestNetworkSourceClass) ClassName() string {
	return "test/WireGuardNetworkSource"
}
func (wireGuardTestNetworkSourceClass) ShortDescription() string {
	return "test WireGuard network source"
}
func (wireGuardTestNetworkSourceClass) LongDescription() string {
	return "Test-only left-directed almagest/network source."
}
func (wireGuardTestNetworkSourceClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (c wireGuardTestNetworkSourceClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &wireGuardTestNetworkSourceNode{network: c.network}, nil
}

type wireGuardTestNetworkSourceNode struct {
	pasta.BasicNode

	w       *pasta.Workspace
	id      uint64
	out     uint64
	network NetworkCloser
}

func (n *wireGuardTestNetworkSourceNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.out = restored.LeftPorts[0]
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	return n.w.SetNodeLabel(n.id, "ready")
}

func (n *wireGuardTestNetworkSourceNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *wireGuardTestNetworkSourceNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *wireGuardTestNetworkSourceNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(link)
	}
	return nil
}

func (n *wireGuardTestNetworkSourceNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(event.Link)
	}
	return nil
}

func (n *wireGuardTestNetworkSourceNode) sendAll() {
	snapshot, ok := n.w.PortSnapshot(n.out)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		n.sendToLink(link)
	}
}

func (n *wireGuardTestNetworkSourceNode) sendToLink(link uint64) {
	if n.network == nil {
		return
	}
	BindNetworkResource(n.w, n.id, link, n.network)
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: n.network})
}
