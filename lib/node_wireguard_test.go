package almagest

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
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
	for _, fieldID := range []string{"amneziaJunkCount", "amneziaInitHeader", "amneziaInitiationPacket1"} {
		item, ok = e.MenuItem(node, "config", fieldID)
		if !ok || item.Field == nil {
			t.Fatalf("config %s item = %#v, want field", fieldID, item)
		}
	}
	if item, ok = e.MenuItem(node, "config", "fwmark"); ok {
		t.Fatalf("config fwmark item = %#v, want removed field", item)
	}
	item, ok = e.MenuItem(node, "peers", "peers")
	if !ok || item.Field == nil || item.Kind != formular.FieldArray {
		t.Fatalf("peers item = %#v, want array field", item)
	}
	for _, fieldID := range []string{"amneziaOverride", "amneziaJunkCount", "amneziaInitHeader", "amneziaInitiationPacket1"} {
		if !wireGuardTemplateHasField(item, fieldID) {
			t.Fatalf("peers template missing %q field", fieldID)
		}
	}
	for _, fieldID := range []string{"runtimePeers", "activePeers", "connectedPeers", "rxBytes", "txBytes", "rxPackets", "txPackets", "latestHandshake"} {
		item, ok = e.MenuItem(node, "status", fieldID)
		if !ok || item.Field == nil || !item.Readonly {
			t.Fatalf("status %s item = %#v, want readonly stats field", fieldID, item)
		}
	}
}

func TestWireGuardRuntimeStatsMessageUpdatesStatusRows(t *testing.T) {
	dev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil)
	defer dev.Close()

	n := &wireGuardNode{dev: dev}
	ignoredDev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil)
	defer ignoredDev.Close()

	n.handleRuntimeStats(wireGuardRuntimeStatsMessage{
		dev: ignoredDev,
		stats: wgdevice.RuntimeStats{
			PeerCount:       99,
			ActivePeerCount: 99,
			RxBytes:         99,
			TxBytes:         99,
		},
	})
	if n.runtimeStats.PeerCount != 0 {
		t.Fatalf("runtime stats accepted stale device update: %+v", n.runtimeStats)
	}

	handshake := time.Date(2026, 6, 28, 12, 34, 56, 0, time.UTC)
	n.handleRuntimeStats(wireGuardRuntimeStatsMessage{
		dev: dev,
		stats: wgdevice.RuntimeStats{
			PeerCount:          3,
			ActivePeerCount:    2,
			ConnectedPeerCount: 1,
			RxBytes:            1234,
			TxBytes:            5678,
			RxPackets:          12,
			TxPackets:          34,
			LastHandshakeTime:  handshake,
		},
	})

	block := n.statusBlock()
	assertWireGuardStatusItemValue(t, block, "runtimePeers", 3)
	assertWireGuardStatusItemValue(t, block, "activePeers", 2)
	assertWireGuardStatusItemValue(t, block, "connectedPeers", 1)
	assertWireGuardStatusItemValue(t, block, "rxBytes", "1234")
	assertWireGuardStatusItemValue(t, block, "txBytes", "5678")
	assertWireGuardStatusItemValue(t, block, "rxPackets", "12")
	assertWireGuardStatusItemValue(t, block, "txPackets", "34")
	assertWireGuardStatusItemValue(t, block, "latestHandshake", "2026-06-28T12:34:56Z")
}

