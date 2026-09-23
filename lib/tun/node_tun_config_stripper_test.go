package tunnodes

import (
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestTunConfigStripperForwardsUsableTunPayloadAndRequestsInput(t *testing.T) {
	w := newTunConfigStripperTestWorkspace(t)
	provider := &tunEventRecorderNode{requests: make(chan struct{}, 4), configs: make(chan TunConfig, 4)}
	providerID, err := w.AddNode(provider, "test/TunEventRecorder", "provider")
	if err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	stripper, err := w.AddNodeByClass(NodeTypeTunConfigStripper, "stripper")
	if err != nil {
		t.Fatalf("AddNodeByClass TunConfigStripper: %v", err)
	}
	consumer := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	if _, err := w.AddNode(consumer, "test/TunReceiver", "consumer"); err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	stripperSnapshot := testkit.MustNodeSnapshot(t, w, stripper)
	inputLink, _, err := w.AddLink(stripperSnapshot.LeftPorts[0], provider.out)
	if err != nil {
		t.Fatalf("AddLink input: %v", err)
	}
	if _, _, err := w.AddLink(consumer.in, stripperSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink output: %v", err)
	}
	assertTunRequestReceived(t, provider.requests)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	w.EmitEvent(providerID, inputLink, TunPayload{Tun: base})
	payload := receiveTunPayload(t, consumer.events)
	if SameTunInstance(payload.Tun, base) {
		t.Fatal("forwarded TunPayload reused the input Tun instance, want detached output wrapper")
	}

	w.EmitEvent(providerID, inputLink, TunPayload{})
	receiveUnavailableTunPayload(t, consumer.events)

	replacement, replacementPeer := tun.Pipe(1, 1400, 0, 0)
	defer closeTestTun(replacement)
	defer closeTestTun(replacementPeer)
	w.EmitEvent(providerID, inputLink, TunPayload{Tun: replacement})
	recovered := receiveTunPayload(t, consumer.events)
	if SameTunInstance(payload.Tun, recovered.Tun) {
		t.Fatal("recovery reused the invalidated output TUN wrapper")
	}
	if mtu, err := recovered.Tun.MTU(); err != nil || mtu != 1400 {
		t.Fatalf("recovered TUN MTU() = %d, %v; want 1400, nil", mtu, err)
	}
}

func TestTunConfigStripperDropsTunConfigFromOutput(t *testing.T) {
	w := newTunConfigStripperTestWorkspace(t)
	provider := &tunEventRecorderNode{requests: make(chan struct{}, 4), configs: make(chan TunConfig, 4)}
	if _, err := w.AddNode(provider, "test/TunEventRecorder", "provider"); err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	stripper, err := w.AddNodeByClass(NodeTypeTunConfigStripper, "stripper")
	if err != nil {
		t.Fatalf("AddNodeByClass TunConfigStripper: %v", err)
	}
	consumer := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	consumerID, err := w.AddNode(consumer, "test/TunReceiver", "consumer")
	if err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	stripperSnapshot := testkit.MustNodeSnapshot(t, w, stripper)
	if _, _, err := w.AddLink(stripperSnapshot.LeftPorts[0], provider.out); err != nil {
		t.Fatalf("AddLink input: %v", err)
	}
	discardTunRequest(t, provider.requests)
	outputLink, _, err := w.AddLink(consumer.in, stripperSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink output: %v", err)
	}
	discardTunRequest(t, provider.requests)

	w.EmitEvent(consumerID, outputLink, TunConfig{
		Addrs:  []string{"10.0.0.2/32"},
		Routes: []string{"10.10.0.0/24"},
		MTU:    1400,
		DNS:    "8.8.8.8",
	})
	std.Request(w, consumerID, outputLink)
	assertTunRequestReceived(t, provider.requests)
	assertNoTunConfigReceived(t, provider.configs)
}

func TestTunConfigStripperDropsTunConfigFromInput(t *testing.T) {
	w := newTunConfigStripperTestWorkspace(t)
	provider := &tunEventRecorderNode{requests: make(chan struct{}, 4), configs: make(chan TunConfig, 4)}
	providerID, err := w.AddNode(provider, "test/TunEventRecorder", "provider")
	if err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	stripper, err := w.AddNodeByClass(NodeTypeTunConfigStripper, "stripper")
	if err != nil {
		t.Fatalf("AddNodeByClass TunConfigStripper: %v", err)
	}
	consumer := &tunEventRecorderNode{requests: make(chan struct{}, 4), configs: make(chan TunConfig, 4)}
	if _, err := w.AddNode(consumer, "test/TunEventRecorder", "consumer"); err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	stripperSnapshot := testkit.MustNodeSnapshot(t, w, stripper)
	inputLink, _, err := w.AddLink(stripperSnapshot.LeftPorts[0], provider.out)
	if err != nil {
		t.Fatalf("AddLink input: %v", err)
	}
	if _, _, err := w.AddLink(consumer.in, stripperSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink output: %v", err)
	}

	w.EmitEvent(providerID, inputLink, TunConfig{
		Addrs:  []string{"fd00::2/128"},
		Routes: []string{"fd00:10::/64"},
		MTU:    1280,
		DNS:    "2001:4860:4860::8888",
	})
	w.EmitEvent(providerID, inputLink, std.RequestValue{})
	assertTunRequestReceived(t, consumer.requests)
	assertNoTunConfigReceived(t, consumer.configs)
}

func newTunConfigStripperTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(TunConfigStripperClass{}); err != nil {
		t.Fatalf("AddNodeClass TunConfigStripper: %v", err)
	}
	return w
}

