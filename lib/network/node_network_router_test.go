package networknodes

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/bufpool"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

func TestNetworkRouterNodeEmitsSingleRouterInstance(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	receiver := networktest.NewReceiver(2)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	routerSnapshot := testkit.MustNodeSnapshot(t, w, routerID)
	if _, _, err := w.AddLink(routerSnapshot.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink router->receiver: %v", err)
	}

	payload := networktest.ReceivePayload(t, receiver.Events)
	wrapped, ok := payload.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("payload.Network = %T, want *gonnect.DetachedNetwork", payload.Network)
	}
	firstRouter, ok := wrapped.GetWrapped().(*gonnect.Router)
	if !ok {
		t.Fatalf("wrapped network = %T, want *gonnect.Router", wrapped.GetWrapped())
	}

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, routerID),
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

func TestNetworkRouterNodeInstallsDNSResolver(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	dnsID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "dns")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	backendID, err := w.AddNodeByClass(NodeTypeMockNetwork, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, dnsID, "router.test A 192.0.2.10")
	receiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	router := testkit.MustNodeSnapshot(t, w, routerID)
	backend := testkit.MustNodeSnapshot(t, w, backendID)
	staticDNS := testkit.MustNodeSnapshot(t, w, dnsID)
	if _, _, err := w.AddLink(backend.LeftPorts[0], networkRouterRightPort(t, w, routerID, slotPortName(1))); err != nil {
		t.Fatalf("AddLink backend->router: %v", err)
	}
	if _, _, err := w.AddLink(staticDNS.LeftPorts[0], networkRouterRightPort(t, w, routerID, NetworkRouterDNSPortName)); err != nil {
		t.Fatalf("AddLink dns->router: %v", err)
	}
	if _, _, err := w.AddLink(router.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink router->receiver: %v", err)
	}

	network := networktest.ReceivePayload(t, receiver.Events).Network
	hosts, err := network.LookupHost(context.Background(), "router.test")
	if err != nil {
		t.Fatalf("LookupHost through router resolver: %v", err)
	}
	testkit.AssertStringSlice(t, hosts, []string{"192.0.2.10"})
}

func TestNetworkRouterDNSLinksAttachDetachReattach(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	backendID, err := w.AddNodeByClass(NodeTypeMockNetwork, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	firstDNSID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "dns-a")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns dns-a: %v", err)
	}
	secondDNSID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "dns-b")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns dns-b: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, firstDNSID, "router.test A 192.0.2.11")
	applyStaticDNSRecordsToWorkspace(t, w, secondDNSID, "router.test A 192.0.2.12")
	receiver := networktest.NewReceiver(8)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	router := testkit.MustNodeSnapshot(t, w, routerID)
	backend := testkit.MustNodeSnapshot(t, w, backendID)
	if _, _, err := w.AddLink(backend.LeftPorts[0], networkRouterRightPort(t, w, routerID, slotPortName(1))); err != nil {
		t.Fatalf("AddLink backend->router: %v", err)
	}
	if _, _, err := w.AddLink(router.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink router->receiver: %v", err)
	}
	routerRuntime := receiveNetworkRouterRuntime(t, receiver.Events)

	firstDNS := testkit.MustNodeSnapshot(t, w, firstDNSID)
	dnsPort := networkRouterRightPort(t, w, routerID, NetworkRouterDNSPortName)
	dnsLink, _, err := w.AddLink(firstDNS.LeftPorts[0], dnsPort)
	if err != nil {
		t.Fatalf("AddLink dns-a->router: %v", err)
	}
	assertNetworkRouterLookupHost(t, routerRuntime, "router.test", "192.0.2.11")

	w.RemoveLink(dnsLink)
	if got := routerRuntime.GetResolver(); got != nil {
		t.Fatalf("router resolver after DNS detach = %T, want nil", got)
	}

	secondDNS := testkit.MustNodeSnapshot(t, w, secondDNSID)
	dnsLink, _, err = w.AddLink(secondDNS.LeftPorts[0], dnsPort)
	if err != nil {
		t.Fatalf("AddLink dns-b->router: %v", err)
	}
	assertNetworkRouterLookupHost(t, routerRuntime, "router.test", "192.0.2.12")
	_ = dnsLink
}

