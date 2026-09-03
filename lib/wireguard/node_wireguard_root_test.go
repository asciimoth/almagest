package wireguardnodes_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
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
	if len(snapshot.LeftPorts) != 3 || len(snapshot.RightPorts) != 1 {
		t.Fatalf("ports = left %d right %d, want three left and one right", len(snapshot.LeftPorts), len(snapshot.RightPorts))
	}
	assertPortShape(t, e, snapshot.LeftPorts[0], "left", "Tun", TypeTun)
	assertPortShape(t, e, snapshot.LeftPorts[1], "left", "Peers", "pasta/object")
	assertPortShape(t, e, snapshot.LeftPorts[2], "left", "WGCtrl", TypeWGCtrl)
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
	item, ok = e.MenuItem(node, "config", "privateKeySecret")
	if !ok || item.Field == nil || item.Secret || item.Help == "" {
		t.Fatalf("config privateKeySecret item = %#v, want documented non-secret field", item)
	}
	item, ok = e.MenuItem(node, "config", "ownedIPs")
	if !ok || item.Field == nil || !item.Multiline {
		t.Fatalf("config ownedIPs item = %#v, want multiline field", item)
	}
	item, ok = e.MenuItem(node, "config", "dns")
	if !ok || item.Field == nil || item.Multiline || item.Help == "" {
		t.Fatalf("config dns item = %#v, want documented single-line field", item)
	}
	item, ok = e.MenuItem(node, "config", "batchSize")
	if !ok || item.Field == nil || item.Help == "" {
		t.Fatalf("config batchSize item = %#v, want documented field", item)
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
	for _, fieldID := range []string{"presharedKeySecret", "amneziaOverride", "amneziaJunkCount", "amneziaInitHeader", "amneziaInitiationPacket1"} {
		if !wireGuardTemplateHasField(item, fieldID) {
			t.Fatalf("peers template missing %q field", fieldID)
		}
	}
	if wireGuardTemplateHasField(item, "presharedKey") {
		t.Fatalf("peers template has legacy raw presharedKey field")
	}
	for _, fieldID := range []string{"runtimePeers", "activePeers", "connectedPeers", "rxBytes", "txBytes", "rxPackets", "txPackets", "latestHandshake"} {
		item, ok = e.MenuItem(node, "status", fieldID)
		if !ok || item.Field == nil || !item.Readonly {
			t.Fatalf("status %s item = %#v, want readonly stats field", fieldID, item)
		}
	}
}

func TestWireGuardAutoGeneratesPrivateKey(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)

	freshSecret, freshKey := waitWireGuardPrivateKeySecret(t, e, node)
	assertWireGuardPublicKeyPresent(t, e, node)
	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig fresh node: %v", err)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "privateKeySecret"}); err != nil || got != freshSecret {
		t.Fatalf("saved fresh privateKeySecret = %#v, %v; want %q", got, err, freshSecret)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "privateKey"}); err == nil {
		t.Fatalf("saved fresh privateKey = %#v, want missing", got)
	}
	if got, ok := e.Secrets.SecretValue(freshSecret); !ok || got != freshKey {
		t.Fatalf("stored fresh private key = %#v, %v; want %q", got, ok, freshKey)
	}

	legacyPrivate, legacyPublic := wireGuardTestKeyPair(t, 72)
	for _, tc := range []struct {
		name     string
		nodeData map[string]any
		wantPub  string
	}{
		{name: "missing", nodeData: map[string]any{"Class": NodeTypeWireGuard}},
		{name: "empty", nodeData: map[string]any{"Class": NodeTypeWireGuard, "privateKey": ""}},
		{name: "legacy raw privateKey", nodeData: map[string]any{"Class": NodeTypeWireGuard, "privateKey": legacyPrivate}, wantPub: legacyPublic},
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
			restoredSecret, restoredKey := waitWireGuardPrivateKeySecret(t, restored, restoredNode)
			if tc.wantPub != "" {
				assertWireGuardPublicKey(t, restored, restoredNode, tc.wantPub)
			} else {
				assertWireGuardPublicKeyPresent(t, restored, restoredNode)
			}

			restoredCfg := configer.NewMemory(nil)
			if err := restored.Engine.SaveConfig(restoredCfg); err != nil {
				t.Fatalf("SaveConfig restored node: %v", err)
			}
			if got, err := restoredCfg.Get(configer.Path{"nodes", "wg", "privateKeySecret"}); err != nil || got != restoredSecret {
				t.Fatalf("saved restored privateKeySecret = %#v, %v; want %q", got, err, restoredSecret)
			}
			if got, err := restoredCfg.Get(configer.Path{"nodes", "wg", "privateKey"}); err == nil {
				t.Fatalf("saved restored privateKey = %#v, want missing", got)
			}
			if got, ok := restored.Secrets.SecretValue(restoredSecret); !ok || got != restoredKey {
				t.Fatalf("stored restored private key = %#v, %v; want %q", got, ok, restoredKey)
			}
		})
	}
}

