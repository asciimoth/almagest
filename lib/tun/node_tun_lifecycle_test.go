package tunnodes

import (
	"context"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/tuntest"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunRapidRewireIgnoresStaleConfigCompletion(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	sys := tuntest.NewRecordingSystem()
	sys.DisableDynTun = true
	runtime := tunTestRuntime{ctx: context.Background(), sys: sys}
	provider := &tunLifecycleTestNode{tunNode: &tunNode{
		NodeBase: NodeBase{Runtime: runtime},
		config:   defaultTunNodeConfig(),
	}}
	providerID, err := w.AddNode(provider, NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNode Tun: %v", err)
	}

	first := tuntest.NewReceiver(2)
	firstID, err := w.AddNode(first, "test/TunReceiver", "first")
	if err != nil {
		t.Fatalf("AddNode first receiver: %v", err)
	}
	oldLink, _, err := w.AddLink(first.In, testkit.MustNodeSnapshot(t, w, providerID).RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink first: %v", err)
	}
	_ = tuntest.ReceivePayload(t, first.Events)
	w.EmitEvent(firstID, oldLink, TunConfig{Addrs: []string{"10.220.0.2/32"}})

	w.RemoveLink(oldLink)
	second := tuntest.NewReceiver(2)
	secondID, err := w.AddNode(second, "test/TunReceiver", "second")
	if err != nil {
		t.Fatalf("AddNode second receiver: %v", err)
	}
	newLink, _, err := w.AddLink(second.In, provider.tunp)
	if err != nil {
		t.Fatalf("AddLink second: %v", err)
	}
	_ = tuntest.ReceivePayload(t, second.Events)
	w.EmitEvent(secondID, newLink, TunConfig{Addrs: []string{"10.221.0.2/32"}})
	wantBuilds := sys.BuildCount()

	w.Lock()
	err = provider.OnEvent(pasta.Event{
		Link:         oldLink,
		ReceiverPort: provider.tunp,
		Payload:      TunConfig{Addrs: []string{"10.222.0.2/32"}},
	}, TypeTun, nil, "right")
	w.Unlock()
	if err != nil {
		t.Fatalf("OnEvent stale config: %v", err)
	}
	if got := sys.BuildCount(); got != wantBuilds {
		t.Fatalf("build count after stale config = %d, want %d", got, wantBuilds)
	}
	if got, want := sys.LastOpts().TunAddrs, []string{"10.221.0.2/32"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effective addresses after stale config = %#v, want %#v", got, want)
	}
}

type tunLifecycleTestNode struct {
	*tunNode
}

func (n *tunLifecycleTestNode) OnInit(
	w *pasta.Workspace,
	logger pasta.Logger,
	id uint64,
	name string,
	_ *pasta.NodeInitData,
	a, b, c, d bool,
) error {
	params := TunPort("right", "Tun")
	params.Node = id
	port, err := w.AddPortLocked(params)
	if err != nil {
		return err
	}
	return n.tunNode.OnInit(w, logger, id, name, &pasta.NodeInitData{
		RightPorts: []uint64{port},
	}, a, b, c, d)
}
