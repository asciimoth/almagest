package wireguardnodes

import (
	"errors"
	"testing"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
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

func TestWGConfigPendingReconcileDoesNotReportUp(t *testing.T) {
	n := &wgConfigNode{
		ctrlLink:            1,
		netLink:             2,
		runtimeWGReady:      true,
		runtimeNetworkReady: true,
		reconcilePending:    true,
	}
	if got := n.stateText(); got != "starting" {
		t.Fatalf("stateText() = %q during reconcile, want starting", got)
	}
}

func TestWGConfigNetworkReplacementClosesOldRuntimeWrapper(t *testing.T) {
	device := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{
		BatchSize: conn.IdealBatchSize,
	})
	t.Cleanup(device.Close)
	controller := wgdevice.DetachDevice(device)
	t.Cleanup(controller.Close)
	first := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	second := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = second.Close() })
	n := &wgConfigNode{NodeBase: NodeBase{ID: 43}}

	n.generation.Store(1)
	if _, _, _, err, ok := n.reconcile(1, controller, first, nil); err != nil || !ok {
		t.Fatalf("first reconcile = error %v, current %t", err, ok)
	}
	n.generation.Store(2)
	if _, _, _, err, ok := n.reconcile(2, controller, second, nil); err != nil || !ok {
		t.Fatalf("replacement reconcile = error %v, current %t", err, ok)
	}
	if NetworkIsReady(first) {
		t.Fatal("network replacement left the old runtime wrapper ready")
	}
	if !NetworkIsReady(second) {
		t.Fatal("network replacement closed the current runtime wrapper")
	}
}

func TestWGConfigRuntimeFailureRetriesUntilSuccess(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })
	var node *wgConfigNode
	if err := w.AddNodeClass(captureWGConfigClass{node: &node}); err != nil {
		t.Fatalf("AddNodeClass WGConfig: %v", err)
	}
	id, err := w.AddNodeByClass("test/WGConfig", "wg-config")
	if err != nil {
		t.Fatalf("AddNodeByClass WGConfig: %v", err)
	}
	if node == nil {
		t.Fatal("captured WGConfig node = nil")
	}

	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return !node.reconcilePending
	}, "initial WGConfig reconcile")
	generation := node.generation.Load()
	w.SendInbox(pasta.InboxMessage{ReceiverNode: id, Payload: wgConfigRuntimeResult{
		generation: generation,
		err:        errors.New("forced reconcile failure"),
	}})
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return node.runtimeError == "forced reconcile failure"
	}, "WGConfig records reconcile failure")
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return node.generation.Load() > generation && !node.reconcilePending && node.runtimeError == ""
	}, "WGConfig retries and clears reconcile failure")
}

func TestWGConfigPartialPeerApplyRemainsOwnedForRemoval(t *testing.T) {
	device := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{
		BatchSize: conn.IdealBatchSize,
	})
	t.Cleanup(device.Close)
	controller := wgdevice.DetachDevice(device)
	t.Cleanup(controller.Close)
	_, publicKeyText := wireGuardTestKeyPair(t, 185)
	publicKey, err := parseWireGuardPublicKey(publicKeyText)
	if err != nil {
		t.Fatalf("parse peer key: %v", err)
	}
	failing := &failAfterWGConfigPeerApply{
		WGCtrlAPI: controller,
		failKey:   publicKey,
		fail:      true,
	}
	n := &wgConfigNode{runtimeController: failing}
	peer := wireGuardPeerConfig{id: "partial", publicKey: publicKeyText}

	if err := n.syncRuntimePeersLocked([]wireGuardPeerConfig{peer}, false); err == nil {
		t.Fatal("partial peer apply succeeded")
	}
	if _, found := device.PeerSpec(publicKey); !found {
		t.Fatal("failure injection did not leave the partially applied peer")
	}
	if _, owned := n.runtimePeerKeys[publicKey]; !owned {
		t.Fatal("partially applied peer was not retained in the ownership set")
	}

	failing.fail = false
	if err := n.syncRuntimePeersLocked(nil, false); err != nil {
		t.Fatalf("remove partial peer: %v", err)
	}
	if _, found := device.PeerSpec(publicKey); found {
		t.Fatal("later empty config did not remove the partially applied peer")
	}
}

type failAfterWGConfigPeerApply struct {
	WGCtrlAPI

	failKey wgdevice.NoisePublicKey
	fail    bool
}

func (c *failAfterWGConfigPeerApply) UpsertTrackedPeer(spec wgdevice.PeerSpec) error {
	if err := c.WGCtrlAPI.UpsertTrackedPeer(spec); err != nil {
		return err
	}
	if c.fail && spec.PublicKey.Equals(c.failKey) {
		return errors.New("forced error after peer apply")
	}
	return nil
}

type captureWGConfigClass struct {
	node **wgConfigNode
}

func (captureWGConfigClass) ClassName() string        { return "test/WGConfig" }
func (captureWGConfigClass) ShortDescription() string { return WGConfigClass{}.ShortDescription() }
func (captureWGConfigClass) LongDescription() string  { return WGConfigClass{}.LongDescription() }
func (captureWGConfigClass) DefaultNodeParams() pasta.NodeClassParams {
	return WGConfigClass{}.DefaultNodeParams()
}
func (c captureWGConfigClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	raw, err := (WGConfigClass{}).NewNode(cfg)
	if err != nil {
		return nil, err
	}
	node := raw.(*wgConfigNode)
	if c.node != nil {
		*c.node = node
	}
	return node, nil
}

func TestWireGuardLocalPeerRebuildPreservesControllerPeers(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 182)
	_, localKeyText := wireGuardTestKeyPair(t, 183)
	localKey, err := parseWireGuardPublicKey(localKeyText)
	if err != nil {
		t.Fatalf("parse local key: %v", err)
	}
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

	// All scheduled convergence paths must rebuild node-owned peers. A runtime
	// generation change can make the original peer-removal job stale. Its
	// attachment or retry replacement must still delete the removed peer without
	// deleting peers that a controller owns through its detached API.
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })
	node.InitBaseNode(w, nil, 991)
	node.config.peers = nil
	node.configGeneration.Store(2)
	node.runtimeGeneration.Store(3)
	node.scheduleConfigApply(node.config.clone(), 3, 0, false)
	eventually(t, func() bool {
		_, found := node.dev.PeerSpec(localKey)
		return !found
	}, "scheduled local peer removal")
	if _, found := node.dev.PeerSpec(controllerKey); !found {
		t.Fatal("local peer rebuild deleted the controller-owned peer")
	}
}