func assertWireGuardStatusItemValue(t *testing.T, block formular.Block, id string, want any) {
	t.Helper()
	item, ok := FindItem(block, id)
	if !ok {
		t.Fatalf("status item %q missing", id)
	}
	if item.Field == nil {
		t.Fatalf("status item %q has no field: %#v", id, item)
	}
	if !reflect.DeepEqual(item.Value, want) {
		t.Fatalf("status item %q value = %#v, want %#v", id, item.Value, want)
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
	amneziaWG := wireGuardTestAmneziaConfig()
	peerAmneziaWG := wireGuardTestAmneziaConfig()
	peerAmneziaWG.initHeader = "6111-6113"
	peerAmneziaWG.responseHeader = "6222-6225"
	peerAmneziaWG.initiationPackets[0] = "<b 0xbeef><rc 2>"
	applyWireGuardConfigWithOwnedIPs(t, e.Engine, node, privateKey, 51820, ownedIPs, []wireGuardPeerConfig{{
		id:                          "peer-main",
		publicKey:                   peerPublicKey,
		presharedKey:                "",
		endpoint:                    "127.0.0.1:51821",
		allowedIPs:                  "10.10.20.1/32",
		persistentKeepaliveInterval: 15,
		amneziaWGOverride:           true,
		amneziaWG:                   peerAmneziaWG,
	}})
	applyWireGuardAmneziaConfig(t, e.Engine, node, amneziaWG)

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
	if got, ok := formular.IntValue(gotPort); !ok || got != 51820 {
		t.Fatalf("saved listenPort = %#v, want 51820", gotPort)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "ownedIPs"}); err != nil || got != ownedIPs {
		t.Fatalf("saved ownedIPs = %#v, %v; want %q", got, err, ownedIPs)
	}
	assertSavedWireGuardAmneziaConfig(t, cfg, "wg", amneziaWG)
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
	assertWireGuardPeerField(t, restored, restoredNode, "amneziaOverride", true)
	assertWireGuardPeerField(t, restored, restoredNode, "amneziaInitHeader", peerAmneziaWG.initHeader)
	assertWireGuardPeerField(t, restored, restoredNode, "amneziaInitiationPacket1", peerAmneziaWG.initiationPackets[0])
	assertWireGuardConfigField(t, restored, restoredNode, "ownedIPs", ownedIPs)
	assertWireGuardConfigField(t, restored, restoredNode, "amneziaJunkCount", amneziaWG.junkCount)
	assertWireGuardConfigField(t, restored, restoredNode, "amneziaInitHeader", amneziaWG.initHeader)
	assertWireGuardConfigField(t, restored, restoredNode, "amneziaInitiationPacket1", amneziaWG.initiationPackets[0])

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
	assertWireGuardConfigField(t, e, pastedNode, "amneziaJunkCount", amneziaWG.junkCount)
	assertWireGuardConfigField(t, e, pastedNode, "amneziaInitHeader", amneziaWG.initHeader)
	assertWireGuardConfigField(t, e, pastedNode, "amneziaInitiationPacket1", amneziaWG.initiationPackets[0])
	assertWireGuardPeerField(t, e, pastedNode, "publicKey", peerPublicKey)
	assertWireGuardPeerField(t, e, pastedNode, "amneziaOverride", true)
	assertWireGuardPeerField(t, e, pastedNode, "amneziaInitHeader", peerAmneziaWG.initHeader)
	assertWireGuardPeerField(t, e, pastedNode, "amneziaInitiationPacket1", peerAmneziaWG.initiationPackets[0])
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

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "amneziaInitHeader"},
		Value:       "bad",
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "amneziaInitHeader")
		return ok && item.Status == formular.StatusError
	}, "amnezia header validation error")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "amneziaInitHeader"},
		Value:       "1111-1113",
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "amneziaInitHeader")
		return ok && item.Status == formular.StatusOK
	}, "amnezia header validation ok")

	peerValidationPrivateKey, _ := wireGuardTestKeyPair(t, 40)
	_, peerPublicKey := wireGuardTestKeyPair(t, 41)
	applyWireGuardConfig(t, e.Engine, node, peerValidationPrivateKey, 0, []wireGuardPeerConfig{{
		id:                "peer-amnezia-validation",
		publicKey:         peerPublicKey,
		amneziaWGOverride: true,
		amneziaWG:         wireGuardTestAmneziaConfig(),
	}})
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field: formular.FieldRef{
			BlockID:     "peers",
			FieldID:     "amneziaInitHeader",
			ElementPath: []formular.ElementPathSegment{{ArrayFieldID: "peers", ElementID: "peer-amnezia-validation"}},
		},
		Value: "bad",
	})
	Eventually(t, func() bool {
		return wireGuardPeerFieldStatus(t, e, node, "amneziaInitHeader") == formular.StatusError
	}, "peer amnezia header validation error")
}