func TestWireGuardSecretValueChangedReloadsPrivateKey(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)
	secretName, oldPrivateKey := waitWireGuardPrivateKeySecret(t, e, node)
	oldPublicKey := wireGuardPublicKeyFromPrivateKey(t, oldPrivateKey)

	newPrivateKey, newPublicKey := wireGuardTestKeyPair(t, 74)
	e.Secrets.SetSecret(secretName, Secret{Value: newPrivateKey}, "wg")
	sendWireGuardSecretValueChanged(t, e, node, secretName, secretName)
	assertWireGuardPublicKey(t, e, node, newPublicKey)
	assertWireGuardConfigField(t, e, node, "privateKeySecret", secretName)

	e.Secrets.SetSecret(secretName, Secret{Value: "bad"}, "wg")
	sendWireGuardSecretValueChanged(t, e, node, secretName, secretName)
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKeySecret")
		return ok && item.Status == formular.StatusError
	}, "invalid WireGuard private key secret is rejected")
	assertWireGuardPublicKey(t, e, node, newPublicKey)

	e.Secrets.SetSecret(secretName, Secret{Value: oldPrivateKey}, "wg")
	sendWireGuardSecretValueChanged(t, e, node, secretName, secretName)
	assertWireGuardPublicKey(t, e, node, oldPublicKey)
}

func TestWireGuardSecretValueChangedRenamesOrRotatesPrivateKeySecret(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)
	oldSecret, oldPrivateKey := waitWireGuardPrivateKeySecret(t, e, node)
	oldPublicKey := wireGuardPublicKeyFromPrivateKey(t, oldPrivateKey)

	renamedPrivateKey, renamedPublicKey := wireGuardTestKeyPair(t, 75)
	renamedSecret := "renamed-wireguard-private-key"
	e.Secrets.SetSecret(renamedSecret, Secret{Value: renamedPrivateKey}, "wg")
	e.Secrets.DeleteSecret(oldSecret)
	sendWireGuardSecretValueChanged(t, e, node, oldSecret, renamedSecret)
	assertWireGuardConfigField(t, e, node, "privateKeySecret", renamedSecret)
	assertWireGuardPublicKey(t, e, node, renamedPublicKey)

	e.Secrets.DeleteSecret(renamedSecret)
	sendWireGuardSecretValueChanged(t, e, node, renamedSecret, renamedSecret)
	assertWireGuardConfigFieldNotEqual(t, e, node, "privateKeySecret", renamedSecret)
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKeySecret")
		if !ok {
			return false
		}
		secretName, ok := formular.StringValue(item.Value)
		if !ok || secretName == "" || secretName == renamedSecret {
			return false
		}
		rotatedPrivateKey, ok := e.Secrets.SecretValue(secretName)
		if !ok {
			return false
		}
		return rotatedPrivateKey != renamedPrivateKey && wireGuardPublicKeyFromPrivateKey(t, rotatedPrivateKey) != oldPublicKey
	}, "inaccessible WireGuard private key secret rotates")
}

