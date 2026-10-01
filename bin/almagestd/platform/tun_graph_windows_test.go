//go:build windows

package platform

import (
	"context"
	"reflect"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"golang.org/x/sys/windows"
)

func TestWindowsGraphRegularTunLifecycle(t *testing.T) {
	engine, system, receiver := newWindowsTunGraphEngine(t)
	node, err := engine.AddNode(almagest.MainWorkspace, tunnodes.NodeTypeTun, "native tun")
	if err != nil {
		t.Fatalf("AddNode Tun: %v", err)
	}
	receiverID, err := engine.AddNode(almagest.MainWorkspace, receiver.ClassName(), "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	linkWindowsTunGraphNodes(t, engine, node, receiverID)
	payload := receiveWindowsTunPayload(t, receiver.events)
	source, ok := nodekit.TunSource(payload.Tun).(tun.Tun)
	if !ok {
		t.Fatalf("regular TUN source = %T, want tun.Tun", nodekit.TunSource(payload.Tun))
	}

	applyWindowsTunGraphForm(t, engine, node, map[string]any{
		"tunAddrs":  "198.18.230.1/32",
		"tunRoutes": "198.19.230.0/24",
		"mtu":       1380,
	})
	waitWindowsTunAddrs(t, system, source, []string{"198.18.230.1/32"})
	applyWindowsTunGraphForm(t, engine, node, map[string]any{
		"tunAddrs":  "198.18.230.1/32",
		"tunRoutes": "198.19.230.0/24",
		"mtu":       1380,
	})
	waitWindowsTunAddrs(t, system, source, []string{"198.18.230.1/32"})
}

func TestWindowsGraphDefaultTunRebuildLifecycle(t *testing.T) {
	engine, system, receiver := newWindowsTunGraphEngine(t)
	node, err := engine.AddNode(almagest.MainWorkspace, tunnodes.NodeTypeDefaultTun, "native default tun")
	if err != nil {
		t.Fatalf("AddNode DefaultTun: %v", err)
	}
	receiverID, err := engine.AddNode(almagest.MainWorkspace, receiver.ClassName(), "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	linkWindowsTunGraphNodes(t, engine, node, receiverID)
	first := receiveWindowsTunPayload(t, receiver.events)

	applyWindowsTunGraphForm(t, engine, node, map[string]any{
		"tunAddrs": "198.18.231.1/32",
		"mtu":      1370,
		"dnsIP":    "198.18.231.1",
	})
	replacement := receiveWindowsTunPayload(t, receiver.events)
	replacementSource, ok := nodekit.TunSource(replacement.Tun).(tun.Tun)
	if !ok {
		t.Fatalf("DefaultTun source = %T, want tun.Tun", nodekit.TunSource(replacement.Tun))
	}
	if nodekit.SameTunInstance(first.Tun, replacement.Tun) {
		t.Fatal("DefaultTun address, DNS-address, and MTU change reused the retired output")
	}
	waitWindowsTunAddrs(t, system, replacementSource, []string{"198.18.231.1/32"})
	if updown, ok := first.Tun.(interface{ IsUp() (bool, error) }); ok {
		if up, _ := updown.IsUp(); up {
			t.Fatal("retired DefaultTun output still reports up")
		}
	}
}

func newWindowsTunGraphEngine(t *testing.T) (almagest.Engine, sysnet.System, *windowsTunReceiverClass) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native graph TUN lifecycle requires an elevated test identity")
	}
	system, err := SystemIntegration(configer.NewMemory(nil), nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	receiver := &windowsTunReceiverClass{events: make(chan links.TunPayload, 8)}
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1024),
		system, nil, nil, nil, nil, []pasta.NodeClass{receiver}, nil, false,
	)
	if err != nil {
		_ = system.Close()
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return engine, system, receiver
}

type windowsTunReceiverClass struct {
	events chan links.TunPayload
}

func (*windowsTunReceiverClass) ClassName() string        { return "test/WindowsTunReceiver" }
func (*windowsTunReceiverClass) ShortDescription() string { return "Windows TUN test receiver" }
func (*windowsTunReceiverClass) LongDescription() string {
	return "Receives native TUN graph values for lifecycle tests."
}
func (*windowsTunReceiverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{nodekit.TunPort("left", "Tun")}}
}
func (c *windowsTunReceiverClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &windowsTunReceiverNode{events: c.events}, nil
}

type windowsTunReceiverNode struct {
	pasta.BasicNode
	in     uint64
	events chan links.TunPayload
}

func (n *windowsTunReceiverNode) OnInit(_ *pasta.Workspace, _ pasta.Logger, _ uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.in = restored.LeftPorts[0]
	return nil
}
func (n *windowsTunReceiverNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if event.ReceiverPort == n.in && direction == "left" && linkType == links.TypeTun {
		if payload, ok := event.Payload.(links.TunPayload); ok {
			n.events <- payload
		}
	}
	return nil
}

func linkWindowsTunGraphNodes(t *testing.T, engine almagest.Engine, provider, receiver uint64) {
	t.Helper()
	providerSnapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, provider)
	if !ok {
		t.Fatal("provider snapshot missing")
	}
	receiverSnapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, receiver)
	if !ok {
		t.Fatal("receiver snapshot missing")
	}
	if _, _, err := engine.AddLink(almagest.MainWorkspace, receiverSnapshot.LeftPorts[0], providerSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
}

func receiveWindowsTunPayload(t *testing.T, events <-chan links.TunPayload) links.TunPayload {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case payload := <-events:
			if payload.Tun != nil {
				return payload
			}
		case <-timer.C:
			t.Fatal("timed out waiting for native TUN payload")
		}
	}
}

func applyWindowsTunGraphForm(t *testing.T, engine almagest.Engine, node uint64, values map[string]any) {
	t.Helper()
	engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values:      values,
	})
}

func waitWindowsTunAddrs(t *testing.T, system sysnet.System, source tun.Tun, want []string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := system.GetTunAddrs(source); err == nil && reflect.DeepEqual(got, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := system.GetTunAddrs(source)
	t.Fatalf("TUN addresses = %#v, %v; want %#v, nil", got, err, want)
}