func TestWireGuardAppliesPeerAmneziaOverrideToDevice(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 42)
	_, peerPublicKey := wireGuardTestKeyPair(t, 43)
	peerAmneziaWG := wireGuardTestAmneziaConfig()
	peerAmneziaWG.initHeader = "7111-7113"
	peerAmneziaWG.initiationPackets[0] = "<b 0xcafe><rd 2>"
	node := &wireGuardNode{config: wireGuardConfig{
		privateKey: privateKey,
		amneziaWG:  defaultWireGuardAmneziaConfig(),
		peers: []wireGuardPeerConfig{{
			id:                "peer-device-override",
			publicKey:         peerPublicKey,
			amneziaWGOverride: true,
			amneziaWG:         peerAmneziaWG,
		}},
	}}
	node.config.normalize()

	if err := node.applyConfigToDevice(true); err != nil {
		t.Fatalf("applyConfigToDevice: %v", err)
	}
	t.Cleanup(func() { node.dev.Close() })
	publicKey, err := parseWireGuardPublicKey(peerPublicKey)
	if err != nil {
		t.Fatalf("parseWireGuardPublicKey: %v", err)
	}
	cfg, ok := node.dev.PeerConfig(publicKey)
	if !ok {
		t.Fatal("PeerConfig reported missing peer")
	}
	if cfg.AmneziaWG == nil {
		t.Fatal("PeerConfig().AmneziaWG = nil, want override")
	}
	if cfg.AmneziaWG.InitHeader.Spec() != peerAmneziaWG.initHeader || cfg.AmneziaWG.InitiationPackets[0] != peerAmneziaWG.initiationPackets[0] {
		t.Fatalf("PeerConfig().AmneziaWG = %+v, want peer override", *cfg.AmneziaWG)
	}
}

func wireGuardTemplateHasField(item formular.Item, fieldID string) bool {
	if item.Field == nil {
		return false
	}
	for _, template := range item.Templates {
		for _, child := range template.Items {
			if child.ID == fieldID && child.Field != nil {
				return true
			}
		}
	}
	return false
}

func TestWireGuardKeysUseBase64AndAcceptUnpaddedOrLegacyHex(t *testing.T) {
	var privateKey wgdevice.NoisePrivateKey
	for i := range privateKey {
		privateKey[i] = byte(i + 1)
	}
	privateKey[0] &= 248
	privateKey[31] = (privateKey[31] & 127) | 64

	privateKeyBase64 := privateKey.Base64()
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
	presharedKeyBase64 := presharedKey.Base64()
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

func TestWireGuardPeerRoutesNormalizeAndDedupe(t *testing.T) {
	got := wireGuardPeerRoutes([]wireGuardPeerConfig{
		{allowedIPs: "10.40.0.0/24 10.40.0.2"},
		{allowedIPs: "10.40.0.0/24\nfd00:40::/64\n10.40.0.2/32"},
	})
	want := []string{"10.40.0.0/24", "10.40.0.2/32", "fd00:40::/64"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wireGuardPeerRoutes() = %#v, want %#v", got, want)
	}

	if got := wireGuardPeerRoutes([]wireGuardPeerConfig{{allowedIPs: "bad"}}); got != nil {
		t.Fatalf("wireGuardPeerRoutes(invalid) = %#v, want nil", got)
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
		wg.runtimeMu.Lock()
		attached := wg.currentNetwork != nil && wg.currentBind != nil
		wg.runtimeMu.Unlock()
		return attached
	}, "wireguard network attached")
	if got := network.listenCount(); got != 0 {
		t.Fatalf("network listen count after network-only attach = %d, want 0", got)
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

	w.RemoveLink(tunLink)
	listensAfterTunDetach := network.listenCount()
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, wgID),
		BlockID:     "config",
		Values: map[string]any{
			"privateKey": privateKey,
			"listenPort": 51822,
		},
	})
	if got := network.listenCount(); got != listensAfterTunDetach {
		t.Fatalf("network listen count after TUN detach config = %d, want %d", got, listensAfterTunDetach)
	}
}

func TestWireGuardStateDoesNotReportUpBeforeNetworkBindAttachCompletes(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 6)
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })

	n := &wireGuardNode{
		config:     defaultWireGuardConfig(),
		currentTun: sourceTun,
		currentNetwork: &recordingWireGuardTestNetwork{
			Network: gonnect.NewLoopbackNetwok(),
		},
	}
	n.config.privateKey = privateKey
	n.config.listenPort = 51820

	if got := n.stateText(); got == "up" {
		t.Fatalf("stateText() = %q with TUN and network present but no attached bind", got)
	}
}