func TestWireGuardSecretValueChangedReloadsPeerPresharedKey(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)
	privateKey, _ := wireGuardTestKeyPair(t, 76)
	_, peerPublicKey := wireGuardTestKeyPair(t, 77)
	oldPresharedKey := wireGuardTestPresharedKey(78)
	applyWireGuardConfig(t, e.Engine, node, privateKey, 0, []wireGuardPeerConfig{{
		id:           "peer-psk",
		publicKey:    peerPublicKey,
		presharedKey: oldPresharedKey,
		allowedIPs:   "10.77.0.2/32",
	}})
	var secretName string
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "peers", "peers")
		if !ok || item.Field == nil || len(item.Elements) == 0 {
			return false
		}
		for _, child := range item.Elements[0].Items {
			if child.ID != "presharedKeySecret" {
				continue
			}
			value, ok := formular.StringValue(child.Value)
			if ok && value != "" {
				secretName = value
				return true
			}
		}
		return false
	}, "peer preshared key migrates to secret")
	if got, ok := e.Secrets.SecretValue(secretName); !ok || got != oldPresharedKey {
		t.Fatalf("stored old peer preshared key = %#v, %v; want %q", got, ok, oldPresharedKey)
	}
	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig with peer preshared key: %v", err)
	}
	savedPeer := savedWireGuardPeerConfig(t, cfg, "wg", 0)
	if got, ok := savedPeer["presharedKey"]; ok {
		t.Fatalf("saved peer presharedKey = %#v, want missing", got)
	}
	if got, ok := formular.StringValue(savedPeer["presharedKeySecret"]); !ok || got != secretName {
		t.Fatalf("saved peer presharedKeySecret = %#v, want %q", savedPeer["presharedKeySecret"], secretName)
	}

	newPresharedKey := wireGuardTestPresharedKey(79)
	e.Secrets.SetSecret(secretName, Secret{Value: newPresharedKey}, "wg")
	sendWireGuardSecretValueChanged(t, e, node, secretName, secretName)
	assertWireGuardPeerField(t, e, node, "presharedKeySecret", secretName)

	renamedSecret := "renamed-wireguard-peer-psk"
	renamedPresharedKey := wireGuardTestPresharedKey(80)
	e.Secrets.SetSecret(renamedSecret, Secret{Value: renamedPresharedKey}, "wg")
	e.Secrets.DeleteSecret(secretName)
	sendWireGuardSecretValueChanged(t, e, node, secretName, renamedSecret)
	assertWireGuardPeerField(t, e, node, "presharedKeySecret", renamedSecret)

	e.Secrets.SetSecret(renamedSecret, Secret{Value: "bad"}, "wg")
	sendWireGuardSecretValueChanged(t, e, node, renamedSecret, renamedSecret)
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "peers", "peers")
		return ok && item.Status == formular.StatusError
	}, "invalid WireGuard peer preshared key secret is rejected")
}

