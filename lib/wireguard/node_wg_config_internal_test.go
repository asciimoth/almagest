package wireguardnodes

import (
	"testing"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/gonnect"
	wgdevice "github.com/asciimoth/wgo/device"
)

func TestWGConfigRuntimeUsesNamedTransportAndDropsWithoutNetwork(t *testing.T) {
	device := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{
		BatchSize: conn.IdealBatchSize,
	})
	t.Cleanup(device.Close)
	controller := wgdevice.DetachDevice(device)
	t.Cleanup(controller.Close)
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	_, publicKeyText := wireGuardTestKeyPair(t, 181)
	publicKey, err := parseWireGuardPublicKey(publicKeyText)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	peers := []wireGuardPeerConfig{{
		id:                "owned",
		publicKey:         publicKeyText,
		endpoint:          "127.0.0.1:51820",
		allowedIPs:        "10.181.0.1/32",
		amneziaWGOverride: true,
		amneziaWGEnabled:  true,
		amneziaWG:         wireGuardTestAmneziaConfig(),
	}}
	node := &wgConfigNode{NodeBase: NodeBase{ID: 42}}

	node.generation.Store(1)
	wgReady, networkReady, count, err, ok := node.reconcile(1, controller, network, peers)
	if err != nil || !ok || !wgReady || !networkReady || count != 1 {
		t.Fatalf("attached reconcile = (%t, %t, %d, %v, %t)", wgReady, networkReady, count, err, ok)
	}
	transportID := WGConfigTransportID(node.ID)
	if want := wgdevice.TransportID("almagest/WGConfig/42/udp"); transportID != want {
		t.Fatalf("transport ID = %q, want %q", transportID, want)
	}
	if _, found := device.TransportInfo(transportID); !found {
		t.Fatalf("named transport %q is missing", transportID)
	}
	spec, found := device.PeerSpec(publicKey)
	if !found || spec.Endpoint == nil || spec.Endpoint.Transport != transportID || spec.Endpoint.Address != peers[0].endpoint {
		t.Fatalf("attached peer endpoint = %#v, want transport %q address %q", spec.Endpoint, transportID, peers[0].endpoint)
	}
	if spec.AmneziaWG == nil || spec.AmneziaWG.InitHeader.Spec() != peers[0].amneziaWG.initHeader {
		t.Fatalf("attached peer AWG profile = %#v, want enabled WGConfig profile", spec.AmneziaWG)
	}

	node.generation.Store(2)
	wgReady, networkReady, count, err, ok = node.reconcile(2, controller, nil, peers)
	if err != nil || !ok || !wgReady || networkReady || count != 1 {
		t.Fatalf("detached reconcile = (%t, %t, %d, %v, %t)", wgReady, networkReady, count, err, ok)
	}
	if _, found := device.TransportInfo(transportID); found {
		t.Fatalf("named transport %q remains while network is detached", transportID)
	}
	spec, found = device.PeerSpec(publicKey)
	if !found || spec.Endpoint != nil {
		t.Fatalf("detached peer endpoint = %#v, want nil drop path", spec.Endpoint)
	}

	node.generation.Store(3)
	wgReady, networkReady, count, err, ok = node.reconcile(3, controller, network, peers)
	if err != nil || !ok || !wgReady || !networkReady || count != 1 {
		t.Fatalf("reattached reconcile = (%t, %t, %d, %v, %t)", wgReady, networkReady, count, err, ok)
	}
	spec, found = device.PeerSpec(publicKey)
	if !found || spec.Endpoint == nil || spec.Endpoint.Transport != transportID {
		t.Fatalf("reattached peer endpoint = %#v, want transport %q", spec.Endpoint, transportID)
	}

	// A disabled peer-local profile must remain an explicit default override on
	// the detached controller path. A nil override would inherit the provider's
	// device-level profile instead.
	peers[0].amneziaWGEnabled = false
	peers[0].amneziaWG.initHeader = "invalid-hidden-value"
	node.generation.Store(4)
	wgReady, networkReady, count, err, ok = node.reconcile(4, controller, network, peers)
	if err != nil || !ok || !wgReady || !networkReady || count != 1 {
		t.Fatalf("disabled AWG reconcile = (%t, %t, %d, %v, %t)", wgReady, networkReady, count, err, ok)
	}
	spec, found = device.PeerSpec(publicKey)
	if !found || spec.AmneziaWG == nil || *spec.AmneziaWG != wgdevice.DefaultAmneziaWGConfig() {
		t.Fatalf("disabled WGConfig peer AWG profile = %#v, want explicit defaults", spec.AmneziaWG)
	}
}

func TestWireGuardLocalPeerRebuildPreservesControllerPeers(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 182)
	_, localKeyText := wireGuardTestKeyPair(t, 183)
	_, controllerKeyText := wireGuardTestKeyPair(t, 184)
	controllerKey, err := parseWireGuardPublicKey(controllerKeyText)
	if err != nil {
		t.Fatalf("parse controller key: %v", err)
	}
	node := &wireGuardNode{config: wireGuardConfig{
		privateKey: privateKey,
		amneziaWG:  defaultWireGuardAmneziaConfig(),
		peers:      []wireGuardPeerConfig{{id: "local", publicKey: localKeyText}},
	}}
	node.config.normalize()
	if err := node.applyConfigToDevice(true); err != nil {
		t.Fatalf("initial local apply: %v", err)
	}
	t.Cleanup(node.stopRuntime)
	controller := wgdevice.DetachDevice(node.dev)
	t.Cleanup(controller.Close)
	if err := controller.UpsertTrackedPeer(wgdevice.PeerSpec{
		PublicKey: controllerKey, ProtocolVersion: 1,
	}); err != nil {
		t.Fatalf("controller peer apply: %v", err)
	}

	node.config.peers = nil
	if err := node.applyConfigToDevice(true); err != nil {
		t.Fatalf("empty local rebuild: %v", err)
	}
	if _, found := node.dev.PeerSpec(controllerKey); !found {
		t.Fatal("local peer rebuild deleted the controller-owned peer")
	}
}