func TestNetworkRouterDNSCopyPastePreservesResolverLink(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)
	routerID, backendID, dnsID := setupNetworkRouterDNSWorkspace(t, w, "copy", "192.0.2.30")
	assertNetworkRouterLookupHost(t, networkRouterRuntimeFromWorkspace(t, w, routerID), "router.test", "192.0.2.30")

	clip := w.Copy([]uint64{routerID, backendID, dnsID})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := w.Paste(clip)
	if len(pasted) != 3 {
		t.Fatalf("Paste returned %d nodes (%v), want 3", len(pasted), pasted)
	}
	pastedRouter := workspaceNodeByClass(t, w, pasted, NodeTypeNetworkRouter)
	pastedBackend := workspaceNodeByClass(t, w, pasted, NodeTypeMockNetwork)
	pastedDNS := workspaceNodeByClass(t, w, pasted, dnsnodes.NodeTypeStaticDNS)
	assertNetworkRouterRightPorts(t, w, pastedRouter, []string{"slot 1", NetworkRouterDNSPortName})
	mustWorkspaceTypedLink(t, w, TypeNetwork, pastedBackend, "left", "Network", pastedRouter, "right", "slot 1")
	mustWorkspaceTypedLink(t, w, dnsnodes.TypeDNS, pastedDNS, "left", "DNS", pastedRouter, "right", NetworkRouterDNSPortName)
	assertNetworkRouterLookupHost(t, networkRouterRuntimeFromWorkspace(t, w, pastedRouter), "router.test", "192.0.2.30")
}

func TestNetworkRouterDNSSaveRestorePreservesResolverLink(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)
	setupNetworkRouterDNSWorkspace(t, w, "restore", "192.0.2.40")

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restored, err := pasta.WorkspaceFromConfig([]pasta.NodeClass{
		NetworkRouterClass{},
		MockNetworkClass{},
		dnsnodes.StaticDNSClass{},
	}, cfg, logging.NewDebugLogFactory(1024))
	if err != nil {
		t.Fatalf("WorkspaceFromConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	restoredRouter, ok := restored.NodeIDByName("restore-router")
	if !ok {
		t.Fatal("restored router missing")
	}
	restoredBackend, ok := restored.NodeIDByName("restore-backend")
	if !ok {
		t.Fatal("restored backend missing")
	}
	restoredDNS, ok := restored.NodeIDByName("restore-dns")
	if !ok {
		t.Fatal("restored DNS missing")
	}
	assertNetworkRouterRightPorts(t, restored, restoredRouter, []string{"slot 1", NetworkRouterDNSPortName})
	mustWorkspaceTypedLink(t, restored, TypeNetwork, restoredBackend, "left", "Network", restoredRouter, "right", "slot 1")
	mustWorkspaceTypedLink(t, restored, dnsnodes.TypeDNS, restoredDNS, "left", "DNS", restoredRouter, "right", NetworkRouterDNSPortName)
	assertNetworkRouterLookupHost(t, networkRouterRuntimeFromWorkspace(t, restored, restoredRouter), "router.test", "192.0.2.40")
}

func TestNetworkRouterRestorePreservesLinkedSlotOmittedByRules(t *testing.T) {
	cfg := configer.NewMemory(map[string]any{
		"backend-1": map[string]any{
			"Class": NodeTypeMockNetwork,
		},
		"backend-2": map[string]any{
			"Class": NodeTypeMockNetwork,
		},
		"router": map[string]any{
			"Class": NodeTypeNetworkRouter,
			"Links": []any{
				"slot 1 -> [backend-1] Network",
				"slot 2 -> [backend-2] Network",
			},
			"rules": "TRUE\nSLOT 1\n",
		},
	})

	w, err := pasta.WorkspaceFromConfig([]pasta.NodeClass{
		NetworkRouterClass{},
		MockNetworkClass{},
	}, cfg, logging.NewDebugLogFactory(1024))
	if err != nil {
		t.Fatalf("WorkspaceFromConfig: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	router, ok := w.NodeIDByName("router")
	if !ok {
		t.Fatal("restored router missing")
	}
	backend1, ok := w.NodeIDByName("backend-1")
	if !ok {
		t.Fatal("restored backend-1 missing")
	}
	backend2, ok := w.NodeIDByName("backend-2")
	if !ok {
		t.Fatal("restored backend-2 missing")
	}
	assertNetworkRouterRightPorts(t, w, router, []string{"slot 1", "slot 2", NetworkRouterDNSPortName})
	mustWorkspaceTypedLink(t, w, TypeNetwork, backend1, "left", "Network", router, "right", "slot 1")
	mustWorkspaceTypedLink(t, w, TypeNetwork, backend2, "left", "Network", router, "right", "slot 2")
}

func TestNetworkRouterNodeReviewsSlotPortsFromRulesAndLinks(t *testing.T) {
	w := newNetworkRouterTestWorkspace(t)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 1", NetworkRouterDNSPortName})

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, routerID),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 3\n"},
	})
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 3", NetworkRouterDNSPortName})

	backendID, err := w.AddNodeByClass(NodeTypeMockNetwork, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	backend := testkit.MustNodeSnapshot(t, w, backendID)
	router := testkit.MustNodeSnapshot(t, w, routerID)
	slot3Port := router.RightPorts[0]
	slot3Link, _, err := w.AddLink(backend.LeftPorts[0], slot3Port)
	if err != nil {
		t.Fatalf("AddLink backend->router slot 3: %v", err)
	}

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, routerID),
		BlockID:     "config",
		Values:      map[string]any{"rules": ""},
	})
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 3", NetworkRouterDNSPortName})

	w.RemoveLink(slot3Link)
	assertNetworkRouterRightPorts(t, w, routerID, []string{"slot 1", NetworkRouterDNSPortName})
	if _, ok := w.PortSnapshot(slot3Port); ok {
		t.Fatal("unreferenced and unlinked slot 3 port still exists")
	}
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
	item, ok := testkit.FindItem(mustFindBlock(t, snapshot, "config"), "rules")
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
	item, ok = testkit.FindItem(mustFindBlock(t, snapshot, "config"), "rules")
	if !ok {
		t.Fatal("rules item missing after ok validation")
	}
	if item.Status != formular.StatusOK {
		t.Fatalf("rules status = %q, want %q", item.Status, formular.StatusOK)
	}
}