func TestWireGuardConfigPersistsAcrossSaveRestoreAndCopyPaste(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeWireGuard, "wg")
	e.SubscribeMenus(node)
	privateKey, publicKey := wireGuardTestKeyPair(t, 1)
	_, peerPublicKey := wireGuardTestKeyPair(t, 2)
	ownedIPs := "10.10.10.2/32\nfd00:10:10::2/128"
	dns := "8.8.8.8"
	amneziaWG := wireGuardTestAmneziaConfig()
	peerAmneziaWG := wireGuardTestAmneziaConfig()
	peerAmneziaWG.initHeader = "6111-6113"
	peerAmneziaWG.responseHeader = "6222-6225"
	peerAmneziaWG.initiationPackets[0] = "<b 0xbeef><rc 2>"
	applyWireGuardConfigWithBatchAndOwnedIPs(t, e.Engine, node, privateKey, 51820, 8, ownedIPs, []wireGuardPeerConfig{{
		id:                          "peer-main",
		publicKey:                   peerPublicKey,
		presharedKey:                "",
		endpoint:                    "127.0.0.1:51821",
		allowedIPs:                  "10.10.20.1/32",
		persistentKeepaliveInterval: 15,
		amneziaWGOverride:           true,
		amneziaWG:                   peerAmneziaWG,
	}})
	applyWireGuardDNS(t, e.Engine, node, dns)
	applyWireGuardAmneziaConfig(t, e.Engine, node, amneziaWG)

	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "status", "publicKey")
		return ok && item.Value == publicKey
	}, "wireguard public key status")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	privateKeySecret, err := cfg.Get(configer.Path{"nodes", "wg", "privateKeySecret"})
	if err != nil {
		t.Fatalf("saved privateKeySecret missing: %v", err)
	}
	privateKeySecretText, ok := formular.StringValue(privateKeySecret)
	if !ok || privateKeySecretText == "" {
		t.Fatalf("saved privateKeySecret = %#v, want non-empty string", privateKeySecret)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "privateKey"}); err == nil {
		t.Fatalf("saved privateKey = %#v, want missing", got)
	}
	if got, ok := e.Secrets.SecretValue(privateKeySecretText); !ok || got != privateKey {
		t.Fatalf("stored private key = %#v, %v; want %q", got, ok, privateKey)
	}
	gotPort, err := cfg.Get(configer.Path{"nodes", "wg", "listenPort"})
	if err != nil {
		t.Fatalf("saved listenPort missing: %v", err)
	}
	if got, ok := formular.IntValue(gotPort); !ok || got != 51820 {
		t.Fatalf("saved listenPort = %#v, want 51820", gotPort)
	}
	gotBatchSize, err := cfg.Get(configer.Path{"nodes", "wg", "batchSize"})
	if err != nil {
		t.Fatalf("saved batchSize missing: %v", err)
	}
	if got, ok := formular.IntValue(gotBatchSize); !ok || got != 8 {
		t.Fatalf("saved batchSize = %#v, want 8", gotBatchSize)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "ownedIPs"}); err != nil || got != ownedIPs {
		t.Fatalf("saved ownedIPs = %#v, %v; want %q", got, err, ownedIPs)
	}
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "dns"}); err != nil || got != dns {
		t.Fatalf("saved dns = %#v, %v; want %q", got, err, dns)
	}
	assertSavedWireGuardAmneziaConfig(t, cfg, "wg", amneziaWG)
	if got, err := cfg.Get(configer.Path{"nodes", "wg", "fwmark"}); err == nil {
		t.Fatalf("saved fwmark = %#v, want missing removed field", got)
	}

	restored := newNodeE2EWithConfigAndSecrets(t, cfg, e.Secrets)
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
	assertWireGuardConfigField(t, restored, restoredNode, "batchSize", 8)
	assertWireGuardConfigField(t, restored, restoredNode, "ownedIPs", ownedIPs)
	assertWireGuardConfigField(t, restored, restoredNode, "dns", dns)
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
		value, _ := formular.StringValue(item.Value)
		return ok && value != "" && value != publicKey
	}, "pasted wireguard public key rotated")
	assertWireGuardConfigFieldNotEqual(t, e, pastedNode, "privateKeySecret", privateKeySecretText)
	assertWireGuardConfigField(t, e, pastedNode, "batchSize", 8)
	assertWireGuardConfigField(t, e, pastedNode, "ownedIPs", ownedIPs)
	assertWireGuardConfigField(t, e, pastedNode, "dns", dns)
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
	privateKey, _ := wireGuardTestKeyPair(t, 3)
	invalidSecret := wireGuardTestPrivateKeySecret(t, e.Engine, node, "bad")
	validSecret := wireGuardTestPrivateKeySecret(t, e.Engine, node, privateKey)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "privateKeySecret"},
		Value:       invalidSecret,
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKeySecret")
		return ok && item.Status == formular.StatusError
	}, "private key secret validation error")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "privateKeySecret"},
		Value:       validSecret,
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKeySecret")
		return ok && item.Status == formular.StatusOK
	}, "private key secret validation ok")

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
		Field:       formular.FieldRef{BlockID: "config", FieldID: "dns"},
		Value:       "8.8.8.8/32",
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "dns")
		return ok && item.Status == formular.StatusError
	}, "DNS validation error")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "dns"},
		Value:       "2001:4860:4860::8888",
	})
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "dns")
		return ok && item.Status == formular.StatusOK
	}, "DNS validation ok")

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

