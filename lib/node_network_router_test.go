package lib

import (
	"testing"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

func TestNetworkRouterNodeEmitsSingleRouterInstance(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	routerSnapshot := mustNodeSnapshot(t, w, routerID)
	if _, _, err := w.AddLink(routerSnapshot.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink router->receiver: %v", err)
	}

	payload := receiveOutNetPayload(t, receiver.events)
	wrapped, ok := payload.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("payload.Network = %T, want *gonnect.DetachedNetwork", payload.Network)
	}
	firstRouter, ok := wrapped.GetWrapped().(*gonnect.Router)
	if !ok {
		t.Fatalf("wrapped network = %T, want *gonnect.Router", wrapped.GetWrapped())
	}

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, routerID),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 2\n"},
	})

	cfg := firstRouter.GetCfg()
	reporter, ok := cfg.(interface{ MentionedSlots() []int })
	if !ok {
		t.Fatalf("router cfg = %T, want MentionedSlots", cfg)
	}
	assertIntSlice(t, reporter.MentionedSlots(), []int{2})
	if got := wrapped.GetWrapped(); got != firstRouter {
		t.Fatalf("wrapped router changed from %p to %p", firstRouter, got)
	}
}

func TestNetworkRouterNodeReviewsSlotPortsFromRulesAndLinks(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 1"})

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, routerID),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 3\n"},
	})
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 3"})

	backendID, err := w.AddNodeByClass(NodeTypeMockNetwork, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	backend := mustNodeSnapshot(t, w, backendID)
	router := mustNodeSnapshot(t, w, routerID)
	if _, _, err := w.AddLink(backend.LeftPorts[0], router.RightPorts[0]); err != nil {
		t.Fatalf("AddLink backend->router slot 3: %v", err)
	}

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, routerID),
		BlockID:     "config",
		Values:      map[string]any{"rules": ""},
	})
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 3"})
}

func TestNetworkRouterNodeValidationReportsStatus(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("Subscribe returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	w.SubscribeNodeMenu(routerID, 1)

	w.SendNodeFormularMsg(routerID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(routerID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "rules"},
		Value:       "NOPE\n",
	})

	snapshot, ok := state.Snapshot(pasta.NodeMenuID(routerID))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	item, ok := FindItem(mustFindBlock(t, snapshot, "config"), "rules")
	if !ok {
		t.Fatal("rules item missing")
	}
	if item.Status != formular.StatusError {
		t.Fatalf("rules status = %q, want %q", item.Status, formular.StatusError)
	}

	w.SendNodeFormularMsg(routerID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(routerID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "rules"},
		Value:       "TRUE\nSLOT 1\n",
	})
	snapshot, ok = state.Snapshot(pasta.NodeMenuID(routerID))
	if !ok {
		t.Fatal("menu snapshot missing after ok validation")
	}
	item, ok = FindItem(mustFindBlock(t, snapshot, "config"), "rules")
	if !ok {
		t.Fatal("rules item missing after ok validation")
	}
	if item.Status != formular.StatusOK {
		t.Fatalf("rules status = %q, want %q", item.Status, formular.StatusOK)
	}
}

func newNetworkRouterTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(networkRouterClass{}); err != nil {
		t.Fatalf("AddNodeClass NetworkRouter: %v", err)
	}
	if err := w.AddNodeClass(mockNetClass{}); err != nil {
		t.Fatalf("AddNodeClass MockNetwork: %v", err)
	}
	return w
}

func assertNetworkRouterRightPorts(t *testing.T, w *pasta.Workspace, node uint64, want []string) {
	t.Helper()
	snapshot := mustNodeSnapshot(t, w, node)
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := w.PortSnapshot(port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		got = append(got, ps.Name)
	}
	assertStringSlice(t, got, want)
}

func assertIntSlice(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slice len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("slice[%d] = %d (%v), want %d (%v)", i, got[i], got, want[i], want)
		}
	}
}

func mustFindBlock(t *testing.T, snapshot formular.MenuSnapshotMessage, id string) formular.Block {
	t.Helper()
	block, ok := FindBlock(snapshot, id)
	if !ok {
		t.Fatalf("block %q missing", id)
	}
	return block
}
