package wireguardnodes

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/testkit"
	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
)

func TestWireGuardRuntimeStatsMessageUpdatesStatusRows(t *testing.T) {
	dev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
	defer dev.Close()

	n := &wireGuardNode{dev: dev}
	ignoredDev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
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

func TestWireGuardMergesMenuAndControllerOwnedIPs(t *testing.T) {
	node := &wireGuardNode{
		config: wireGuardConfig{ownedIPs: "10.60.0.2\nfd00:60::2/128"},
		wgctrlConfigs: map[uint64]WGCtrlInterfaceConfig{
			9: {OwnedIPs: []string{"10.90.0.2/32", "10.40.0.2/32"}},
			4: {OwnedIPs: []string{"10.40.0.2/32", "10.60.0.2/32"}},
		},
	}
	want := []string{"10.60.0.2/32", "fd00:60::2/128", "10.40.0.2/32", "10.90.0.2/32"}
	if got := node.effectiveOwnedIPAddrs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("effective owned IPs = %v, want %v", got, want)
	}
	assertWireGuardStatusItemValue(t, node.statusBlock(), "ownedIPs", strings.Join(want, "\n"))

	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{OwnedIPs: []string{"10.41.0.2"}}); err != nil {
		t.Fatalf("update controller owned IP: %v", err)
	}
	want = []string{"10.60.0.2/32", "fd00:60::2/128", "10.41.0.2/32", "10.90.0.2/32", "10.40.0.2/32"}
	if got := node.effectiveOwnedIPAddrs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("updated effective owned IPs = %v, want %v", got, want)
	}

	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{}); err != nil {
		t.Fatalf("withdraw controller owned IPs: %v", err)
	}
	want = []string{"10.60.0.2/32", "fd00:60::2/128", "10.90.0.2/32", "10.40.0.2/32"}
	if got := node.effectiveOwnedIPAddrs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("withdrawn effective owned IPs = %v, want %v", got, want)
	}
}

func TestWireGuardMergesConfiguredAndControllerTunRoutes(t *testing.T) {
	node := &wireGuardNode{
		config: wireGuardConfig{peers: []wireGuardPeerConfig{
			{allowedIPs: "0.0.0.0/0 10.40.0.0/16"},
			{allowedIPs: "fd00:40::/48"},
		}},
		wgctrlConfigs: map[uint64]WGCtrlInterfaceConfig{
			9: {Routes: []string{"10.90.0.0/16", "10.40.0.0/16"}},
			4: {Routes: []string{"100.64.0.0/10", "fd00:40::/48"}},
		},
	}
	want := []string{
		"0.0.0.0/0", "10.40.0.0/16", "fd00:40::/48",
		"100.64.0.0/10", "10.90.0.0/16",
	}
	if got := node.effectiveTunRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("effective TUN routes = %v, want %v", got, want)
	}
	if got := node.tunConfig().Routes; !reflect.DeepEqual(got, want) {
		t.Fatalf("TUN config routes = %v, want %v", got, want)
	}

	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{
		Routes: []string{"100.97.143.122", "fd7a:115c:a1e0::/48", "100.97.143.122/32"},
	}); err != nil {
		t.Fatalf("replace controller routes: %v", err)
	}
	want = []string{
		"0.0.0.0/0", "10.40.0.0/16", "fd00:40::/48",
		"100.97.143.122/32", "fd7a:115c:a1e0::/48", "10.90.0.0/16",
	}
	if got := node.effectiveTunRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("updated effective TUN routes = %v, want %v", got, want)
	}

	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{Routes: []string{"bad route"}}); err == nil {
		t.Fatal("invalid controller route was accepted")
	}
	want = []string{"0.0.0.0/0", "10.40.0.0/16", "fd00:40::/48", "10.90.0.0/16"}
	if got := node.effectiveTunRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("routes after invalid update = %v, want %v", got, want)
	}
}

func TestWireGuardUsesLatestWGCtrlDNSUnlessMenuDNSIsSet(t *testing.T) {
	node := &wireGuardNode{config: defaultWireGuardConfig()}

	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{DNS: "1.1.1.1"}); err != nil {
		t.Fatalf("accept first controller DNS: %v", err)
	}
	if got := node.effectiveDNS(); got != "1.1.1.1" {
		t.Fatalf("effective DNS = %q, want 1.1.1.1", got)
	}
	if err := node.acceptWGCtrlInterfaceConfig(9, WGCtrlInterfaceConfig{DNS: "8.8.8.8"}); err != nil {
		t.Fatalf("accept second controller DNS: %v", err)
	}
	if got := node.effectiveDNS(); got != "8.8.8.8" {
		t.Fatalf("effective DNS = %q, want latest 8.8.8.8", got)
	}
	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{
		OwnedIPs: []string{"10.41.0.2/32"},
		DNS:      "9.9.9.9",
	}); err != nil {
		t.Fatalf("replace first controller DNS: %v", err)
	}
	if got := node.effectiveDNS(); got != "9.9.9.9" {
		t.Fatalf("effective DNS = %q, want latest replacement 9.9.9.9", got)
	}

	node.config.dns = "4.4.4.4"
	if got := node.tunConfig().DNS; got != "4.4.4.4" {
		t.Fatalf("TUN DNS = %q, want menu DNS 4.4.4.4", got)
	}
	node.config.dns = ""
	if err := node.acceptWGCtrlInterfaceConfig(4, WGCtrlInterfaceConfig{}); err != nil {
		t.Fatalf("withdraw latest controller DNS: %v", err)
	}
	if got := node.effectiveDNS(); got != "8.8.8.8" {
		t.Fatalf("effective DNS = %q, want previous current suggestion 8.8.8.8", got)
	}
	if err := node.acceptWGCtrlInterfaceConfig(9, WGCtrlInterfaceConfig{DNS: "invalid"}); err == nil {
		t.Fatal("invalid controller DNS was accepted")
	}
	if got := node.effectiveDNS(); got != "" {
		t.Fatalf("effective DNS after rejection = %q, want empty", got)
	}
}

func TestWireGuardBatchSizeConfiguresDeviceAndBind(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 51)
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	node := &wireGuardNode{config: wireGuardConfig{
		privateKey: privateKey,
		batchSize:  3,
		amneziaWG:  defaultWireGuardAmneziaConfig(),
	}}
	node.config.normalize()

	node.runtimeMu.Lock()
	node.ensureDeviceLocked(node.config.batchSize)
	if got := node.dev.BatchSize(); got != 3 {
		node.runtimeMu.Unlock()
		t.Fatalf("device BatchSize() = %d, want 3", got)
	}
	node.currentNetwork = network
	node.networkGeneration.Store(1)
	node.currentNetworkGeneration = 1
	if _, err := node.attachCurrentNetworkBindLocked(node.config.batchSize); err != nil {
		node.runtimeMu.Unlock()
		t.Fatalf("attachCurrentNetworkBindLocked: %v", err)
	}
	if got := node.currentBind.BatchSize(); got != 3 {
		node.runtimeMu.Unlock()
		t.Fatalf("bind BatchSize() = %d, want 3", got)
	}
	dev := node.dev
	node.runtimeMu.Unlock()
	t.Cleanup(dev.Close)
}

