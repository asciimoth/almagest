package lib

import (
	"context"
	"testing"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

const testSysBuildNodeClassName = "test/SysBuildNode"

type recordingSysBuilder struct {
	*sysnet.DebugSysBuilder
	builds []sysnet.BuildOpts
}

func newRecordingSysBuilder() *recordingSysBuilder {
	return &recordingSysBuilder{
		DebugSysBuilder: sysnet.NewDebugSysBuilder(),
	}
}

func (r *recordingSysBuilder) Build(opts sysnet.BuildOpts) (*sysnet.System, error) {
	r.builds = append(r.builds, opts)
	return r.DebugSysBuilder.Build(opts)
}

type testSysBuildClass struct {
	inbox chan pasta.InboxMessage
}

func (testSysBuildClass) ClassName() string        { return testSysBuildNodeClassName }
func (testSysBuildClass) ShortDescription() string { return "sys build test node" }
func (testSysBuildClass) LongDescription() string  { return "sys build test node" }
func (testSysBuildClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}
func (c testSysBuildClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testSysBuildNode{inbox: c.inbox}, nil
}

type testSysBuildNode struct {
	pasta.BasicNode
	inbox chan pasta.InboxMessage
}

func (n *testSysBuildNode) OnInbox(message pasta.InboxMessage) error {
	n.inbox <- message
	return nil
}

func TestBuildEngineRunsInitialSysBuild(t *testing.T) {
	pool := bufpool.NewTestDebugPool(t)
	defer pool.Close()

	sys := newRecordingSysBuilder()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1024),
		sys,
		nil,
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	}()

	if len(sys.builds) != 1 {
		t.Fatalf("sys build count = %d, want 1", len(sys.builds))
	}
}

func TestRequestSysRebuildSendsResultToSubscribers(t *testing.T) {
	pool := bufpool.NewTestDebugPool(t)
	defer pool.Close()

	sys := newRecordingSysBuilder()
	inbox := make(chan pasta.InboxMessage, 1)
	engineIface, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1024),
		sys,
		[]pasta.NodeClass{testSysBuildClass{inbox: inbox}},
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	defer func() {
		if err := engineIface.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	}()

	engine := engineIface.(*engine)
	node, err := engine.AddNode("main", testSysBuildNodeClassName, "subscriber")
	if err != nil {
		t.Fatalf("AddNode error = %v, want nil", err)
	}

	engine.SubscribeSysBuild("main", node)
	engine.Lock()
	engine.SysBuildOpts.TunNeeded = true
	engine.SysBuildOpts.TunAddrs = []string{"10.0.0.2/32"}
	engine.SysBuildOpts.Exclude = []sysnet.Rule{{Type: "app", Rule: "example"}}
	engine.Unlock()

	if err := engine.RequestSysRebuild(); err != nil {
		t.Fatalf("RequestSysRebuild error = %v, want nil", err)
	}

	select {
	case message := <-inbox:
		payload, ok := message.Payload.(SysBuildInboxMessage)
		if !ok {
			t.Fatalf("payload type = %T, want SysBuildInboxMessage", message.Payload)
		}
		if payload.Initial {
			t.Fatal("payload.Initial = true, want false")
		}
		if payload.Err != nil {
			t.Fatalf("payload.Err = %v, want nil", payload.Err)
		}
		if payload.System == nil {
			t.Fatal("payload.System = nil, want system")
		}
		if !payload.Opts.TunNeeded {
			t.Fatal("payload.Opts.TunNeeded = false, want true")
		}
		if len(payload.Opts.TunAddrs) != 1 || payload.Opts.TunAddrs[0] != "10.0.0.2/32" {
			t.Fatalf("payload.Opts.TunAddrs = %v, want [10.0.0.2/32]", payload.Opts.TunAddrs)
		}
		if len(payload.Opts.Exclude) != 1 || payload.Opts.Exclude[0].Rule != "example" {
			t.Fatalf("payload.Opts.Exclude = %v, want example rule", payload.Opts.Exclude)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sys build inbox message")
	}

	if len(sys.builds) != 2 {
		t.Fatalf("sys build count = %d, want 2", len(sys.builds))
	}
}