func TestWireGuardStateDoesNotReportUpWithClosedNetwork(t *testing.T) {
	// Closed network wrappers can still be stored on the node, but they must
	// not satisfy the active-attachment predicate used for "up".
	privateKey, _ := wireGuardTestKeyPair(t, 7)
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })

	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwok(), nil, nil)
	if err := network.Close(); err != nil {
		t.Fatalf("network.Close() error = %v, want nil", err)
	}
	n := &wireGuardNode{
		config:                   defaultWireGuardConfig(),
		currentTun:               sourceTun,
		currentTunGeneration:     1,
		currentNetwork:           network,
		currentNetworkGeneration: 1,
		currentBind:              wireGuardTrackedBind{},
		runtimeReadyGeneration:   1,
	}
	n.config.privateKey = privateKey
	n.config.listenPort = 51820
	n.tunGeneration.Store(1)
	n.networkGeneration.Store(1)
	n.runtimeGeneration.Store(1)

	if got := n.stateText(); got == "up" {
		t.Fatalf("stateText() = %q with closed network, want not up", got)
	}
}

func TestWireGuardSameSourceNetworkReemitRebuildsFailedBind(t *testing.T) {
	// Same-source re-emits should preserve wrapper identity only for healthy
	// binds; a failed bind needs to be rebuilt before config is retried.
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })

	base := gonnect.NewLoopbackNetwok()
	current := gonnect.DetachNetwork(base, nil, nil)
	t.Cleanup(func() { _ = current.Close() })
	duplicate := gonnect.DetachNetwork(base, nil, nil)

	n := &wireGuardNode{
		config:                   defaultWireGuardConfig(),
		currentTun:               sourceTun,
		currentTunGeneration:     1,
		currentNetwork:           current,
		currentNetworkGeneration: 1,
		currentBind:              wireGuardTrackedBind{},
		currentBindFailed:        true,
	}
	n.tunGeneration.Store(1)
	n.networkGeneration.Store(2)

	needsConfig, ok, retry, err := n.attachNetworkPayload(2, duplicate)
	if err != nil {
		t.Fatalf("attachNetworkPayload() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("attachNetworkPayload() ok = false, want true")
	}
	if retry {
		t.Fatal("attachNetworkPayload() retry = true, want false")
	}
	if !needsConfig {
		t.Fatal("attachNetworkPayload() needsConfig = false, want true")
	}
	if n.currentBindFailed {
		t.Fatal("currentBindFailed = true after same-source reemit, want false")
	}
	if n.currentBindGen == 0 {
		t.Fatal("currentBindGen = 0, want rebuilt bind generation")
	}
	if NetworkIsReady(duplicate) {
		t.Fatal("duplicate same-source network was not closed")
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
		return ok && snapshot.Label == "up" && network.listenCount() >= 3
	}, "wireguard retry after transient listen failure")
}

func TestWireGuardTrackedBindClosedReceiveErrorIsRecoverable(t *testing.T) {
	var err error = wireGuardTrackedBindReceiveError{
		generation:     1,
		openGeneration: 1,
		err:            net.ErrClosed,
	}
	if errors.Is(err, net.ErrClosed) {
		t.Fatal("tracked unexpected close matched net.ErrClosed and would be hidden by wgo")
	}
	if !wgdevice.ReceiveErrorShouldRecover(err) {
		t.Fatal("tracked unexpected close is not recoverable")
	}
	var tracked wireGuardTrackedBindReceiveError
	if !errors.As(err, &tracked) {
		t.Fatal("tracked unexpected close cannot be recovered with errors.As")
	}
	if !errors.Is(tracked.err, net.ErrClosed) {
		t.Fatalf("tracked underlying err = %v, want net.ErrClosed", tracked.err)
	}
}

func TestWireGuardIntentionalClosedReceiveErrorIsIgnored(t *testing.T) {
	wg := &wireGuardNode{}
	dev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil)
	t.Cleanup(dev.Close)
	wg.dev = dev

	wg.handleReceiveError(wireGuardReceiveErrorMessage{
		dev:              dev,
		intentionalClose: true,
		err:              net.ErrClosed,
	})

	if wg.runtimeError != "" {
		t.Fatalf("runtimeError = %q, want empty", wg.runtimeError)
	}
	if wg.currentBindFailed {
		t.Fatal("currentBindFailed = true, want false")
	}
}