func TestResolveWireGuardPrivateKeySecret(t *testing.T) {
	nodeName := "wg"
	storage := newWireGuardTestSecretsStorage()
	validPrivate, validPublic := wireGuardTestKeyPair(t, 61)
	legacyPrivate, legacyPublic := wireGuardTestKeyPair(t, 62)
	accessibleSecret, err := storage.NewSecret(Secret{Value: validPrivate}, nodeName)
	if err != nil {
		t.Fatalf("NewSecret accessible: %v", err)
	}
	inaccessibleSecret := "inaccessible-wireguard-private-key"
	storage.SetSecret(inaccessibleSecret, Secret{Value: validPrivate}, "other-wg")
	invalidSecret := "invalid-wireguard-private-key"
	storage.SetSecret(invalidSecret, Secret{Value: "bad"}, nodeName)

	t.Run("accessible configured secret", func(t *testing.T) {
		gotPrivate, gotSecret, err := resolveWireGuardPrivateKeySecret(storage, nodeName, accessibleSecret, legacyPrivate)
		if err != nil {
			t.Fatalf("resolve accessible secret: %v", err)
		}
		if gotSecret != accessibleSecret {
			t.Fatalf("secret = %q, want %q", gotSecret, accessibleSecret)
		}
		if gotPublic := wireGuardPublicKeyFromPrivateKey(t, gotPrivate); gotPublic != validPublic {
			t.Fatalf("public key = %q, want %q", gotPublic, validPublic)
		}
	})

	t.Run("empty secret migrates legacy value", func(t *testing.T) {
		gotPrivate, gotSecret, err := resolveWireGuardPrivateKeySecret(storage, nodeName, "", legacyPrivate)
		if err != nil {
			t.Fatalf("resolve legacy value: %v", err)
		}
		if gotSecret == "" {
			t.Fatal("secret name is empty")
		}
		if gotPublic := wireGuardPublicKeyFromPrivateKey(t, gotPrivate); gotPublic != legacyPublic {
			t.Fatalf("public key = %q, want %q", gotPublic, legacyPublic)
		}
		if stored, ok := storage.SecretValue(gotSecret); !ok || stored != legacyPrivate {
			t.Fatalf("stored legacy private key = %q, %v; want %q", stored, ok, legacyPrivate)
		}
	})

	t.Run("inaccessible configured secret rotates instead of using legacy", func(t *testing.T) {
		gotPrivate, gotSecret, err := resolveWireGuardPrivateKeySecret(storage, nodeName, inaccessibleSecret, legacyPrivate)
		if err != nil {
			t.Fatalf("resolve inaccessible secret: %v", err)
		}
		if gotSecret == "" || gotSecret == inaccessibleSecret {
			t.Fatalf("secret = %q, want generated replacement", gotSecret)
		}
		if gotPrivate == legacyPrivate {
			t.Fatal("private key reused legacy value for inaccessible configured secret")
		}
		if stored, ok := storage.SecretValue(gotSecret); !ok || normalizeWireGuardKey(stored) != gotPrivate {
			t.Fatalf("stored replacement key = %q, %v; want %q", stored, ok, gotPrivate)
		}
	})

	t.Run("accessible invalid secret reports error", func(t *testing.T) {
		if _, _, err := resolveWireGuardPrivateKeySecret(storage, nodeName, invalidSecret, ""); err == nil {
			t.Fatal("resolve invalid secret succeeded")
		}
	})

	t.Run("missing storage reports error", func(t *testing.T) {
		if _, _, err := resolveWireGuardPrivateKeySecret(nil, nodeName, accessibleSecret, ""); !errors.Is(err, errWireGuardSecretsUnavailable) {
			t.Fatalf("resolve missing storage error = %v, want %v", err, errWireGuardSecretsUnavailable)
		}
	})
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
			amneziaWGEnabled:  true,
			amneziaWG:         peerAmneziaWG,
		}},
	}}
	node.config.normalize()

	node.runtimeMu.Lock()
	node.ensureDeviceLocked(node.config.batchSize)
	node.runtimeMu.Unlock()
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

func TestValidateWireGuardDNS(t *testing.T) {
	for _, value := range []string{"", "8.8.8.8", "2001:4860:4860::8888", " 1.1.1.1 "} {
		if err := validateWireGuardDNS(value); err != nil {
			t.Errorf("validateWireGuardDNS(%q) error = %v, want nil", value, err)
		}
	}
	for _, value := range []string{"dns.example", "8.8.8.8/32", "8.8.8.8 1.1.1.1"} {
		if err := validateWireGuardDNS(value); err == nil {
			t.Errorf("validateWireGuardDNS(%q) error = nil, want error", value)
		}
	}
}

func TestWGCtrlInterfaceConfigApplyValidatesWithoutTun(t *testing.T) {
	node := &wireGuardNode{
		ctrlp:         1,
		wgctrlConfigs: map[uint64]WGCtrlInterfaceConfig{},
		config:        defaultWireGuardConfig(),
	}
	apply := func(addresses []string) error {
		t.Helper()
		result := make(chan error, 1)
		request := &wgCtrlInterfaceConfigApply{
			config: WGCtrlInterfaceConfig{OwnedIPs: addresses},
			result: result,
		}
		err := node.OnEvent(pasta.Event{
			ReceiverPort: 1,
			Link:         7,
			Payload:      request,
		}, TypeWGCtrl, nil, "left")
		if err != nil {
			t.Fatalf("OnEvent() error = %v", err)
		}
		if err := node.OnEvent(pasta.Event{
			ReceiverPort: 1,
			Link:         7,
			Payload:      wgCtrlInterfaceConfigCommit{apply: request},
		}, TypeWGCtrl, nil, "left"); err != nil {
			t.Fatalf("commit OnEvent() error = %v", err)
		}
		select {
		case err := <-result:
			return err
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for interface config result")
			return nil
		}
	}

	if err := apply([]string{"10.31.0.2/32"}); err == nil {
		t.Fatal("valid interface config succeeded without a TUN link")
	}
	if got := node.effectiveOwnedIPAddrs(); !reflect.DeepEqual(got, []string{"10.31.0.2/32"}) {
		t.Fatalf("effective addresses = %v, want [10.31.0.2/32]", got)
	}
	if err := apply([]string{"invalid"}); err == nil {
		t.Fatal("invalid interface config error = nil")
	}
	if got := node.effectiveOwnedIPAddrs(); got != nil {
		t.Fatalf("effective addresses after rejection = %v, want nil", got)
	}
}

func TestWGCtrlInterfaceConfigApplyFailsWhenTunLinkIsRemoved(t *testing.T) {
	result := make(chan error, 1)
	apply := &wgCtrlInterfaceConfigApply{link: 7, result: result}
	n := &wireGuardNode{
		config:               defaultWireGuardConfig(),
		tunp:                 1,
		tunLink:              2,
		wgctrlPendingApplies: map[*wgCtrlInterfaceConfigApply]uint64{apply: 3},
	}

	if err := n.OnLinkRemoved(2, 1, TypeTun, "left"); err != nil {
		t.Fatalf("OnLinkRemoved() error = %v", err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("pending interface config apply succeeded after TUN removal")
		}
	case <-time.After(time.Second):
		t.Fatal("pending interface config apply did not terminate after TUN removal")
	}
	if len(n.wgctrlPendingApplies) != 0 {
		t.Fatal("TUN removal left a pending interface config apply")
	}
}

func TestWGCtrlInterfaceConfigApplyFailsWithoutTunLink(t *testing.T) {
	result := make(chan error, 1)
	apply := &wgCtrlInterfaceConfigApply{link: 7, result: result}
	n := &wireGuardNode{}

	n.commitWGCtrlInterfaceConfigApply(7, apply)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("interface config apply succeeded without a TUN link")
		}
	case <-time.After(time.Second):
		t.Fatal("interface config apply did not terminate without a TUN link")
	}
}