func savedWireGuardPeerConfig(t *testing.T, cfg configer.Config, nodeName string, index int) map[string]any {
	t.Helper()
	raw, err := cfg.Get(configer.Path{"nodes", nodeName, "peers"})
	if err != nil {
		t.Fatalf("saved peers missing: %v", err)
	}
	switch peers := raw.(type) {
	case []map[string]any:
		if index < 0 || index >= len(peers) {
			t.Fatalf("saved peer index %d outside %#v", index, peers)
		}
		return peers[index]
	case []any:
		if index < 0 || index >= len(peers) {
			t.Fatalf("saved peer index %d outside %#v", index, peers)
		}
		peer, ok := peers[index].(map[string]any)
		if !ok {
			t.Fatalf("saved peer %d = %#v, want map", index, peers[index])
		}
		return peer
	default:
		t.Fatalf("saved peers = %#v, want array", raw)
		return nil
	}
}

const wireGuardAmneziaInitiationPacketCount = 5

type wireGuardAmneziaConfig struct {
	junkCount         int
	junkMin           int
	junkMax           int
	initPadding       int
	responsePadding   int
	cookiePadding     int
	transportPadding  int
	initHeader        string
	responseHeader    string
	cookieHeader      string
	transportHeader   string
	initiationPackets [wireGuardAmneziaInitiationPacketCount]string
}

type wireGuardPeerConfig struct {
	id                          string
	publicKey                   string
	presharedKey                string
	endpoint                    string
	allowedIPs                  string
	persistentKeepaliveInterval int
	amneziaWGOverride           bool
	amneziaWG                   wireGuardAmneziaConfig
}

func (p *wireGuardPeerConfig) normalize(index int) {
	p.publicKey = normalizeWireGuardKey(p.publicKey)
	p.presharedKey = normalizeWireGuardKey(p.presharedKey)
	p.endpoint = strings.TrimSpace(p.endpoint)
	p.allowedIPs = strings.TrimSpace(p.allowedIPs)
	if p.persistentKeepaliveInterval < 0 || p.persistentKeepaliveInterval > 65535 {
		p.persistentKeepaliveInterval = 0
	}
	p.id = strings.TrimSpace(p.id)
	if p.id == "" {
		p.id = "peer-" + strconv.Itoa(index+1)
	}
}

func applyWireGuardConfig(t *testing.T, engine Engine, node uint64, privateKey string, listenPort int, peers []wireGuardPeerConfig) {
	t.Helper()
	applyWireGuardConfigWithOwnedIPs(t, engine, node, privateKey, listenPort, "", peers)
}

func applyWireGuardConfigWithOwnedIPs(t *testing.T, engine Engine, node uint64, privateKey string, listenPort int, ownedIPs string, peers []wireGuardPeerConfig) {
	t.Helper()
	applyWireGuardConfigWithBatchAndOwnedIPs(t, engine, node, privateKey, listenPort, 0, ownedIPs, peers)
}

func applyWireGuardConfigWithBatchAndOwnedIPs(t *testing.T, engine Engine, node uint64, privateKey string, listenPort, batchSize int, ownedIPs string, peers []wireGuardPeerConfig) {
	t.Helper()
	privateKeySecret := wireGuardTestPrivateKeySecret(t, engine, node, privateKey)
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"privateKeySecret": privateKeySecret,
			"listenPort":       listenPort,
			"batchSize":        batchSize,
			"ownedIPs":         ownedIPs,
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

func applyWireGuardDNS(t *testing.T, engine Engine, node uint64, dns string) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values:      map[string]any{"dns": dns},
	})
}

func applyWireGuardPeers(t *testing.T, engine Engine, node uint64, peers []wireGuardPeerConfig) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "peers",
		Values: map[string]any{
			"peers": wireGuardPeerValues(peers),
		},
	})
}

func wireGuardTestPrivateKeySecret(t *testing.T, engine Engine, node uint64, privateKey string) string {
	t.Helper()
	storage := engine.SecretsStorage()
	if storage == nil {
		t.Fatal("test engine has no secrets storage")
	}
	snapshot, ok := engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	return wireGuardTestPrivateKeySecretForNode(t, storage, snapshot.Name, privateKey)
}

func wireGuardTestPrivateKeySecretForNode(t *testing.T, storage SecretsStorage, nodeName string, privateKey string) string {
	t.Helper()
	if storage == nil {
		t.Fatal("test has no secrets storage")
	}
	name, err := storage.NewSecret(Secret{Value: privateKey}, nodeName)
	if err != nil {
		t.Fatalf("NewSecret for WireGuard private key: %v", err)
	}
	return name
}