func TestWireGuardRetriesBindAfterReceiveEOF(t *testing.T) {
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
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	Eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "up" && network.listenCount() >= 1
	}, "wireguard initial listen")

	injectWireGuardReceiveEOF(t, wg)

	Eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "up" && network.listenCount() >= 2
	}, "wireguard retry after receive EOF")
}

func TestWireGuardReceiveEOFRebuildsBindBeforePublishingUp(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(4096))
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

	labels := make(chan string, 32)
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind != pasta.NotificationNodeUpdated || notification.ID != wgID || notification.Node == nil {
			return
		}
		select {
		case labels <- notification.Node.Label:
		default:
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { w.UnsubscribeNotifications(sub) })

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
		return ok && snapshot.Label == "up" && network.listenCount() >= 1
	}, "wireguard initial up")
	drainWireGuardTestLabels(labels)

	injectWireGuardReceiveEOF(t, wg)

	waitWireGuardTestLabel(t, labels, "error")
	listensAfterError := network.listenCount()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case label := <-labels:
			if label == "up" {
				if got := network.listenCount(); got <= listensAfterError {
					t.Fatalf("WireGuard label returned to up before rebuilding bind; listen count after first error=%d now=%d", listensAfterError, got)
				}
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for WireGuard to recover after receive EOF; listen count after first error=%d now=%d", listensAfterError, network.listenCount())
		}
	}
}

func injectWireGuardReceiveEOF(t *testing.T, wg *wireGuardNode) {
	t.Helper()
	var dev *wgdevice.Device
	var bindGeneration uint64
	var bindOpenGeneration uint64
	Eventually(t, func() bool {
		wg.runtimeMu.Lock()
		defer wg.runtimeMu.Unlock()
		if wg.dev == nil || wg.currentBind == nil || wg.currentBindOpen == nil || wg.currentBindFailed {
			return false
		}
		openGeneration := wg.currentBindOpen.Load()
		if openGeneration == 0 {
			return false
		}
		dev = wg.dev
		bindGeneration = wg.currentBindGen
		bindOpenGeneration = openGeneration
		return true
	}, "wireguard bind open before receive EOF injection")
	wg.notifyReceiveError(dev, wireGuardTrackedBindReceiveError{
		generation:     bindGeneration,
		openGeneration: bindOpenGeneration,
		err:            io.EOF,
	})
}

func drainWireGuardTestLabels(labels <-chan string) {
	for {
		select {
		case <-labels:
		default:
			return
		}
	}
}

func waitWireGuardTestLabel(t *testing.T, labels <-chan string, want string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-labels:
			if got == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for WireGuard label %q", want)
		}
	}
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

func applyWireGuardAmneziaConfig(t *testing.T, engine Engine, node uint64, config wireGuardAmneziaConfig) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values:      wireGuardAmneziaFormValues(config),
	})
}

func wireGuardAmneziaFormValues(config wireGuardAmneziaConfig) map[string]any {
	return map[string]any{
		"amneziaJunkCount":         config.junkCount,
		"amneziaJunkMin":           config.junkMin,
		"amneziaJunkMax":           config.junkMax,
		"amneziaInitPadding":       config.initPadding,
		"amneziaResponsePadding":   config.responsePadding,
		"amneziaCookiePadding":     config.cookiePadding,
		"amneziaTransportPadding":  config.transportPadding,
		"amneziaInitHeader":        config.initHeader,
		"amneziaResponseHeader":    config.responseHeader,
		"amneziaCookieHeader":      config.cookieHeader,
		"amneziaTransportHeader":   config.transportHeader,
		"amneziaInitiationPacket1": config.initiationPackets[0],
		"amneziaInitiationPacket2": config.initiationPackets[1],
		"amneziaInitiationPacket3": config.initiationPackets[2],
		"amneziaInitiationPacket4": config.initiationPackets[3],
		"amneziaInitiationPacket5": config.initiationPackets[4],
	}
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
				"amneziaOverride":             peer.amneziaWGOverride,
				"amneziaJunkCount":            peer.amneziaWG.junkCount,
				"amneziaJunkMin":              peer.amneziaWG.junkMin,
				"amneziaJunkMax":              peer.amneziaWG.junkMax,
				"amneziaInitPadding":          peer.amneziaWG.initPadding,
				"amneziaResponsePadding":      peer.amneziaWG.responsePadding,
				"amneziaCookiePadding":        peer.amneziaWG.cookiePadding,
				"amneziaTransportPadding":     peer.amneziaWG.transportPadding,
				"amneziaInitHeader":           peer.amneziaWG.initHeader,
				"amneziaResponseHeader":       peer.amneziaWG.responseHeader,
				"amneziaCookieHeader":         peer.amneziaWG.cookieHeader,
				"amneziaTransportHeader":      peer.amneziaWG.transportHeader,
				"amneziaInitiationPacket1":    peer.amneziaWG.initiationPackets[0],
				"amneziaInitiationPacket2":    peer.amneziaWG.initiationPackets[1],
				"amneziaInitiationPacket3":    peer.amneziaWG.initiationPackets[2],
				"amneziaInitiationPacket4":    peer.amneziaWG.initiationPackets[3],
				"amneziaInitiationPacket5":    peer.amneziaWG.initiationPackets[4],
			},
		})
	}
	return out
}