func TestWGCtrlInterfaceConfigApplyRequiresHealthyDesiredTransport(t *testing.T) {
	sourceTun, peerTun := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	n := &wireGuardNode{
		currentTun:               sourceTun,
		currentTunGeneration:     3,
		currentNetwork:           network,
		currentNetworkGeneration: 1,
		currentBind:              wireGuardTrackedBind{},
		currentBindFailed:        true,
	}
	n.tunGeneration.Store(3)
	n.networkGeneration.Store(1)

	// Generation 3 supersedes generation 2 and includes its still-current
	// controller contribution, but a failed desired transport must prevent the
	// acknowledgement until the device is usable.
	if n.wgCtrlInterfaceConfigReadyLocked(2, true) {
		t.Fatal("interface config was ready while the desired bind was failed")
	}
	n.currentBindFailed = false
	n.runtimeDeviceUp = true
	if !n.wgCtrlInterfaceConfigReadyLocked(2, true) {
		t.Fatal("later healthy TUN generation did not satisfy earlier interface config")
	}
	n.tunGeneration.Store(4)
	if n.wgCtrlInterfaceConfigReadyLocked(2, true) {
		t.Fatal("older installed TUN satisfied interface config while a replacement was pending")
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

func TestWireGuardObjectPeersParseAndMenuConflictWins(t *testing.T) {
	_, menuKey := wireGuardTestKeyPair(t, 101)
	_, objectKey := wireGuardTestKeyPair(t, 102)
	object, err := std.ObjectFromJSON([]byte(fmt.Sprintf(`{
		"metadata":{"source":"test"},
		"peers":[
			{"id":"object-conflict","publicKey":%q,"endpoint":"127.0.0.1:1","allowedIPs":"10.101.0.0/24"},
			{"id":"object-only","publicKey":%q,"allowedIPs":"10.102.0.0/24","persistentKeepaliveInterval":17}
		]
	}`, menuKey, objectKey)))
	if err != nil {
		t.Fatalf("ObjectFromJSON: %v", err)
	}
	peers, err := wireGuardPeersFromObject(object)
	if err != nil {
		t.Fatalf("wireGuardPeersFromObject: %v", err)
	}
	if len(peers) != 2 || peers[1].persistentKeepaliveInterval != 17 {
		t.Fatalf("parsed object peers = %#v, want two peers and keepalive 17", peers)
	}

	menu := []wireGuardPeerConfig{{
		id:         "menu-winner",
		publicKey:  menuKey,
		endpoint:   "127.0.0.1:51820",
		allowedIPs: "10.200.0.0/24",
	}}
	merged := mergeWireGuardPeers(menu, map[uint64][]wireGuardPeerConfig{
		20: peers,
		10: {{id: "lower-link-winner", publicKey: objectKey, allowedIPs: "10.201.0.0/24"}},
	})
	if len(merged) != 2 {
		t.Fatalf("merged peers = %d, want 2", len(merged))
	}
	if merged[0].id != "menu-winner" || merged[0].endpoint != "127.0.0.1:51820" {
		t.Fatalf("merged menu conflict = %#v, want menu peer", merged[0])
	}
	if merged[1].id != "lower-link-winner" {
		t.Fatalf("merged object conflict = %#v, want lower link ID peer", merged[1])
	}
}

func TestWireGuardPeersFromObjectRejectsMissingOrInvalidPeers(t *testing.T) {
	for _, input := range []string{
		`null`,
		`{"metadata":true}`,
		`{"peers":{}}`,
		`{"peers":[true]}`,
	} {
		object, err := std.ObjectFromJSON([]byte(input))
		if err != nil {
			t.Fatalf("ObjectFromJSON(%s): %v", input, err)
		}
		if _, err := wireGuardPeersFromObject(object); err == nil {
			t.Fatalf("wireGuardPeersFromObject(%s) succeeded", input)
		}
	}
}

func TestWireGuardNetworkAttachmentDoesNotOpenSocketsUntilTunAttached(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(wireGuardTestTunProviderClass{}); err != nil {
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
	tunID, err := w.AddNodeByClass(wireGuardTestTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	networkSnapshot := testkit.MustNodeSnapshot(t, w, networkID)
	tunSnapshot := testkit.MustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	eventually(t, func() bool {
		wg.runtimeMu.Lock()
		attached := wg.currentNetwork != nil && wg.currentBind != nil
		wg.runtimeMu.Unlock()
		return attached
	}, "wireguard network attached")
	if got := network.listenCount(); got != 0 {
		t.Fatalf("network listen count after network-only attach = %d, want 0", got)
	}

	privateKey, _ := wireGuardTestKeyPair(t, 4)
	privateKeySecret := wireGuardTestPrivateKeySecretForNode(t, wg.secrets, wg.nodeName, privateKey)
	_, peerPublicKey := wireGuardTestKeyPair(t, 5)
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(wgID)},
		BlockID:     "config",
		Values: map[string]any{
			"privateKeySecret": privateKeySecret,
			"listenPort":       51820,
		},
	})
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(wgID)},
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
	eventually(t, func() bool {
		return network.listenCount() > 0
	}, "wireguard opens network after TUN attaches")
	eventually(t, func() bool {
		wg.runtimeMu.Lock()
		defer wg.runtimeMu.Unlock()
		return wg.runtimeDeviceUp && wg.dev != nil && wg.dev.RuntimeStats().ActivePeerCount == 1
	}, "wireguard peer becomes active")

	w.RemoveLink(tunLink)
	eventually(t, func() bool {
		wg.runtimeMu.Lock()
		defer wg.runtimeMu.Unlock()
		return wg.currentTun == nil && wg.runtimeDeviceUp && wg.dev != nil &&
			wg.dev.RuntimeStats().ActivePeerCount == 1 &&
			wg.dev.RuntimeStats().ActiveTransportCount == 1
	}, "wireguard remains active while waiting for a replacement TUN")
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(wgID)},
		BlockID:     "config",
		Values: map[string]any{
			"privateKeySecret": privateKeySecret,
			"listenPort":       51822,
		},
	})
	eventually(t, func() bool {
		wg.runtimeMu.Lock()
		defer wg.runtimeMu.Unlock()
		return wg.runtimeDeviceUp && wg.dev != nil && wg.dev.ListenPort() == 51822 &&
			wg.dev.RuntimeStats().ActivePeerCount == 1
	}, "wireguard remains active after config apply without a TUN")
}