func wireGuardTestPresharedKey(seed byte) string {
	var key wgdevice.NoisePresharedKey
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key.Base64()
}

func sendWireGuardSecretValueChanged(t *testing.T, e *NodeE2E, node uint64, prev, next string) {
	t.Helper()
	e.Engine.SendInbox(E2EWorkspace, pasta.InboxMessage{
		ReceiverNode: node,
		Payload: SecretValueChanged{
			SecretNamePrev: prev,
			SecretNameNew:  next,
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
	var config wireGuardAmneziaConfig
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

func wireGuardAmneziaInitiationPacketsFromAny(raw any) ([wireGuardAmneziaInitiationPacketCount]string, bool) {
	var out [wireGuardAmneziaInitiationPacketCount]string
	switch values := raw.(type) {
	case []string:
		for i := range out {
			if i < len(values) {
				out[i] = values[i]
			}
		}
		return out, true
	case []any:
		for i := range out {
			if i >= len(values) {
				continue
			}
			value, ok := formular.StringValue(values[i])
			if !ok {
				return out, false
			}
			out[i] = value
		}
		return out, true
	default:
		return out, false
	}
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

func assertWireGuardConfigFieldNotEqual(t *testing.T, e *NodeE2E, node uint64, fieldID string, notWant any) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", fieldID)
		return ok && !reflect.DeepEqual(item.Value, notWant)
	}, "wireguard config field %s != %#v", fieldID, notWant)
}

func waitWireGuardPrivateKeySecret(t *testing.T, e *NodeE2E, node uint64) (secretName, privateKey string) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "config", "privateKeySecret")
		if !ok || item.Field == nil {
			return false
		}
		value, ok := formular.StringValue(item.Value)
		if !ok || value == "" {
			return false
		}
		keyValue, ok := e.Secrets.SecretValue(value)
		if !ok {
			return false
		}
		key, err := parseWireGuardPrivateKey(keyValue)
		if err != nil || key.IsZero() {
			return false
		}
		secretName = value
		privateKey = normalizeWireGuardKey(keyValue)
		return true
	}, "wireguard generated private key secret")
	return secretName, privateKey
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

func assertWireGuardPublicKey(t *testing.T, e *NodeE2E, node uint64, want string) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "status", "publicKey")
		return ok && item.Value == want
	}, "wireguard public key %q", want)
}

func waitWireGuardPublicKeyValue(t *testing.T, e *NodeE2E, node uint64) string {
	t.Helper()
	var got string
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, "status", "publicKey")
		if !ok || item.Field == nil {
			return false
		}
		value, ok := formular.StringValue(item.Value)
		if !ok || value == "" {
			return false
		}
		if _, err := parseWireGuardPublicKey(value); err != nil {
			return false
		}
		got = value
		return true
	}, "wireguard public key value")
	return got
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

func wireGuardPublicKeyFromPrivateKey(t *testing.T, privateKeyBase64 string) string {
	t.Helper()
	privateKey, err := parseWireGuardPrivateKey(privateKeyBase64)
	if err != nil {
		t.Fatalf("parseWireGuardPrivateKey: %v", err)
	}
	publicKey := wireGuardPublicKeyBase64(privateKey)
	if publicKey == "" {
		t.Fatal("wireGuardPublicKeyBase64 returned empty key")
	}
	return publicKey
}

func parseWireGuardPrivateKey(value string) (wgdevice.NoisePrivateKey, error) {
	return wgdevice.ParsePrivateKey(strings.TrimSpace(value))
}

func parseWireGuardPublicKey(value string) (wgdevice.NoisePublicKey, error) {
	return wgdevice.ParsePublicKey(strings.TrimSpace(value))
}

func normalizeWireGuardKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if key, err := wgdevice.ParsePresharedKey(value); err == nil {
		return key.Base64()
	}
	return value
}

func wireGuardPublicKeyBase64(privateKey wgdevice.NoisePrivateKey) string {
	if privateKey.IsZero() {
		return ""
	}
	return privateKey.PublicKey().Base64()
}