func TestNetworkRouterNodeMenuAppliesAndSavesDNSCacheSelector(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	manager.Storage("shared")
	manager.Storage("other")
	w := newNetworkRouterTestWorkspaceWithDNSCacheManager(t, manager)
	state, sub := subscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	w.SubscribeNodeMenu(routerID, sub)
	assertConfigFieldValue(t, state, routerID, NetworkRouterConfigBlockID, NetworkRouterDNSCacheStorageNameField, "")
	assertConfigFieldAllowedValues(t, state, routerID, NetworkRouterConfigBlockID, NetworkRouterDNSCacheStorageNameField, []any{"", "other", "shared"})

	w.SendNodeFormularMsg(routerID, formular.FieldValidateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, routerID),
		Field:       formular.FieldRef{BlockID: NetworkRouterConfigBlockID, FieldID: NetworkRouterDNSCacheStorageNameField},
		Value:       string([]byte{0xff}),
	})
	assertMenuItemStatus(t, state, routerID, NetworkRouterDNSCacheStorageNameField, formular.StatusError)

	w.SendNodeFormularMsg(routerID, formular.FieldValidateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, routerID),
		Field:       formular.FieldRef{BlockID: NetworkRouterConfigBlockID, FieldID: NetworkRouterRulesField},
		Value:       "TRUE\nSLOT 1\n",
	})
	assertMenuItemStatus(t, state, routerID, NetworkRouterRulesField, formular.StatusOK)

	w.SendNodeFormularMsg(routerID, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, routerID),
		BlockID:     NetworkRouterConfigBlockID,
		Values: map[string]any{
			NetworkRouterRulesField:               "TRUE\nSLOT 1\n",
			NetworkRouterDNSCacheStorageNameField: "shared",
		},
	})
	assertConfigFieldValue(t, state, routerID, NetworkRouterConfigBlockID, NetworkRouterDNSCacheStorageNameField, "shared")

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := cfg.Get(configer.Path{"router", NetworkRouterDNSCacheStorageNameField})
	if err != nil {
		t.Fatalf("saved NetworkRouter DNS cache storage missing: %v", err)
	}
	if got != "shared" {
		t.Fatalf("saved NetworkRouter DNS cache storage = %#v, want shared", got)
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
		NetworkRouterClass{},
		MockNetworkClass{},
		restoreReadyNetworkReceiverClass{node: &receiver},
	}, cfg, logging.NewDebugLogFactory(1024))
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
	if snapshot := testkit.MustNodeSnapshot(t, w, routerID); snapshot.Placeholder {
		t.Fatalf("router restored as placeholder: %#v", snapshot.Popups)
	}
}

func newNetworkRouterTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	return newNetworkRouterTestWorkspaceWithDNSCacheManager(t, nil)
}

func newNetworkRouterTestWorkspaceWithDNSCacheManager(t *testing.T, manager storage.DnsCacheManager) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(NetworkRouterClass{Runtime: networkRouterTestRuntime{dns: manager}}); err != nil {
		t.Fatalf("AddNodeClass NetworkRouter: %v", err)
	}
	if err := w.AddNodeClass(MockNetworkClass{}); err != nil {
		t.Fatalf("AddNodeClass MockNetwork: %v", err)
	}
	if err := w.AddNodeClass(dnsnodes.StaticDNSClass{}); err != nil {
		t.Fatalf("AddNodeClass StaticDns: %v", err)
	}
	return w
}

type networkRouterTestRuntime struct {
	dns storage.DnsCacheManager
}

func (r networkRouterTestRuntime) Context() context.Context { return context.Background() }
func (r networkRouterTestRuntime) Pool() bufpool.Pool       { return nil }
func (r networkRouterTestRuntime) System() sysnet.System    { return nil }
func (r networkRouterTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (r networkRouterTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (r networkRouterTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (r networkRouterTestRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return nil
}
func (r networkRouterTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
func (r networkRouterTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return r.dns
}

func setupNetworkRouterDNSWorkspace(t *testing.T, w *pasta.Workspace, prefix, ip string) (uint64, uint64, uint64) {
	t.Helper()
	routerID, err := w.AddNodeByClass(NodeTypeNetworkRouter, prefix+"-router")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkRouter: %v", err)
	}
	backendID, err := w.AddNodeByClass(NodeTypeMockNetwork, prefix+"-backend")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	dnsID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, prefix+"-dns")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, dnsID, "router.test A "+ip)

	backend := testkit.MustNodeSnapshot(t, w, backendID)
	dnsNode := testkit.MustNodeSnapshot(t, w, dnsID)
	if _, _, err := w.AddLink(backend.LeftPorts[0], networkRouterRightPort(t, w, routerID, slotPortName(1))); err != nil {
		t.Fatalf("AddLink backend->router: %v", err)
	}
	if _, _, err := w.AddLink(dnsNode.LeftPorts[0], networkRouterRightPort(t, w, routerID, NetworkRouterDNSPortName)); err != nil {
		t.Fatalf("AddLink dns->router: %v", err)
	}
	return routerID, backendID, dnsID
}

func networkRouterRuntimeFromWorkspace(t *testing.T, w *pasta.Workspace, routerID uint64) *gonnect.Router {
	t.Helper()
	receiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", fmt.Sprintf("receiver-%d", routerID)); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	router := testkit.MustNodeSnapshot(t, w, routerID)
	if _, _, err := w.AddLink(router.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink router->receiver: %v", err)
	}
	return receiveNetworkRouterRuntime(t, receiver.Events)
}

func workspaceNodeByClass(t *testing.T, w *pasta.Workspace, nodes []uint64, class string) uint64 {
	t.Helper()
	for _, node := range nodes {
		snapshot := testkit.MustNodeSnapshot(t, w, node)
		if snapshot.Class == class {
			return node
		}
	}
	t.Fatalf("pasted node of class %q missing in %v", class, nodes)
	return 0
}

func mustWorkspaceTypedLink(t *testing.T, w *pasta.Workspace, linkType string, leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	t.Helper()
	left := workspaceNodePort(t, w, leftNode, leftDir, leftName)
	right := workspaceNodePort(t, w, rightNode, rightDir, rightName)
	link, snapshot, ok := w.LinkByPorts(left, right)
	if !ok {
		t.Fatalf("link missing between node %d %s %q and node %d %s %q", leftNode, leftDir, leftName, rightNode, rightDir, rightName)
	}
	if snapshot.Type != linkType {
		t.Fatalf("link %d type = %q, want %q", link, snapshot.Type, linkType)
	}
	return link
}

func workspaceNodePort(t *testing.T, w *pasta.Workspace, node uint64, direction, name string) uint64 {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	ports := snapshot.LeftPorts
	if direction == "right" {
		ports = snapshot.RightPorts
	}
	for _, port := range ports {
		ps, ok := w.PortSnapshot(port)
		if ok && ps.Direction == direction && ps.Name == name {
			return port
		}
	}
	t.Fatalf("node %d has no %s port named %q", node, direction, name)
	return 0
}

func receiveNetworkRouterRuntime(t *testing.T, events <-chan NetworkPayload) *gonnect.Router {
	t.Helper()
	payload := networktest.ReceivePayload(t, events)
	wrapped, ok := payload.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("payload.Network = %T, want *gonnect.DetachedNetwork", payload.Network)
	}
	router, ok := wrapped.GetWrapped().(*gonnect.Router)
	if !ok {
		t.Fatalf("wrapped network = %T, want *gonnect.Router", wrapped.GetWrapped())
	}
	return router
}