// A secret mutation is accepted by the workspace before its asynchronous
// device apply completes. The node label is the public convergence barrier:
// callers must not treat the secret API response as proof that the data plane
// already uses the new key.
func TestWireGuardPresharedKeySecretRotationPublishesRuntimeConvergence(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })
	testPresharedKey := func(seed byte) string {
		var key wgdevice.NoisePresharedKey
		for i := range key {
			key[i] = seed + byte(i)
		}
		return key.Base64()
	}

	secrets := newWireGuardTestSecretsStorage()
	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg, secrets: secrets}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	provider := links.NewLayer3DatagramMTUProvider(
		1500, true, false, links.MTUConstraint{Source: "rotation test carrier"},
	)
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network, provider: provider}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(wireGuardTestTunProviderClass{}); err != nil {
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
	tunID, err := w.AddNodeByClass(wireGuardTestTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	networkSnapshot := testkit.MustNodeSnapshot(t, w, networkID)
	tunSnapshot := testkit.MustNodeSnapshot(t, w, tunID)
	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	privateKey, _ := wireGuardTestKeyPair(t, 30)
	privateKeySecret := wireGuardTestPrivateKeySecretForNode(t, secrets, "wg", privateKey)
	_, peerPublicKeyText := wireGuardTestKeyPair(t, 31)
	peerPublicKey, err := parseWireGuardPublicKey(peerPublicKeyText)
	if err != nil {
		t.Fatalf("parse peer public key: %v", err)
	}
	initialPresharedKey := testPresharedKey(40)
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(wgID)},
		BlockID:     "config",
		Values: map[string]any{
			"privateKeySecret": privateKeySecret,
		},
	})
	w.SendNodeFormularMsg(wgID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(wgID)},
		BlockID:     "peers",
		Values: map[string]any{
			"peers": wireGuardPeerValues([]wireGuardPeerConfig{{
				id:                          "rotating-peer",
				publicKey:                   peerPublicKeyText,
				presharedKey:                initialPresharedKey,
				endpoint:                    "127.0.0.1:51821",
				allowedIPs:                  "10.77.0.2/32",
				persistentKeepaliveInterval: 1,
			}}),
		},
	})
	eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		if !ok || snapshot.Label != "up" {
			return false
		}
		wg.runtimeMu.Lock()
		defer wg.runtimeMu.Unlock()
		if wg.dev == nil {
			return false
		}
		spec, ok := wg.dev.PeerSpec(peerPublicKey)
		want, err := parseWireGuardPresharedKey(initialPresharedKey)
		return err == nil && ok && spec.PresharedKey == want
	}, "initial preshared key applied")

	w.Lock()
	secretName := wg.config.peers[0].presharedKeySecret
	w.Unlock()
	if secretName == "" {
		t.Fatal("legacy preshared key was not migrated to a secret")
	}

	currentKey := initialPresharedKey
	for rotation := 0; rotation < 32; rotation++ {
		nextKey := testPresharedKey(byte(41 + rotation%2))
		secrets.SetSecret(secretName, Secret{Value: nextKey}, "wg")

		// Keep the runtime apply pending so this test deterministically checks
		// the state visible when the secret mutation handler returns.
		wg.runtimeMu.Lock()
		delivered := make(chan struct{})
		w.Lock()
		w.SendInboxLocked(pasta.InboxMessage{
			ReceiverNode: wgID,
			Payload: SecretValueChanged{
				SecretNamePrev: secretName,
				SecretNameNew:  secretName,
			},
		})
		w.AddPendingOpLocked(func() { close(delivered) })
		w.Unlock()
		<-delivered
		snapshot, ok := w.NodeSnapshot(wgID)
		if !ok {
			wg.runtimeMu.Unlock()
			t.Fatal("WireGuard node disappeared during secret rotation")
		}
		if snapshot.Label == "up" {
			wg.runtimeMu.Unlock()
			t.Fatalf("rotation %d returned while node still reported up", rotation+1)
		}
		if wg.appliedConfigGeneration >= wg.currentConfigGeneration() {
			wg.runtimeMu.Unlock()
			t.Fatalf(
				"rotation %d applied generation=%d, desired=%d; want pending apply",
				rotation+1, wg.appliedConfigGeneration, wg.currentConfigGeneration(),
			)
		}
		oldSpec, ok := wg.dev.PeerSpec(peerPublicKey)
		oldKey, parseErr := parseWireGuardPresharedKey(currentKey)
		wg.runtimeMu.Unlock()
		if parseErr != nil || !ok || oldSpec.PresharedKey != oldKey {
			t.Fatalf("rotation %d changed the device key before the pending apply", rotation+1)
		}

		want, err := parseWireGuardPresharedKey(nextKey)
		if err != nil {
			t.Fatalf("parse rotated preshared key %d: %v", rotation+1, err)
		}
		eventually(t, func() bool {
			snapshot, ok := w.NodeSnapshot(wgID)
			if !ok || snapshot.Label != "up" {
				return false
			}
			wg.runtimeMu.Lock()
			defer wg.runtimeMu.Unlock()
			if wg.dev == nil || wg.appliedConfigGeneration != wg.currentConfigGeneration() {
				return false
			}
			spec, ok := wg.dev.PeerSpec(peerPublicKey)
			return ok && spec.PresharedKey == want
		}, "preshared-key rotation %d converged", rotation+1)
		currentKey = nextKey
	}
}

func TestWireGuardLinkChangesDoNotDropPendingConfigApply(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg, lockRuntime: true}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	if err := w.AddNodeClass(wireGuardTestTunProviderClass{silent: true}); err != nil {
		t.Fatalf("AddNodeClass TunProvider: %v", err)
	}
	wgID, err := w.AddNodeByClass("test/WireGuard", "wg")
	if err != nil {
		t.Fatalf("AddNodeByClass WireGuard: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}
	runtimeLocked := true
	t.Cleanup(func() {
		if runtimeLocked {
			wg.runtimeMu.Unlock()
		}
	})
	tunID, err := w.AddNodeByClass(wireGuardTestTunProviderNodeType, "silent-tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	tunSnapshot := testkit.MustNodeSnapshot(t, w, tunID)
	link, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	// OnInit's config job is blocked behind runtimeMu. The link add invalidates
	// its runtime generation, and the silent provider produces no attachment
	// result that could otherwise trigger convergence.
	wg.runtimeMu.Unlock()
	runtimeLocked = false
	eventually(t, func() bool {
		return wg.controllerSource.Load() != nil
	}, "link add reschedules invalidated initial config")

	// Repeat the ordering for link removal while no TUN is physically attached.
	// The detach job has no result in this state, so OnLinkRemoved must preserve
	// the pending desired config itself.
	wg.runtimeMu.Lock()
	runtimeLocked = true
	wg.configGeneration.Add(1)
	wg.scheduleConfigApply(wg.config.clone(), wg.currentRuntimeGeneration(), 0, false)
	w.RemoveLink(link)
	wg.runtimeMu.Unlock()
	runtimeLocked = false
	eventually(t, func() bool {
		wg.runtimeMu.Lock()
		defer wg.runtimeMu.Unlock()
		return wg.appliedConfigGeneration == wg.currentConfigGeneration()
	}, "link removal reschedules invalidated config without an attachment")
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
			Network: gonnect.NewLoopbackNetwork(),
		},
	}
	n.config.privateKey = privateKey
	n.config.listenPort = 51820

	if got := n.stateText(); got == "up" {
		t.Fatalf("stateText() = %q with TUN and network present but no attached bind", got)
	}
}

func TestWireGuardHotTunReplacementDoesNotNeedConfigApply(t *testing.T) {
	first, firstPeer := tun.Pipe(4, 1500, 0, 0)
	second, secondPeer := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = firstPeer.Close() })
	t.Cleanup(func() { _ = secondPeer.Close() })

	node := &wireGuardNode{config: defaultWireGuardConfig()}
	t.Cleanup(node.stopRuntime)
	node.tunGeneration.Store(1)
	needsConfig, attached, err := node.attachCurrentTun(1, first, node.config.batchSize)
	if err != nil || !attached || needsConfig {
		t.Fatalf("initial TUN attach = needsConfig %t, attached %t, error %v", needsConfig, attached, err)
	}

	node.runtimeMu.Lock()
	node.currentBind = wireGuardTrackedBind{}
	node.runtimeDeviceUp = true
	node.runtimeMu.Unlock()
	node.tunGeneration.Store(2)
	needsConfig, attached, err = node.attachCurrentTun(2, second, node.config.batchSize)
	if err != nil || !attached {
		t.Fatalf("replacement TUN attach = attached %t, error %v", attached, err)
	}
	if needsConfig {
		t.Fatal("hot TUN replacement requested an unchanged device config apply")
	}
}

func TestWireGuardTunReplacementAppliesSkippedCurrentConfig(t *testing.T) {
	first, firstPeer := tun.Pipe(4, 1500, 0, 0)
	second, secondPeer := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = firstPeer.Close() })
	t.Cleanup(func() { _ = secondPeer.Close() })

	n := &wireGuardNode{config: defaultWireGuardConfig()}
	t.Cleanup(n.stopRuntime)
	n.tunGeneration.Store(1)
	if _, attached, err := n.attachCurrentTun(1, first, n.config.batchSize); err != nil || !attached {
		t.Fatalf("initial TUN attach = attached %t, error %v", attached, err)
	}
	n.runtimeMu.Lock()
	n.currentBind = wireGuardTrackedBind{}
	n.runtimeDeviceUp = true
	n.appliedConfigGeneration = 1
	n.runtimeMu.Unlock()

	// A config apply can become stale before it takes runtimeMu when its TunConfig
	// output causes a replacement payload. The replacement must observe that the
	// latest desired config was not applied and schedule a new apply.
	n.configGeneration.Store(2)
	n.tunGeneration.Store(2)
	needsConfig, attached, err := n.attachCurrentTun(2, second, n.config.batchSize)
	if err != nil || !attached {
		t.Fatalf("replacement TUN attach = attached %t, error %v", attached, err)
	}
	if !needsConfig {
		t.Fatal("TUN replacement accepted a skipped current config as applied")
	}
}

