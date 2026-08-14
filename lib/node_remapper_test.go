package almagest

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/pasta/pasta"
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
	mock, err := w.AddNodeByClass(NodeTypeMockNetwork, "mock")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	remapper, err := w.AddNodeByClass(NodeTypeRemapper, "remapper")
	if err != nil {
		t.Fatalf("AddNodeByClass Remapper: %v", err)
	}
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	mockNode := mustNodeSnapshot(t, w, mock)
	remapperNode := mustNodeSnapshot(t, w, remapper)
	if _, _, err := w.AddLink(mockNode.LeftPorts[0], remapperNode.RightPorts[0]); err != nil {
		t.Fatalf("AddLink mock->remapper: %v", err)
	}
	if _, _, err := w.AddLink(remapperNode.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink remapper->receiver: %v", err)
	}

	payload := receiveOutNetPayload(t, receiver.events)
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
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     remapperConfigBlockID,
		Values:      map[string]any{remapperRulesField: "TRUE\nREMAP DST PORT"},
	})
	assertNodeLabel(t, w, node, "invalid config")
	rulesText := "DIAL\nADDR_S example.test\nAND\nREMAP DST PORT 8080"
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     remapperConfigBlockID,
		Values:      map[string]any{remapperRulesField: rulesText},
	})
	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := configer.String(cfg.View(configer.Path{"remapper"}), configer.Path{remapperRulesField}, "")
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
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(remapperClass{}); err != nil {
		t.Fatalf("AddNodeClass Remapper: %v", err)
	}
	if err := w.AddNodeClass(mockNetClass{}); err != nil {
		t.Fatalf("AddNodeClass MockNetwork: %v", err)
	}
	return w
}
