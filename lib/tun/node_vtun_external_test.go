package tunnodes

import (
	"context"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/almagest/lib/testkit/tuntest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestVTunNodeEmitsNetworkAndTunAndRebuildsOnFormApply(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	netReceiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	tunReceiver := tuntest.NewReceiver(3)
	if _, err := w.AddNode(tunReceiver, "test/TunReceiver", "tun receiver"); err != nil {
		t.Fatalf("AddNode tun receiver: %v", err)
	}

	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(tunReceiver.In, vtunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	firstNetwork := networktest.ReceivePayload(t, netReceiver.Events).Network
	firstTun := tuntest.ReceivePayload(t, tunReceiver.Events).Tun
	firstNetworkWrapper, ok := firstNetwork.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("first network = %T, want *gonnect.DetachedNetwork", firstNetwork)
	}

	w.SendNodeFormularMsg(vtunID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(vtunID)},
		BlockID:     "config",
		Values: map[string]any{
			"name":           "vtun-test",
			"localAddrs":     "10.123.0.2",
			"dnsServers":     "1.1.1.1",
			"mtu":            1300,
			"endpointCh":     16,
			"eventCh":        4,
			"mwo":            1,
			"mro":            2,
			"noLoopbackAddr": true,
			"disableSACK":    true,
		},
	})

	secondNetwork := networktest.ReceivePayload(t, netReceiver.Events).Network
	secondTun := tuntest.ReceivePayload(t, tunReceiver.Events).Tun
	secondNetworkWrapper, ok := secondNetwork.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("second network = %T, want *gonnect.DetachedNetwork", secondNetwork)
	}
	if firstNetworkWrapper.GetWrapped() == secondNetworkWrapper.GetWrapped() {
		t.Fatal("VTun form apply reused wrapped VTun, want rebuilt instance")
	}
	if nodekit.SameTunSource(firstTun, secondTun) {
		t.Fatal("VTun form apply reused TUN source, want rebuilt instance")
	}
	if up, err := firstNetworkWrapper.IsUp(); err != nil || up {
		t.Fatalf("old network wrapper IsUp = %v, %v; want false, nil", up, err)
	}
}

func TestVTunEquivalentTunConfigDoesNotRebuildActiveVTun(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)
	netReceiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	tunReceiver := tuntest.NewReceiver(3)
	tunReceiverID, err := w.AddNode(tunReceiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode tun receiver: %v", err)
	}

	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	tunLink, _, err := w.AddLink(tunReceiver.In, vtunSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	_ = tuntest.ReceivePayload(t, tunReceiver.Events)
	networktest.AssertNoEvent(t, netReceiver.Events)

	addrs := strings.Fields(testkit.ReadonlyMenuFieldString(t, state, vtunID, "effective", "localAddrs"))
	if len(addrs) == 0 {
		t.Fatal("effective localAddrs is empty, want dynamic VTun address")
	}

	w.EmitEvent(tunReceiverID, tunLink, TunConfig{Addrs: addrs, MTU: DefaultVTunMTU})

	networktest.AssertNoEvent(t, netReceiver.Events)
}

func TestVTunGeneratedAddrsAreNotSaved(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)
	testkit.AssertLeadingBlock(t, state, vtunID, "effective")
	netReceiver := networktest.NewReceiver(1)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)

	localAddrs := testkit.ReadonlyMenuFieldString(t, state, vtunID, "effective", "localAddrs")
	if len(strings.Fields(localAddrs)) == 0 {
		t.Fatal("effective localAddrs is empty, want engine allocation cached")
	}
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "localAddrs", localAddrs)
	testkit.AssertReadonlyMenuFieldValue(t, state, vtunID, "effective", "mtu", DefaultVTunMTU)
	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw := configer.String(cfg.View(configer.Path{"vtun"}), configer.Path{"localAddrs"}, "")
	if raw != "" {
		t.Fatalf("saved localAddrs = %q, want empty generated value omitted", raw)
	}
}

func TestVTunNodeValidationReportsStatus(t *testing.T) {
	w := newVTunNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "localAddrs"},
		Value:       "not-an-ip",
	})
	testkit.AssertMenuItemStatus(t, state, vtunID, "localAddrs", formular.StatusError)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "localAddrs"},
		Value:       "",
	})
	testkit.AssertMenuItemStatus(t, state, vtunID, "localAddrs", formular.StatusOK)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       DefaultVTunMTU,
	})
	testkit.AssertMenuItemStatus(t, state, vtunID, "mtu", formular.StatusOK)
}

func newVTunNodeTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	runtime := vtunTestRuntime{ctx: context.Background(), sys: &sysnetdebug.System{}}
	if err := w.AddNodeClass(VTunClass{Runtime: runtime}); err != nil {
		t.Fatalf("AddNodeClass VTun: %v", err)
	}
	return w
}

type vtunTestRuntime struct {
	ctx context.Context
	sys sysnet.System
}

func (r vtunTestRuntime) Context() context.Context { return r.ctx }
func (vtunTestRuntime) Pool() bufpool.Pool         { return nil }
func (r vtunTestRuntime) System() sysnet.System    { return r.sys }
func (vtunTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (vtunTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (vtunTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (vtunTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (vtunTestRuntime) WorkerSpawner(uint64) WorkerSpawner {
	return nil
}
func (vtunTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
func (r vtunTestRuntime) AllocIP() subnet.IPAllocator {
	allocator, ok := r.sys.(interface {
		AllocIP() subnet.IPAllocator
	})
	if !ok {
		return nil
	}
	return allocator.AllocIP()
}