func TestWireGuardBindRecoveryAppliesSkippedCurrentConfigWithoutTun(t *testing.T) {
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	n := &wireGuardNode{
		config:                   defaultWireGuardConfig(),
		currentNetwork:           network,
		currentNetworkGeneration: 1,
		currentBindFailed:        true,
		appliedConfigGeneration:  1,
	}
	t.Cleanup(n.stopRuntime)
	n.networkGeneration.Store(1)
	n.configGeneration.Store(2)

	needsConfig, attached, retry, err := n.attachCurrentNetworkBind(1, n.config.batchSize)
	if err != nil || !attached || retry {
		t.Fatalf("bind recovery = attached %t, retry %t, error %v", attached, retry, err)
	}
	if !needsConfig {
		t.Fatal("bind recovery stopped before applying the skipped current config")
	}
}

func TestWireGuardConfigApplyFailureInvalidatesActiveBind(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 10)
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = peerTun.Close() })
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	n := &wireGuardNode{config: defaultWireGuardConfig()}
	n.config.privateKey = privateKey
	n.config.listenPort = 51820
	t.Cleanup(n.stopRuntime)

	n.tunGeneration.Store(1)
	if _, attached, err := n.attachCurrentTun(1, sourceTun, n.config.batchSize); err != nil || !attached {
		t.Fatalf("TUN attach = attached %t, error %v", attached, err)
	}
	n.networkGeneration.Store(1)
	if _, attached, _, err := n.attachNetworkPayload(1, network, n.config.batchSize); err != nil || !attached {
		t.Fatalf("network attach = attached %t, error %v", attached, err)
	}
	if err := n.applyConfigToDevice(true); err != nil {
		t.Fatalf("initial config apply: %v", err)
	}

	network.failNextListens(1, errors.New("forced bind refresh failure"))
	if err := n.applyConfigToDevice(false); err == nil {
		t.Fatal("config apply with failed bind refresh succeeded")
	}
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if n.runtimeDeviceUp {
		t.Fatal("failed active config apply left runtimeDeviceUp true")
	}
	if !n.currentBindFailed {
		t.Fatal("failed active config apply left the unusable bind reusable")
	}
	if !n.configApplyNeededLocked() {
		t.Fatal("failed bind did not require convergence when config generations matched")
	}
	n.runtimeMu.Unlock()

	// The next config convergence must repair the failed bind itself. It must
	// not depend on a particular retry or attachment-result ordering.
	if err := n.applyConfigToDevice(false); err != nil {
		t.Fatalf("config convergence after bind failure: %v", err)
	}
	n.runtimeMu.Lock()
	if n.currentBindFailed {
		t.Fatal("config convergence did not repair the failed bind")
	}
	if !n.runtimeDeviceUp {
		t.Fatal("config convergence repaired the bind but left the device down")
	}
}

func TestWireGuardStateDoesNotReportUpWithClosedNetwork(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 7)
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })

	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
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

func TestWireGuardStateDoesNotReportUpWithClosedTun(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 8)
	baseTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = baseTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })
	closedTun := tun.Detach(baseTun, nil, nil)
	if err := closedTun.Close(); err != nil {
		t.Fatalf("TUN Close() error = %v, want nil", err)
	}
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	n := &wireGuardNode{
		config:                   defaultWireGuardConfig(),
		currentTun:               closedTun,
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

	if got := n.stateText(); got != "waiting for TUN" {
		t.Fatalf("stateText() = %q with closed TUN, want waiting for TUN", got)
	}
}

func TestWireGuardTunFailureRejectsStaleAttachmentAndInvalidatesCurrentTun(t *testing.T) {
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })
	current := links.NewTunFailureObserver(sourceTun, nil)
	stale := links.NewTunFailureObserver(sourceTun, nil)
	n := &wireGuardNode{
		config:                   defaultWireGuardConfig(),
		currentTun:               sourceTun,
		currentTunObserver:       current,
		currentTunGeneration:     3,
		tunPayloadReceived:       true,
		desiredTun:               sourceTun,
		runtimeReadyGeneration:   7,
		currentNetworkGeneration: 1,
	}
	n.tunGeneration.Store(3)
	n.runtimeGeneration.Store(7)

	n.handleTunFailure(wireGuardTunFailureMessage{
		attachment: stale, tunGeneration: 3, err: errors.New("stale failure"),
	})
	if n.currentTunFailed || n.currentTunGenerationValue() != 3 || !n.tunPayloadReceived {
		t.Fatal("stale TUN failure invalidated the current attachment")
	}
	n.handleTunFailure(wireGuardTunFailureMessage{
		attachment: current, tunGeneration: 2, err: errors.New("old generation"),
	})
	if n.currentTunFailed || n.currentTunGenerationValue() != 3 || !n.tunPayloadReceived {
		t.Fatal("old-generation TUN failure invalidated the current attachment")
	}

	n.handleTunFailure(wireGuardTunFailureMessage{
		attachment: current, tunGeneration: 3, err: errors.New("current failure"),
	})
	if !n.currentTunFailed {
		t.Fatal("current TUN failure did not mark the attachment failed")
	}
	if got := n.currentTunGenerationValue(); got != 4 {
		t.Fatalf("desired TUN generation = %d after failure, want 4", got)
	}
	if got := n.currentRuntimeGeneration(); got != 8 {
		t.Fatalf("runtime generation = %d after failure, want 8", got)
	}
	if n.tunPayloadReceived || n.desiredTun != nil || n.runtimeReadyGeneration != 0 {
		t.Fatal("current TUN failure left the old payload or readiness active")
	}
}

func TestWireGuardTryMarkRuntimeReadyRequiresActiveRuntime(t *testing.T) {
	n := &wireGuardNode{}
	n.runtimeGeneration.Store(1)

	locked, ready := n.tryMarkRuntimeReady(1)
	if !locked {
		t.Fatal("tryMarkRuntimeReady did not acquire an available runtime lock")
	}
	if ready {
		t.Fatal("tryMarkRuntimeReady reported ready without an active runtime")
	}
	if n.runtimeReadyGeneration != 0 {
		t.Fatalf("runtimeReadyGeneration = %d, want 0", n.runtimeReadyGeneration)
	}
}

func TestWireGuardFailedReopenRequiresConfigApplyAfterTunReplacement(t *testing.T) {
	source, peer := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = peer.Close() })
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	n := &wireGuardNode{config: defaultWireGuardConfig()}
	n.ensureDeviceLocked(n.config.batchSize)
	t.Cleanup(n.stopRuntime)

	// A controller owns named transports on the shared WireGuard device. A
	// graph network refresh can close its old transport before the controller
	// replaces it. Model WireGuard reopening the device during that exact
	// interval after it had previously recorded an up state.
	openErr := errors.New("controller network was replaced")
	if err := n.dev.AddTransport("test-controller", wgdevice.TransportConfig{
		Bind: &failingWireGuardTestBind{err: openErr},
	}); err != nil {
		t.Fatalf("AddTransport() error = %v", err)
	}
	n.runtimeDeviceUp = true
	if err := n.bringDeviceUpLocked(); !errors.Is(err, openErr) {
		t.Fatalf("bringDeviceUpLocked() error = %v, want %v", err, openErr)
	}
	if n.runtimeDeviceUp {
		t.Fatal("runtimeDeviceUp remained true after Device.Up failed")
	}

	// The current TUN re-emission is the next convergence event in the failing
	// startup trace. It must request a config apply so the device is reopened
	// after the controller finishes replacing its transport.
	n.currentTun = source
	n.currentTunGeneration = 1
	n.tunGeneration.Store(2)
	n.currentNetwork = network
	n.currentNetworkGeneration = 1
	n.networkGeneration.Store(1)
	n.currentBind = wireGuardTrackedBind{}
	needsConfig, ok, err := n.attachCurrentTun(2, source, n.config.batchSize)
	if err != nil {
		t.Fatalf("attachCurrentTun() error = %v", err)
	}
	if !ok {
		t.Fatal("attachCurrentTun() did not accept the current TUN generation")
	}
	if !needsConfig {
		t.Fatal("TUN replacement did not request recovery config apply after failed reopen")
	}
}

