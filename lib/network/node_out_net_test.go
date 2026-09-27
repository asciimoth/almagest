package networknodes

import (
	"context"
	"testing"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type outNetworkTestRuntime struct {
	sys sysnet.System
}

func (r outNetworkTestRuntime) Context() context.Context { return context.Background() }
func (r outNetworkTestRuntime) Pool() bufpool.Pool       { return nil }
func (r outNetworkTestRuntime) System() sysnet.System    { return r.sys }
func (r outNetworkTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (r outNetworkTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (r outNetworkTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (r outNetworkTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (r outNetworkTestRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return nil
}
func (r outNetworkTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

func TestOutNetworkSendsSystemOutNetOnReadyAndRequest(t *testing.T) {
	w := newOutNetworkTestWorkspace(t, gonnect.NewLoopbackNetwork())
	base := w.runtime.sys.(*sysnetdebug.System).OutNetwork
	outID := addOutNetworkTestNode(t, w.workspace)
	out := testkit.MustNodeSnapshot(t, w.workspace, outID)

	receiver := networktest.NewReceiver(2)
	if _, err := w.workspace.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.workspace.AddLink(out.LeftPorts[0], receiver.In)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	payload := networktest.ReceivePayload(t, receiver.Events)
	if payload.DatagramMTU == nil {
		t.Fatal("payload DatagramMTU = nil, want provider from sysnet capabilities")
	}
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("wrapped network = %T %p, want base %p", got, got, base)
	}
	first := payload.Network

	w.workspace.EmitEvent(receiver.ID, link, std.RequestValue{})
	payload = networktest.ReceivePayload(t, receiver.Events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("request wrapped network = %T %p, want base %p", got, got, base)
	}
	if nodekit.SameNetworkInstance(payload.Network, first) {
		t.Fatal("request reused the previous network wrapper")
	}
	if nodekit.NetworkIsReady(first) {
		t.Fatal("request left the previous network wrapper ready")
	}
}

func TestOutNetworkDoesNotInferMTUWhenSysnetOperationsAreUnavailable(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	system := &sysnetdebug.System{OutNetwork: base}
	report := system.Capabilities()
	for i := range report.Operations {
		key := report.Operations[i].Key
		if key.Target == sysnet.TargetOutNet &&
			(key.Operation == sysnet.OpInterfaces || key.Operation == sysnet.OpDialUDP) {
			report.Operations[i].Capability = sysnet.Capability{
				State:   sysnet.CapabilityUnavailable,
				Reasons: []sysnet.CapabilityReason{sysnet.ReasonNoUnderlay},
			}
		}
	}
	if err := system.SetCapabilityReport(report); err != nil {
		t.Fatalf("SetCapabilityReport: %v", err)
	}
	attachSystemNetworkMTUProvider(system, base, sysnet.TargetOutNet)
	if provider := links.DatagramMTUProviderFromNetwork(base); provider != nil {
		t.Fatalf("DatagramMTUProviderFromNetwork() = %T, want nil", provider)
	}
}

func TestOutNetworkSendsSystemOutNetToNewLinks(t *testing.T) {
	w := newOutNetworkTestWorkspace(t, gonnect.NewLoopbackNetwork())
	base := w.runtime.sys.(*sysnetdebug.System).OutNetwork
	outID := addOutNetworkTestNode(t, w.workspace)
	out := testkit.MustNodeSnapshot(t, w.workspace, outID)

	receiver := networktest.NewReceiver(2)
	if _, err := w.workspace.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	if _, _, err := w.workspace.AddLink(out.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	payload := networktest.ReceivePayload(t, receiver.Events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("wrapped network = %T %p, want base %p", got, got, base)
	}
}

func TestOutNetworkWithoutSystemOutNetDoesNotSend(t *testing.T) {
	w := newOutNetworkTestWorkspace(t, nil)
	outID := addOutNetworkTestNode(t, w.workspace)
	out := testkit.MustNodeSnapshot(t, w.workspace, outID)

	receiver := networktest.NewReceiver(2)
	if _, err := w.workspace.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.workspace.AddLink(out.LeftPorts[0], receiver.In)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	networktest.AssertNoEvent(t, receiver.Events)
	w.workspace.EmitEvent(receiver.ID, link, std.RequestValue{})
	networktest.AssertNoEvent(t, receiver.Events)
}

type outNetworkTestWorkspace struct {
	workspace *pasta.Workspace
	runtime   outNetworkTestRuntime
}

func newOutNetworkTestWorkspace(t *testing.T, base gonnect.Network) outNetworkTestWorkspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	runtime := outNetworkTestRuntime{sys: &sysnetdebug.System{OutNetwork: base}}
	if base == nil {
		runtime.sys = nil
	}
	if err := w.AddNodeClass(OutNetworkClass{Runtime: runtime}); err != nil {
		t.Fatalf("AddNodeClass OutNetwork: %v", err)
	}
	return outNetworkTestWorkspace{workspace: w, runtime: runtime}
}

func addOutNetworkTestNode(t *testing.T, w *pasta.Workspace) uint64 {
	t.Helper()
	outID, err := w.AddNodeByClass(NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutNetwork: %v", err)
	}
	return outID
}
