package networknodes

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

var errRemapperCapture = errors.New("remapper capture")

func TestRemapperBytecodeRulesApplyMatchAndMethodFilters(t *testing.T) {
	rules, err := routing.NewRemapRules(`
# Only Dial to service.test:80 is rewritten.
DIAL
ADDR_S service.test
AND
PORT 80
AND
REMAP DST ADDR_PORT 127.0.0.1:8080

LISTEN
REMAP SRC ADDR 127.0.0.1
	`)
	if err != nil {
		t.Fatalf("NewRemapRules() error = %v, want nil", err)
	}
	wrapped := newRemapperCaptureNetwork()
	remapper := gonnect.NewRemapper(wrapped, rules)

	if _, err := remapper.Dial(context.Background(), "tcp6", "service.test:80"); !errors.Is(err, errRemapperCapture) {
		t.Fatalf("Dial() error = %v, want capture", err)
	}
	if got, want := wrapped.call("Dial"), []string{"tcp4", "127.0.0.1:8080"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Dial args = %v, want %v", got, want)
	}

	if _, err := remapper.Listen(context.Background(), "tcp6", "[::1]:0"); !errors.Is(err, errRemapperCapture) {
		t.Fatalf("Listen() error = %v, want capture", err)
	}
	if got, want := wrapped.call("Listen"), []string{"tcp4", "127.0.0.1:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Listen args = %v, want %v", got, want)
	}
}

func TestRemapperBytecodeRulesReportErrors(t *testing.T) {
	tests := []string{
		"REMAP",
		"BOGUS\nREMAP DST PORT 8080",
		"TRUE\nREMAP DST ADDR_PORT 127.0.0.1",
		"TRUE\nREMAP DST PORT",
		"TRUE\nDROP",
	}
	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			if _, err := routing.NewRemapRules(text); err == nil {
				t.Fatalf("NewRemapRules(%q) error = nil, want error", text)
			}
		})
	}
}

func TestRemapperNodeEmitsGonnectRemapperPayload(t *testing.T) {
	w := newRemapperTestWorkspace(t)
	source, err := w.AddNodeByClass(remapperTestNetworkSourceNodeType, "source")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkSource: %v", err)
	}
	remapper, err := w.AddNodeByClass(NodeTypeRemapper, "remapper")
	if err != nil {
		t.Fatalf("AddNodeByClass Remapper: %v", err)
	}
	receiver := networktest.NewReceiver(2)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	sourceNode := testkit.MustNodeSnapshot(t, w, source)
	remapperNode := testkit.MustNodeSnapshot(t, w, remapper)
	if _, _, err := w.AddLink(sourceNode.LeftPorts[0], remapperNode.RightPorts[0]); err != nil {
		t.Fatalf("AddLink source->remapper: %v", err)
	}
	if _, _, err := w.AddLink(remapperNode.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink remapper->receiver: %v", err)
	}

	payload := networktest.ReceivePayload(t, receiver.Events)
	wrapper, ok := payload.Network.(*gonnect.Remapper)
	if !ok {
		t.Fatalf("payload.Network = %T, want *gonnect.Remapper", payload.Network)
	}
	if _, ok := wrapper.GetWrapped().(*gonnect.DetachedNetwork); !ok {
		t.Fatalf("remapper wrapped network = %T, want *gonnect.DetachedNetwork", wrapper.GetWrapped())
	}
}

func TestRemapperNodeMenuValidationAndSave(t *testing.T) {
	w := newRemapperTestWorkspace(t)
	node, err := w.AddNodeByClass(NodeTypeRemapper, "remapper")
	if err != nil {
		t.Fatalf("AddNodeByClass Remapper: %v", err)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formMessageBase(formular.MessageFormApply, node),
		BlockID:     RemapperConfigBlockID,
		Values:      map[string]any{RemapperRulesField: "TRUE\nREMAP DST PORT"},
	})
	assertNodeLabel(t, w, node, "invalid config")
	rulesText := "DIAL\nADDR_S example.test\nAND\nREMAP DST PORT 8080"
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formMessageBase(formular.MessageFormApply, node),
		BlockID:     RemapperConfigBlockID,
		Values:      map[string]any{RemapperRulesField: rulesText},
	})
	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := configer.String(cfg.View(configer.Path{"remapper"}), configer.Path{RemapperRulesField}, "")
	if got != rulesText {
		t.Fatalf("saved rules = %q, want %q", got, rulesText)
	}
}

type remapperCaptureNetwork struct {
	*gonnect.RejectNetwork
	calls map[string][]string
}

func newRemapperCaptureNetwork() *remapperCaptureNetwork {
	return &remapperCaptureNetwork{
		RejectNetwork: &gonnect.RejectNetwork{},
		calls:         map[string][]string{},
	}
}

func (n *remapperCaptureNetwork) Dial(_ context.Context, network, address string) (net.Conn, error) {
	n.calls["Dial"] = []string{network, address}
	return nil, errRemapperCapture
}

func (n *remapperCaptureNetwork) Listen(_ context.Context, network, address string) (net.Listener, error) {
	n.calls["Listen"] = []string{network, address}
	return nil, errRemapperCapture
}

func (n *remapperCaptureNetwork) call(name string) []string {
	return n.calls[name]
}

func newRemapperTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(RemapperClass{}); err != nil {
		t.Fatalf("AddNodeClass Remapper: %v", err)
	}
	if err := w.AddNodeClass(remapperTestNetworkSourceClass{}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	return w
}

const remapperTestNetworkSourceNodeType = "test/NetworkSource"

type remapperTestNetworkSourceClass struct{}

func (remapperTestNetworkSourceClass) ClassName() string { return remapperTestNetworkSourceNodeType }

func (remapperTestNetworkSourceClass) ShortDescription() string { return "network source" }

func (remapperTestNetworkSourceClass) LongDescription() string { return "network source" }

func (remapperTestNetworkSourceClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}

func (remapperTestNetworkSourceClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &remapperTestNetworkSourceNode{network: gonnect.NewLoopbackNetwork()}, nil
}

type remapperTestNetworkSourceNode struct {
	pasta.BasicNode
	w       *pasta.Workspace
	id      uint64
	out     uint64
	network gonnect.Network
	outputs NetworkOutputCache
}

func (n *remapperTestNetworkSourceNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.out = restoredLeft(restored, 0)
	if n.network == nil {
		n.network = gonnect.NewLoopbackNetwork()
	}
	return nil
}

func (n *remapperTestNetworkSourceNode) OnReady() error {
	n.outputs.SendPortLocked(n.w, n.id, n.out, n.network)
	return nil
}

func (n *remapperTestNetworkSourceNode) OnStop() {
	n.outputs.CloseAll()
}

func (n *remapperTestNetworkSourceNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *remapperTestNetworkSourceNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.SendLocked(n.w, n.id, link, n.network)
	}
	return nil
}

func (n *remapperTestNetworkSourceNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
	}
	return nil
}

func (n *remapperTestNetworkSourceNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.outputs.RefreshLocked(n.w, n.id, event.Link, n.network)
	}
	return nil
}

func assertNodeLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	if snapshot.Label != want {
		t.Fatalf("node label = %q, want %q", snapshot.Label, want)
	}
}

func formMessageBase(messageType string, node uint64) formular.MessageBase {
	return formular.MessageBase{
		Type:           messageType,
		MenuID:         pasta.NodeMenuID(node),
		MenuGeneration: 1,
	}
}
