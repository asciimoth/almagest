package almagest

import (
	"reflect"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sniffer"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestSnifferNodeCompileRulesBuildsInlineClassifiersAndSlots(t *testing.T) {
	node := &snifferNode{}
	rules, controls, err := node.compileRules(`
DIAL
TCP
AND
INTERCEPT

SNIFF HTTP URL:/alpha
SLOT 1

SNIFF TLS ALPN:http/1.1
ROUTE 2 DST:127.0.0.1:8443
`, "")
	if err != nil {
		t.Fatalf("compileRules() error = %v, want nil", err)
	}
	if got, want := len(rules.Classifiers), 2; got != want {
		t.Fatalf("classifier count = %d, want %d", got, want)
	}
	if got, want := controls.MentionedSlots(), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mentioned slots = %v, want %v", got, want)
	}
}

func TestSnifferNodeCompileRulesRejectsInvalidRulesAndSlots(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "bad inline classifier", text: "SNIFF HTTP URL\nSLOT 1"},
		{name: "unsupported node slot", text: "TRUE\nSLOT 17"},
		{name: "bad route field", text: "TRUE\nROUTE 1 UNKNOWN:value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &snifferNode{}
			if _, _, err := node.compileRules(tt.text, ""); err == nil {
				t.Fatalf("compileRules(%q) error = nil, want error", tt.text)
			}
		})
	}
}

func TestSnifferNodeEmitsGonnectSnifferPayload(t *testing.T) {
	w := newSnifferTestWorkspace(t)
	mock, err := w.AddNodeByClass(NodeTypeMockNetwork, "mock")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeSniffer, "sniffer")
	if err != nil {
		t.Fatalf("AddNodeByClass Sniffer: %v", err)
	}
	applySnifferRules(t, w, node, "TRUE\nSLOT 1")
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	mockNode := mustNodeSnapshot(t, w, mock)
	snifferNode := mustNodeSnapshot(t, w, node)
	if _, _, err := w.AddLink(mockNode.LeftPorts[0], snifferRightPort(t, w, node, slotPortName(1))); err != nil {
		t.Fatalf("AddLink mock->sniffer: %v", err)
	}
	if _, _, err := w.AddLink(snifferNode.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink sniffer->receiver: %v", err)
	}

	payload := receiveOutNetPayload(t, receiver.events)
	wrapper, ok := payload.Network.(*sniffer.Sniffer)
	if !ok {
		t.Fatalf("payload.Network = %T, want *sniffer.Sniffer", payload.Network)
	}
	if got, want := len(wrapper.Outputs()), 1; got != want {
		t.Fatalf("sniffer outputs = %d, want %d", got, want)
	}
}

func TestSnifferNodeWaitsForMentionedBackendSlot(t *testing.T) {
	w := newSnifferTestWorkspace(t)
	mock, err := w.AddNodeByClass(NodeTypeMockNetwork, "mock")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeSniffer, "sniffer")
	if err != nil {
		t.Fatalf("AddNodeByClass Sniffer: %v", err)
	}
	applySnifferRules(t, w, node, "TRUE\nSLOT 2")
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	snifferSnapshot := mustNodeSnapshot(t, w, node)
	if _, _, err := w.AddLink(snifferSnapshot.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink sniffer->receiver: %v", err)
	}
	assertNoOutNetEvent(t, receiver.events)
	assertNodeLabel(t, w, node, "waiting for slot 2")

	mockSnapshot := mustNodeSnapshot(t, w, mock)
	if _, _, err := w.AddLink(mockSnapshot.LeftPorts[0], snifferRightPort(t, w, node, slotPortName(2))); err != nil {
		t.Fatalf("AddLink mock->slot2: %v", err)
	}
	receiveOutNetPayload(t, receiver.events)
}

func TestSnifferNodeRefreshRebuildsClosedPayload(t *testing.T) {
	w := newSnifferTestWorkspace(t)
	mock, err := w.AddNodeByClass(NodeTypeMockNetwork, "mock")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeSniffer, "sniffer")
	if err != nil {
		t.Fatalf("AddNodeByClass Sniffer: %v", err)
	}
	applySnifferRules(t, w, node, "TRUE\nSLOT 1")
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	mockSnapshot := mustNodeSnapshot(t, w, mock)
	snifferSnapshot := mustNodeSnapshot(t, w, node)
	if _, _, err := w.AddLink(mockSnapshot.LeftPorts[0], snifferRightPort(t, w, node, slotPortName(1))); err != nil {
		t.Fatalf("AddLink mock->sniffer: %v", err)
	}
	link, _, err := w.AddLink(snifferSnapshot.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink sniffer->receiver: %v", err)
	}
	first := receiveOutNetPayload(t, receiver.events).Network
	if err := first.Close(); err != nil {
		t.Fatalf("first.Close(): %v", err)
	}

	w.EmitEvent(receiver.id, link, std.RequestValue{})
	second := receiveOutNetPayload(t, receiver.events).Network
	if SameNetworkInstance(first, second) {
		t.Fatal("request refresh reused closed sniffer payload")
	}
}

func TestSnifferNodeMenuValidationAndSave(t *testing.T) {
	w := newSnifferTestWorkspace(t)
	node, err := w.AddNodeByClass(NodeTypeSniffer, "sniffer")
	if err != nil {
		t.Fatalf("AddNodeByClass Sniffer: %v", err)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     snifferConfigBlockID,
		Values:      map[string]any{snifferRulesField: "SNIFF HTTP URL\nSLOT 1"},
	})
	assertNodeLabel(t, w, node, "invalid config")

	rulesText := "TRUE\nSLOT 1"
	applySnifferRules(t, w, node, rulesText)
	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := configer.String(cfg.View(configer.Path{"sniffer"}), configer.Path{snifferRulesField}, "")
	if got != rulesText {
		t.Fatalf("saved rules = %q, want %q", got, rulesText)
	}
}

func applySnifferRules(t *testing.T, w *pasta.Workspace, node uint64, rules string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     snifferConfigBlockID,
		Values:      map[string]any{snifferRulesField: rules},
	})
}

func snifferRightPort(t *testing.T, w *pasta.Workspace, node uint64, name string) uint64 {
	t.Helper()
	snapshot := mustNodeSnapshot(t, w, node)
	for _, port := range snapshot.RightPorts {
		portSnapshot, ok := w.PortSnapshot(port)
		if ok && portSnapshot.Name == name && portSnapshot.Direction == "right" {
			return port
		}
	}
	t.Fatalf("sniffer node %d has no right port %q", node, name)
	return 0
}

func newSnifferTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(snifferClass{}); err != nil {
		t.Fatalf("AddNodeClass Sniffer: %v", err)
	}
	if err := w.AddNodeClass(mockNetClass{}); err != nil {
		t.Fatalf("AddNodeClass MockNetwork: %v", err)
	}
	return w
}
