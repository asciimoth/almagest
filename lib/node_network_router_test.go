package almagest

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/asciimoth/configer/configer"
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

func TestNetworkRouterRestoreDefersConsumerOutputUntilReady(t *testing.T) {
	var receiver *restoreReadyNetworkReceiver
	cfg := configer.NewMemory(map[string]any{
		"a-consumer": map[string]any{
			"Class": "test/RestoreReadyNetworkReceiver",
			"Links": []any{"Network -> [router] Network"},
		},
		"backend": map[string]any{
			"Class": NodeTypeMockNetwork,
		},
		"router": map[string]any{
			"Class": NodeTypeNetworkRouter,
			"Links": []any{"slot 1 -> [backend] Network"},
			"rules": "TRUE\nSLOT 1\n",
		},
	})

	w, err := pasta.WorkspaceFromConfig([]pasta.NodeClass{
		networkRouterClass{},
		mockNetClass{},
		restoreReadyNetworkReceiverClass{node: &receiver},
	}, cfg, NewDebugLogFactory(1024))
	if err != nil {
		t.Fatalf("WorkspaceFromConfig: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	if receiver == nil {
		t.Fatal("receiver was not restored")
	}
	if receiver.eventBeforeReady {
		t.Fatal("router emitted network payload to consumer before Workspace.Ready")
	}
	if receiver.events == 0 {
		t.Fatal("receiver got no router payload after restore")
	}
	routerID, ok := w.NodeIDByName("router")
	if !ok {
		t.Fatal("restored router missing")
	}
	if snapshot := mustNodeSnapshot(t, w, routerID); snapshot.Placeholder {
		t.Fatalf("router restored as placeholder: %#v", snapshot.Popups)
	}
}

func TestNetworkRouterTreatsClosedAttachNotificationsAsNonFatal(t *testing.T) {
	router := gonnect.NewRouter(nil)
	t.Cleanup(func() { _ = router.Close() })
	backend := gonnect.NewLoopbackNetwok()
	if err := router.Attach(1, backend); err != nil {
		t.Fatalf("Attach backend: %v", err)
	}
	node := &networkRouterNode{router: router}

	closedNotifications := errors.Join(net.ErrClosed, fmt.Errorf("notify downstream: %w", net.ErrClosed))
	if !node.isNonFatalAttachError(closedNotifications) {
		t.Fatalf("closed notification error classified as fatal: %v", closedNotifications)
	}
	mixedErr := errors.Join(net.ErrClosed, errors.New("subscribe failed"))
	if node.isNonFatalAttachError(mixedErr) {
		t.Fatalf("mixed attach error classified as non-fatal: %v", mixedErr)
	}
	_ = router.Close()
	if node.isNonFatalAttachError(net.ErrClosed) {
		t.Fatal("closed router attach error classified as non-fatal")
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

type restoreReadyNetworkReceiverClass struct {
	node **restoreReadyNetworkReceiver
}

func (restoreReadyNetworkReceiverClass) ClassName() string {
	return "test/RestoreReadyNetworkReceiver"
}
func (restoreReadyNetworkReceiverClass) ShortDescription() string { return "restore ready receiver" }
func (restoreReadyNetworkReceiverClass) LongDescription() string  { return "restore ready receiver" }
func (restoreReadyNetworkReceiverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
	}}
}
func (c restoreReadyNetworkReceiverClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	node := &restoreReadyNetworkReceiver{}
	if c.node != nil {
		*c.node = node
	}
	return node, nil
}

type restoreReadyNetworkReceiver struct {
	pasta.BasicNode

	w      *pasta.Workspace
	in     uint64
	events int

	eventBeforeReady bool
}

func (n *restoreReadyNetworkReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, _ uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	if restored != nil && len(restored.RightPorts) > 0 {
		n.in = restored.RightPorts[0]
	}
	return nil
}

func (n *restoreReadyNetworkReceiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "right" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *restoreReadyNetworkReceiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	if _, ok := event.Payload.(NetworkPayload); !ok {
		return nil
	}
	if !n.w.IsReady() {
		n.eventBeforeReady = true
	}
	n.events++
	return nil
}