func TestWireGuardDuplicateTunPayloadDoesNotInvalidatePendingAttach(t *testing.T) {
	source, peer := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = peer.Close() })
	n := &wireGuardNode{
		config:  defaultWireGuardConfig(),
		tunp:    1,
		tunLink: 2,
	}
	t.Cleanup(n.stopRuntime)

	// Keep the first asynchronous attach pending while the provider repeats
	// its cached payload. This is the restore ordering that previously let the
	// duplicate invalidate, and later detach, the first installation.
	n.runtimeMu.Lock()
	event := pasta.Event{ReceiverPort: n.tunp, Link: n.tunLink, Payload: TunPayload{Tun: source}}
	if err := n.OnEvent(event, TypeTun, nil, "left"); err != nil {
		n.runtimeMu.Unlock()
		t.Fatalf("first OnEvent() error = %v", err)
	}
	wantTunGeneration := n.currentTunGenerationValue()
	wantRuntimeGeneration := n.currentRuntimeGeneration()
	if err := n.OnEvent(event, TypeTun, nil, "left"); err != nil {
		n.runtimeMu.Unlock()
		t.Fatalf("duplicate OnEvent() error = %v", err)
	}
	if got := n.currentTunGenerationValue(); got != wantTunGeneration {
		n.runtimeMu.Unlock()
		t.Fatalf("TUN generation after duplicate = %d, want %d", got, wantTunGeneration)
	}
	if got := n.currentRuntimeGeneration(); got != wantRuntimeGeneration {
		n.runtimeMu.Unlock()
		t.Fatalf("runtime generation after duplicate = %d, want %d", got, wantRuntimeGeneration)
	}
	n.runtimeMu.Unlock()

	eventually(t, func() bool {
		n.runtimeMu.Lock()
		defer n.runtimeMu.Unlock()
		return n.currentTun == source && n.currentTunGeneration == wantTunGeneration
	}, "first TUN payload remains current after duplicate")
}

func TestWireGuardDuplicateNetworkPayloadDoesNotInvalidatePendingAttach(t *testing.T) {
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	n := &wireGuardNode{
		config:  defaultWireGuardConfig(),
		netp:    1,
		netLink: 2,
	}
	t.Cleanup(n.stopRuntime)

	n.runtimeMu.Lock()
	event := pasta.Event{ReceiverPort: n.netp, Link: n.netLink, Payload: NetworkPayload{Network: network}}
	if err := n.OnEvent(event, TypeNetwork, nil, "right"); err != nil {
		n.runtimeMu.Unlock()
		t.Fatalf("first OnEvent() error = %v", err)
	}
	wantNetworkGeneration := n.currentNetworkGenerationValue()
	wantRuntimeGeneration := n.currentRuntimeGeneration()
	if err := n.OnEvent(event, TypeNetwork, nil, "right"); err != nil {
		n.runtimeMu.Unlock()
		t.Fatalf("duplicate OnEvent() error = %v", err)
	}
	if got := n.currentNetworkGenerationValue(); got != wantNetworkGeneration {
		n.runtimeMu.Unlock()
		t.Fatalf("network generation after duplicate = %d, want %d", got, wantNetworkGeneration)
	}
	if got := n.currentRuntimeGeneration(); got != wantRuntimeGeneration {
		n.runtimeMu.Unlock()
		t.Fatalf("runtime generation after duplicate = %d, want %d", got, wantRuntimeGeneration)
	}
	n.runtimeMu.Unlock()

	eventually(t, func() bool {
		n.runtimeMu.Lock()
		defer n.runtimeMu.Unlock()
		return n.currentNetwork == network && n.currentNetworkGeneration == wantNetworkGeneration
	}, "first network payload remains current after duplicate")
}

func TestWireGuardStaleAttachmentPayloadsAreClosed(t *testing.T) {
	baseTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = baseTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })
	staleTun := tun.Detach(baseTun, nil, nil)
	staleNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = staleTun.Close() })
	t.Cleanup(func() { _ = staleNetwork.Close() })
	n := &wireGuardNode{config: defaultWireGuardConfig()}
	n.tunGeneration.Store(2)
	n.networkGeneration.Store(2)

	if _, current, err := n.attachCurrentTun(1, staleTun, n.config.batchSize); err != nil || current {
		t.Fatalf("stale TUN attach = current %t, error %v", current, err)
	}
	if TunIsReady(staleTun) {
		t.Fatal("stale TUN payload remained ready")
	}
	if _, current, _, err := n.attachNetworkPayload(1, staleNetwork, n.config.batchSize); err != nil || current {
		t.Fatalf("stale network attach = current %t, error %v", current, err)
	}
	if NetworkIsReady(staleNetwork) {
		t.Fatal("stale network payload remained ready")
	}
}

func TestWireGuardAttachmentPayloadsDoNotCancelFailureRetry(t *testing.T) {
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	n := &wireGuardNode{
		config:          defaultWireGuardConfig(),
		tunp:            1,
		tunLink:         2,
		netp:            3,
		netLink:         4,
		retryGeneration: 7,
		retryCancel:     make(chan struct{}),
	}
	t.Cleanup(n.stopRuntime)

	// Keep both attachment jobs pending. This models startup when an attachment
	// payload reaches the workspace after a device operation fails but before
	// its failure result is handled. The failure result owns the retry, and an
	// attachment receipt must not revoke it before recovery succeeds.
	n.runtimeMu.Lock()
	for _, event := range []struct {
		value     pasta.Event
		linkType  string
		direction string
	}{
		{
			value: pasta.Event{
				ReceiverPort: n.tunp,
				Link:         n.tunLink,
				Payload:      TunPayload{Tun: sourceTun},
			},
			linkType: TypeTun, direction: "left",
		},
		{
			value: pasta.Event{
				ReceiverPort: n.netp,
				Link:         n.netLink,
				Payload:      NetworkPayload{Network: network},
			},
			linkType: TypeNetwork, direction: "right",
		},
	} {
		if err := n.OnEvent(event.value, event.linkType, nil, event.direction); err != nil {
			n.runtimeMu.Unlock()
			t.Fatalf("OnEvent(%s) error = %v", event.linkType, err)
		}
		if n.retryCancel == nil {
			n.runtimeMu.Unlock()
			t.Fatalf("OnEvent(%s) cancelled the active failure retry", event.linkType)
		}
		if n.retryGeneration != 7 {
			n.runtimeMu.Unlock()
			t.Fatalf("retryGeneration after %s payload = %d, want 7", event.linkType, n.retryGeneration)
		}
	}
	n.runtimeMu.Unlock()
}

func TestWireGuardSameSourceNetworkReemitRebuildsFailedBind(t *testing.T) {
	sourceTun, peerTun := tun.Pipe(4, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })

	base := gonnect.NewLoopbackNetwork()
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

	needsConfig, ok, retry, err := n.attachNetworkPayload(2, duplicate, n.config.batchSize)
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
	if !SameNetworkInstance(n.currentNetwork, duplicate) {
		t.Fatal("currentNetwork was not replaced with the fresh same-source wrapper")
	}
	if NetworkIsReady(current) {
		t.Fatal("replaced network wrapper remained ready")
	}
	if !NetworkIsReady(duplicate) {
		t.Fatal("fresh same-source network was closed")
	}
}