func assertNetworkRouterLookupHost(t *testing.T, router *gonnect.Router, host, want string) {
	t.Helper()
	Eventually(t, func() bool {
		hosts, err := router.LookupHost(context.Background(), host)
		return err == nil && len(hosts) == 1 && hosts[0] == want
	}, "router LookupHost(%q) = %q", host, want)
}

func applyStaticDNSRecordsToWorkspace(t *testing.T, w *pasta.Workspace, node uint64, records string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"records": records,
			"ttl":     dnsnodes.DefaultStaticDNSTTL,
		},
	})
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for "+format, args...)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func networkRouterRightPort(t *testing.T, w *pasta.Workspace, node uint64, name string) uint64 {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	for _, port := range snapshot.RightPorts {
		ps, ok := w.PortSnapshot(port)
		if ok && ps.Name == name {
			return port
		}
	}
	t.Fatalf("router right port %q missing", name)
	return 0
}

func assertNetworkRouterRightPorts(t *testing.T, w *pasta.Workspace, node uint64, want []string) {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := w.PortSnapshot(port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		got = append(got, ps.Name)
	}
	testkit.AssertStringSlice(t, got, want)
}

func assertConfigFieldValue(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string, want any) {
	t.Helper()
	item := configFieldItem(t, state, node, blockID, fieldID)
	if item.Value != want {
		t.Fatalf("%s.%s value = %#v, want %#v", blockID, fieldID, item.Value, want)
	}
}

func assertConfigFieldAllowedValues(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string, want []any) {
	t.Helper()
	item := configFieldItem(t, state, node, blockID, fieldID)
	if item.Field == nil {
		t.Fatalf("%s.%s field missing", blockID, fieldID)
	}
	if len(item.AllowedValues) != len(want) {
		t.Fatalf("%s.%s allowed values = %#v, want %#v", blockID, fieldID, item.AllowedValues, want)
	}
	for i := range want {
		if item.AllowedValues[i] != want[i] {
			t.Fatalf("%s.%s allowed values = %#v, want %#v", blockID, fieldID, item.AllowedValues, want)
		}
	}
}

func subscribeMenuState(t *testing.T, w *pasta.Workspace) (*formular.MenuSnapshotState, uint64) {
	t.Helper()
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	return state, sub
}

func assertMenuItemStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, fieldID string, status string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	item, ok := testkit.FindItem(mustFindBlock(t, snapshot, "config"), fieldID)
	if !ok {
		t.Fatalf("%s item missing", fieldID)
	}
	if item.Status != status {
		t.Fatalf("%s status = %q, want %q", fieldID, item.Status, status)
	}
}

func configFieldItem(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string) formular.Item {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	item, ok := testkit.FindItem(mustFindBlock(t, snapshot, blockID), fieldID)
	if !ok {
		t.Fatalf("%s.%s item missing", blockID, fieldID)
	}
	return item
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
	return testkit.MustFindBlock(t, snapshot, id)
}

type memoryDNSCacheManager struct {
	mu       sync.Mutex
	storages map[string]dns.CacheStorage
}

func newMemoryDNSCacheManager() *memoryDNSCacheManager {
	return &memoryDNSCacheManager{storages: map[string]dns.CacheStorage{}}
}

func (m *memoryDNSCacheManager) Storage(name string) dns.CacheStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storages == nil {
		m.storages = map[string]dns.CacheStorage{}
	}
	storage := m.storages[name]
	if storage == nil {
		storage = dns.NewMemoryStorage()
		m.storages[name] = storage
	}
	return storage
}

func (m *memoryDNSCacheManager) StorageNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.storages))
	for name := range m.storages {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
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
	if !n.w.IsReadyLocked() {
		n.eventBeforeReady = true
	}
	n.events++
	return nil
}