func wireGuardTestAmneziaConfig() wireGuardAmneziaConfig {
	config := defaultWireGuardAmneziaConfig()
	config.junkCount = 2
	config.junkMin = 11
	config.junkMax = 23
	config.initPadding = 7
	config.responsePadding = 8
	config.cookiePadding = 9
	config.transportPadding = 10
	config.initHeader = "1111-1113"
	config.responseHeader = "2222-2225"
	config.cookieHeader = "3333"
	config.transportHeader = "4444-4449"
	config.initiationPackets[0] = "<b 0xaa55><rc 3><rd 2><t>"
	config.initiationPackets[1] = "<r 5>"
	config.initiationPackets[2] = "<rd 4>"
	config.initiationPackets[3] = "<rc 6>"
	config.initiationPackets[4] = "<b 0x01020304>"
	return config
}

func assertSavedWireGuardAmneziaConfig(t *testing.T, cfg configer.Config, nodeName string, want wireGuardAmneziaConfig) {
	t.Helper()
	raw, err := cfg.Get(configer.Path{"nodes", nodeName, "amneziaWG"})
	if err != nil {
		t.Fatalf("saved amneziaWG missing: %v", err)
	}
	got, ok := configer.MapStringAny(raw)
	if !ok {
		t.Fatalf("saved amneziaWG = %#v, want map", raw)
	}
	for key, wantValue := range map[string]int{
		"junkCount":        want.junkCount,
		"junkMin":          want.junkMin,
		"junkMax":          want.junkMax,
		"initPadding":      want.initPadding,
		"responsePadding":  want.responsePadding,
		"cookiePadding":    want.cookiePadding,
		"transportPadding": want.transportPadding,
	} {
		gotValue, ok := formular.IntValue(got[key])
		if !ok || gotValue != wantValue {
			t.Fatalf("saved amneziaWG.%s = %#v, want %#v", key, got[key], wantValue)
		}
	}
	for key, wantValue := range map[string]string{
		"initHeader":      want.initHeader,
		"responseHeader":  want.responseHeader,
		"cookieHeader":    want.cookieHeader,
		"transportHeader": want.transportHeader,
	} {
		if !reflect.DeepEqual(got[key], wantValue) {
			t.Fatalf("saved amneziaWG.%s = %#v, want %#v", key, got[key], wantValue)
		}
	}
	packets, ok := wireGuardAmneziaInitiationPacketsFromAny(got["initiationPackets"])
	if !ok || packets != want.initiationPackets {
		t.Fatalf("saved amneziaWG.initiationPackets = %#v, want %#v", got["initiationPackets"], want.initiationPackets)
	}
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

func wireGuardPeerFieldStatus(t *testing.T, e *NodeE2E, node uint64, fieldID string) string {
	t.Helper()
	item, ok := e.MenuItem(node, "peers", "peers")
	if !ok || item.Field == nil || len(item.Elements) == 0 {
		return ""
	}
	for _, child := range item.Elements[0].Items {
		if child.ID == fieldID {
			return child.Status
		}
	}
	return ""
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
		value, ok := formular.StringValue(item.Value)
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
		value, ok := formular.StringValue(item.Value)
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
	return privateKey.Base64(), publicKeyBase64
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