type tunEventRecorderNode struct {
	pasta.BasicNode
	in       uint64
	out      uint64
	requests chan struct{}
	configs  chan TunConfig
}

func (n *tunEventRecorderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	in, err := w.AddPortLocked(pasta.Port{Node: id, Direction: "left", Name: "TunIn", Types: []string{TypeTun}})
	if err != nil {
		return err
	}
	out, err := w.AddPortLocked(pasta.Port{Node: id, Direction: "right", Name: "TunOut", Types: []string{TypeTun}})
	if err != nil {
		return err
	}
	n.in = in
	n.out = out
	return nil
}

func (n *tunEventRecorderNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" {
		return TunPreLinkAdd(nil, 0, linkType)
	}
	if port == n.out && portDirection == "right" {
		return TunPreLinkAdd(nil, 0, linkType)
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *tunEventRecorderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	if std.IsRequest(event.Payload) {
		n.requests <- struct{}{}
		return nil
	}
	if cfg, ok := event.Payload.(TunConfig); ok {
		n.configs <- cfg
	}
	return nil
}

func discardTunRequest(t *testing.T, requests <-chan struct{}) {
	t.Helper()
	select {
	case <-requests:
	default:
	}
}

func assertTunRequestReceived(t *testing.T, requests <-chan struct{}) {
	t.Helper()
	select {
	case <-requests:
	default:
		t.Fatal("no TUN request received")
	}
}

func assertNoTunConfigReceived(t *testing.T, configs <-chan TunConfig) {
	t.Helper()
	select {
	case got := <-configs:
		t.Fatalf("received TunConfig %#v, want none", got)
	default:
	}
}

type tunPayloadReceiver struct {
	pasta.BasicNode
	in     uint64
	events chan TunPayload
}

func (n *tunPayloadReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
		Node:      id,
		Direction: "left",
		Name:      "Tun",
		Types:     []string{TypeTun},
	})
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *tunPayloadReceiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "left" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

func (n *tunPayloadReceiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "left" || linkType != TypeTun {
		return nil
	}
	payload, ok := event.Payload.(TunPayload)
	if ok {
		n.events <- payload
	}
	return nil
}

func receiveTunPayload(t *testing.T, events <-chan TunPayload) TunPayload {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var latest TunPayload
	for {
		select {
		case payload := <-events:
			latest = payload
		drain:
			for {
				select {
				case payload = <-events:
					latest = payload
				default:
					break drain
				}
			}
			if latest.Tun != nil {
				return latest
			}
		case <-timer.C:
			t.Fatal("timed out waiting for TUN payload")
			return TunPayload{}
		}
	}
}

func receiveUnavailableTunPayload(t *testing.T, events <-chan TunPayload) {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Tun != nil {
			t.Fatalf("unavailable payload Tun = %T, want nil", payload.Tun)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unavailable TUN payload")
	}
}

func closeTestTun(tun tun.Tun) {
	_ = tun.Close()
}