func TestWireGuardRetriesDeviceBringUpAfterNetworkListenFailure(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	network.failNextListens(2, errors.New("temporary listen failure"))
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(wireGuardTestTunProviderClass{}); err != nil {
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
	tunID, err := w.AddNodeByClass(wireGuardTestTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	networkSnapshot := testkit.MustNodeSnapshot(t, w, networkID)
	tunSnapshot := testkit.MustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "error" && network.listenCount() >= 1
	}, "wireguard initial listen failure")

	eventually(t, func() bool {
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

func TestWireGuardNetworkBindSerializesNonNativeSends(t *testing.T) {
	underlying := &blockingWireGuardTestBind{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	bind := &wireGuardSerializedSendBind{Bind: underlying}

	done := make(chan struct{}, 2)
	go func() {
		_ = bind.Send(nil, nil)
		done <- struct{}{}
	}()
	<-underlying.entered

	attempted := make(chan struct{})
	go func() {
		close(attempted)
		_ = bind.Send(nil, nil)
		done <- struct{}{}
	}()
	<-attempted
	select {
	case <-underlying.entered:
		t.Fatal("second Send entered the non-native bind concurrently")
	case <-time.After(50 * time.Millisecond):
	}

	underlying.release <- struct{}{}
	<-underlying.entered
	underlying.release <- struct{}{}
	<-done
	<-done
}

func TestWireGuardDeviceSpawnerRunsOneHandshakeConsumer(t *testing.T) {
	base := &wireGuardTestWorkerSpawner{}
	spawner := newWireGuardDeviceWorkerSpawner(base)
	handshakeEntered := make(chan struct{}, 2)
	handshakeRelease := make(chan struct{})
	var handshakeReleaseOnce sync.Once
	releaseHandshake := func() { handshakeReleaseOnce.Do(func() { close(handshakeRelease) }) }
	defer func() {
		releaseHandshake()
		base.wait()
	}()

	for i := 1; i <= 2; i++ {
		_, err := spawner.Spawn(func() {
			handshakeEntered <- struct{}{}
			<-handshakeRelease
		}, wireGuardHandshakeWorkerPrefix+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("spawn handshake worker %d: %v", i, err)
		}
	}

	<-handshakeEntered
	select {
	case <-handshakeEntered:
		t.Fatal("two handshake consumers ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}

	regularEntered := make(chan struct{}, 2)
	regularRelease := make(chan struct{})
	defer close(regularRelease)
	for i := 1; i <= 2; i++ {
		_, err := spawner.Spawn(func() {
			regularEntered <- struct{}{}
			<-regularRelease
		}, "wgo: encryption worker "+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("spawn encryption worker %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-regularEntered:
		case <-time.After(time.Second):
			t.Fatal("non-handshake workers were serialized")
		}
	}

	releaseHandshake()
	select {
	case <-handshakeEntered:
	case <-time.After(time.Second):
		t.Fatal("waiting handshake consumer did not run during shutdown")
	}
}

func TestWireGuardIntentionalClosedReceiveErrorIsIgnored(t *testing.T) {
	wg := &wireGuardNode{}
	dev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
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

func TestWireGuardUnexpectedReceiveErrorFromOldOpenIsIgnored(t *testing.T) {
	dev := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
	t.Cleanup(dev.Close)
	openGeneration := &atomic.Uint64{}
	openGeneration.Store(2)
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	sourceTun, peerTun := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() { _ = sourceTun.Close() })
	t.Cleanup(func() { _ = peerTun.Close() })

	n := &wireGuardNode{
		dev:                      dev,
		currentTun:               sourceTun,
		currentTunGeneration:     1,
		currentNetwork:           network,
		currentNetworkGeneration: 1,
		currentBind:              wireGuardTrackedBind{},
		currentBindGen:           4,
		currentBindOpen:          openGeneration,
		runtimeDeviceUp:          true,
	}
	n.runtimeGeneration.Store(1)
	n.tunGeneration.Store(1)
	n.networkGeneration.Store(1)

	n.handleReceiveError(wireGuardReceiveErrorMessage{
		dev:                dev,
		generation:         1,
		bindGeneration:     4,
		bindOpenGeneration: 1,
		err:                io.EOF,
	})

	if n.runtimeError != "" {
		t.Fatalf("runtimeError = %q after stale receive error, want empty", n.runtimeError)
	}
	if n.currentBindFailed {
		t.Fatal("stale receive error marked the current bind failed")
	}
	if got := n.currentRuntimeGeneration(); got != 1 {
		t.Fatalf("runtime generation = %d after stale receive error, want 1", got)
	}
}

func TestWireGuardStaleAttachmentSuccessDoesNotCancelFailureRetry(t *testing.T) {
	n := &wireGuardNode{
		currentBindFailed: true,
		runtimeError:      "receive from bind: EOF",
		retryCancel:       make(chan struct{}),
	}
	n.runtimeFailureGeneration.Store(1)

	n.handleRuntimeAttachmentResult(wireGuardRuntimeAttachmentResultMessage{
		failureGeneration: 0,
	})

	if n.runtimeError == "" {
		t.Fatal("stale attachment success cleared the current runtime error")
	}
	if n.retryCancel == nil {
		t.Fatal("stale attachment success cancelled the current retry")
	}
	if got := n.currentRuntimeFailureGeneration(); got != 1 {
		t.Fatalf("runtime failure generation = %d, want 1", got)
	}
}

func TestWireGuardRetriesBindAfterReceiveEOF(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(wireGuardTestTunProviderClass{}); err != nil {
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
	tunID, err := w.AddNodeByClass(wireGuardTestTunProviderNodeType, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass TunProvider: %v", err)
	}
	if wg == nil {
		t.Fatal("captured WireGuard node = nil")
	}

	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	networkSnapshot := testkit.MustNodeSnapshot(t, w, networkID)
	tunSnapshot := testkit.MustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "up" && network.listenCount() >= 1
	}, "wireguard initial listen")

	injectWireGuardReceiveEOF(t, wg)

	eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(wgID)
		return ok && snapshot.Label == "up" && network.listenCount() >= 2
	}, "wireguard retry after receive EOF")
}

func TestWireGuardReceiveEOFRebuildsBindBeforePublishingUp(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })

	var wg *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wg}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{network: network}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	if err := w.AddNodeClass(wireGuardTestTunProviderClass{}); err != nil {
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
	tunID, err := w.AddNodeByClass(wireGuardTestTunProviderNodeType, "tun")
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

	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	networkSnapshot := testkit.MustNodeSnapshot(t, w, networkID)
	tunSnapshot := testkit.MustNodeSnapshot(t, w, tunID)

	if _, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(wgSnapshot.LeftPorts[0], tunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	eventually(t, func() bool {
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

func assertWireGuardStatusItemValue(t *testing.T, block formular.Block, id string, want any) {
	t.Helper()
	item, ok := findWireGuardTestItem(block, id)
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

func findWireGuardTestItem(block formular.Block, id string) (formular.Item, bool) {
	for _, item := range block.Items {
		if item.ID == id {
			return item, true
		}
	}
	return formular.Item{}, false
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

func injectWireGuardReceiveEOF(t *testing.T, wg *wireGuardNode) {
	t.Helper()
	var dev *wgdevice.Device
	var bindGeneration uint64
	var bindOpenGeneration uint64
	eventually(t, func() bool {
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

type captureWireGuardClass struct {
	node        **wireGuardNode
	secrets     SecretsStorage
	lockRuntime bool
}

func (captureWireGuardClass) ClassName() string        { return "test/WireGuard" }
func (captureWireGuardClass) ShortDescription() string { return WireGuardClass{}.ShortDescription() }
func (captureWireGuardClass) LongDescription() string  { return WireGuardClass{}.LongDescription() }
func (captureWireGuardClass) DefaultNodeParams() pasta.NodeClassParams {
	return WireGuardClass{}.DefaultNodeParams()
}
func (c captureWireGuardClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultWireGuardConfig()
	config.read(cfg)
	secrets := c.secrets
	if secrets == nil {
		secrets = newWireGuardTestSecretsStorage()
	}
	node := &wireGuardNode{config: config, secrets: secrets}
	if c.lockRuntime {
		node.runtimeMu.Lock()
	}
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
	network  NetworkCloser
	provider links.DatagramMTUProvider
	node     **wireGuardTestNetworkSourceNode
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
	node := &wireGuardTestNetworkSourceNode{network: c.network, provider: c.provider}
	if c.node != nil {
		*c.node = node
	}
	return node, nil
}

type wireGuardTestNetworkSourceNode struct {
	pasta.BasicNode

	w        *pasta.Workspace
	id       uint64
	out      uint64
	network  NetworkCloser
	provider links.DatagramMTUProvider
}

func (n *wireGuardTestNetworkSourceNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.out = restoredLeft(restored, 0)
	if err := n.w.SetNodePrimaryLocked(n.id, TypeNetwork); err != nil {
		return err
	}
	return n.w.SetNodeLabelLocked(n.id, "ready")
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
	snapshot, ok := n.w.PortSnapshotLocked(n.out)
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
	links.AttachDatagramMTUProvider(n.network, n.provider)
	BindNetworkResource(n.w, n.id, link, n.network)
	payload := NetworkPayload{Network: n.network, DatagramMTU: n.provider}
	if n.provider != nil {
		payload.CapabilityRevision = n.provider.Revision()
	}
	n.w.EmitEventLocked(n.id, link, payload)
}

const wireGuardTestTunProviderNodeType = "test/WireGuardTunProvider"

type wireGuardTestTunProviderClass struct {
	silent bool
}

func (wireGuardTestTunProviderClass) ClassName() string        { return wireGuardTestTunProviderNodeType }
func (wireGuardTestTunProviderClass) ShortDescription() string { return "test TUN provider" }
func (wireGuardTestTunProviderClass) LongDescription() string {
	return "Test-only right-directed almagest/tun source."
}
func (wireGuardTestTunProviderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c wireGuardTestTunProviderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	source, peer := tun.Pipe(4, 1500, 0, 0)
	return &wireGuardTestTunProviderNode{tun: source, peer: peer, silent: c.silent}, nil
}

type wireGuardTestTunProviderNode struct {
	pasta.BasicNode

	w      *pasta.Workspace
	id     uint64
	out    uint64
	tun    tun.Tun
	peer   tun.Tun
	silent bool
}

func (n *wireGuardTestTunProviderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.out = restoredRight(restored, 0)
	if err := n.w.SetNodePrimaryLocked(n.id, TypeTun); err != nil {
		return err
	}
	return n.w.SetNodeLabelLocked(n.id, "ready")
}

func (n *wireGuardTestTunProviderNode) OnReady() error {
	if !n.silent {
		n.sendAll()
	}
	return nil
}

func (n *wireGuardTestTunProviderNode) OnStop() {
	_ = n.tun.Close()
	_ = n.peer.Close()
}

func (n *wireGuardTestTunProviderNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.w, port, linkType)
}

func (n *wireGuardTestTunProviderNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if !n.silent && port == n.out && portDirection == "right" && linkType == TypeTun {
		n.sendToLink(link)
	}
	return nil
}

func (n *wireGuardTestTunProviderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if !n.silent && event.ReceiverPort == n.out && receiverPortDirection == "right" && linkType == TypeTun && std.IsRequest(event.Payload) {
		n.sendToLink(event.Link)
	}
	return nil
}

func (n *wireGuardTestTunProviderNode) sendAll() {
	snapshot, ok := n.w.PortSnapshotLocked(n.out)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		n.sendToLink(link)
	}
}

func (n *wireGuardTestTunProviderNode) sendToLink(link uint64) {
	if n.tun == nil {
		return
	}
	BindTunResource(n.w, n.id, link, n.tun)
	n.w.EmitEventLocked(n.id, link, TunPayload{Tun: n.tun})
}

type wireGuardTestSecretsStorage struct {
	mu      sync.Mutex
	nextKey int
	records map[string]wireGuardTestSecretRecord
}

type wireGuardTestSecretRecord struct {
	secret Secret
	nodes  map[string]struct{}
}

func newWireGuardTestSecretsStorage() *wireGuardTestSecretsStorage {
	return &wireGuardTestSecretsStorage{records: map[string]wireGuardTestSecretRecord{}}
}

func (s *wireGuardTestSecretsStorage) GetSecret(key string, node string) (Secret, error) {
	if s == nil || key == "" || node == "" {
		return Secret{}, errors.New("secret access denied")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return Secret{}, errors.New("secret access denied")
	}
	if _, ok := record.nodes[node]; !ok {
		return Secret{}, errors.New("secret access denied")
	}
	return record.secret, nil
}

func (s *wireGuardTestSecretsStorage) NewSecret(sec Secret, node string) (string, error) {
	if s == nil {
		return "", errors.New("secret storage closed")
	}
	if node == "" {
		return "", ErrSecretInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		s.nextKey++
		key := "secret-" + strconv.Itoa(s.nextKey)
		if _, exists := s.records[key]; exists {
			continue
		}
		s.records[key] = wireGuardTestSecretRecord{
			secret: Secret{Value: sec.Value},
			nodes:  map[string]struct{}{node: {}},
		}
		return key, nil
	}
}

func (s *wireGuardTestSecretsStorage) SetSecret(key string, sec Secret, nodes ...string) {
	if s == nil {
		return
	}
	allowed := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node != "" {
			allowed[node] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[key] = wireGuardTestSecretRecord{secret: sec, nodes: allowed}
}

func (s *wireGuardTestSecretsStorage) SecretValue(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return "", false
	}
	return record.secret.Value, true
}

func eventually(t *testing.T, fn func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last bool
	for time.Now().Before(deadline) {
		last = fn()
		if last {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition failed: "+format, args...)
}

type wireGuardTestLogFactory struct{}

func (wireGuardTestLogFactory) WorkspaceLogger() pasta.Logger {
	return wireGuardTestLogger{}
}

func (wireGuardTestLogFactory) NodeLogger(uint64, string) pasta.Logger {
	return wireGuardTestLogger{}
}

type wireGuardTestLogger struct{}

func (wireGuardTestLogger) Debug(...any)          {}
func (wireGuardTestLogger) Debugf(string, ...any) {}
func (wireGuardTestLogger) Info(...any)           {}
func (wireGuardTestLogger) Infof(string, ...any)  {}
func (wireGuardTestLogger) Warn(...any)           {}
func (wireGuardTestLogger) Warnf(string, ...any)  {}
func (wireGuardTestLogger) Err(...any)            {}
func (wireGuardTestLogger) Errf(string, ...any)   {}
func (wireGuardTestLogger) Fatal(...any)          {}
func (wireGuardTestLogger) Fatalf(string, ...any) {}

type blockingWireGuardTestBind struct {
	entered chan struct{}
	release chan struct{}
}

type wireGuardTestWorkerSpawner struct {
	mu sync.Mutex
	wg sync.WaitGroup
}

func (s *wireGuardTestWorkerSpawner) Spawn(worker func(), _ string) (uint64, error) {
	s.mu.Lock()
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		worker()
	}()
	return 0, nil
}

func (s *wireGuardTestWorkerSpawner) SpawnWg(worker func(), wg *sync.WaitGroup, name string) (uint64, error) {
	if wg == nil {
		return 0, errors.New("wait group is nil")
	}
	wg.Add(1)
	return s.Spawn(func() {
		defer wg.Done()
		worker()
	}, name)
}

func (s *wireGuardTestWorkerSpawner) wait() {
	s.wg.Wait()
}

type failingWireGuardTestBind struct {
	err error
}

func (b *failingWireGuardTestBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	return nil, 0, b.err
}

func (*failingWireGuardTestBind) Close() error         { return nil }
func (*failingWireGuardTestBind) SetMark(uint32) error { return nil }
func (*failingWireGuardTestBind) BatchSize() int       { return 1 }
func (*failingWireGuardTestBind) Send([][]byte, conn.Endpoint) error {
	return nil
}
func (*failingWireGuardTestBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return nil, nil
}

func (*blockingWireGuardTestBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	return nil, 0, nil
}

func (*blockingWireGuardTestBind) Close() error         { return nil }
func (*blockingWireGuardTestBind) SetMark(uint32) error { return nil }
func (*blockingWireGuardTestBind) BatchSize() int       { return 1 }
func (*blockingWireGuardTestBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return nil, nil
}

func (b *blockingWireGuardTestBind) Send([][]byte, conn.Endpoint) error {
	b.entered <- struct{}{}
	<-b.release
	return nil
}
