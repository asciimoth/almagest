package almagest

import (
	"context"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const testTimeTriggerSinkNodeType = "test/TimeTriggerSink"

func TestTimeTriggerNodeClassShapeAndMenuDefaults(t *testing.T) {
	e := NewNodeE2E(t)
	class, ok := e.Engine.NodeClassSnapshot(E2EWorkspace, NodeTypeTimeTrigger)
	if !ok {
		t.Fatal("TimeTrigger class missing")
	}
	if class.PrimaryType != std.TypeTrigger {
		t.Fatalf("primary type = %q, want %q", class.PrimaryType, std.TypeTrigger)
	}
	if len(class.InitialPorts) != 1 || class.InitialPorts[0].Direction != "right" || class.InitialPorts[0].Types[0] != std.TypeTrigger {
		t.Fatalf("initial ports = %#v, want one right-directed pasta/trigger port", class.InitialPorts)
	}

	node := e.AddNode(NodeTypeTimeTrigger, "timer")
	e.SubscribeMenus(node)
	e.assertTimeTriggerField(node, "mode", timeTriggerModeDisabled)
	e.assertTimeTriggerField(node, "interval", defaultTimeTriggerInterval)
	e.assertTimeTriggerField(node, "dailyTime", defaultTimeTriggerDailyTime)
	e.assertTimeTriggerField(node, "emitOnReady", false)
}

func TestTimeTriggerNodeValidationApplyAndSave(t *testing.T) {
	e := NewNodeE2E(t)
	node := e.AddNode(NodeTypeTimeTrigger, "timer")
	e.SubscribeMenus(node)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldValidateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldValidate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "interval"},
		Value:       "0s",
	})
	e.assertTimeTriggerFieldStatus(node, "interval", formular.StatusError)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"mode":        timeTriggerModeInterval,
			"interval":    "30ms",
			"dailyTime":   "00:00",
			"emitOnReady": true,
		},
	})
	e.assertTimeTriggerField(node, "mode", timeTriggerModeInterval)
	e.assertTimeTriggerField(node, "interval", "30ms")
	e.assertTimeTriggerField(node, "emitOnReady", true)

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for key, want := range map[string]any{
		"mode":        timeTriggerModeInterval,
		"interval":    "30ms",
		"dailyTime":   "00:00",
		"emitOnReady": true,
	} {
		got, err := cfg.Get(configer.Path{"nodes", "timer", key})
		if err != nil {
			t.Fatalf("saved key %s missing: %v", key, err)
		}
		if got != want {
			t.Fatalf("saved key %s = %#v, want %#v", key, got, want)
		}
	}
}

func TestTimeTriggerNodeWorkerIsLazyAndTriggersAttachDetachReattach(t *testing.T) {
	sinks := newTimeTriggerSinkRegistry()
	e := newTimeTriggerTestE2E(t, sinks)

	timer := e.AddNode(NodeTypeTimeTrigger, "timer")
	sinkID := e.AddNode(testTimeTriggerSinkNodeType, "sink")
	sink := sinks.sink(t, sinkID)
	link := e.MustAddTypedLinkByPortName(std.TypeTrigger, timer, "right", "Trigger", sinkID, "left", "Trigger")
	assertNoTimeTriggerWorkers(t, e.Engine)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, timer, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, timer),
		BlockID:     "config",
		Values: map[string]any{
			"mode":      timeTriggerModeInterval,
			"interval":  "20ms",
			"dailyTime": "00:00",
		},
	})
	assertTimeTriggerWorker(t, e.Engine, timer)
	sink.receive(t, "initial interval trigger")

	e.Engine.RemoveLink(E2EWorkspace, link)
	sink.assertNoTrigger(t, 70*time.Millisecond, "detached link")

	e.MustAddTypedLinkByPortName(std.TypeTrigger, timer, "right", "Trigger", sinkID, "left", "Trigger")
	sink.receive(t, "reattached interval trigger")
}

func TestTimeTriggerNodeEmitOnReadySaveRestore(t *testing.T) {
	sinks := newTimeTriggerSinkRegistry()
	cfg := configer.NewMemory(map[string]any{"nodes": map[string]any{
		"timer": map[string]any{
			"Class":       NodeTypeTimeTrigger,
			"Links":       []any{"Trigger -> [sink] Trigger"},
			"emitOnReady": true,
		},
		"sink": map[string]any{
			"Class": testTimeTriggerSinkNodeType,
		},
	}})
	engine := buildTimeTriggerTestEngine(t, cfg, sinks)
	sinkID := nodeIDByName(t, engine, "sink")
	sinks.sink(t, sinkID).receive(t, "OnReady trigger after restore")
	assertNoTimeTriggerWorkers(t, engine)
}

func TestTimeTriggerNodeCopyPastePreservesTimerConfigAndInternalLinks(t *testing.T) {
	sinks := newTimeTriggerSinkRegistry()
	e := newTimeTriggerTestE2E(t, sinks)

	timer := e.AddNode(NodeTypeTimeTrigger, "timer")
	sinkID := e.AddNode(testTimeTriggerSinkNodeType, "sink")
	e.MustAddTypedLinkByPortName(std.TypeTrigger, timer, "right", "Trigger", sinkID, "left", "Trigger")
	e.Engine.SendNodeFormularMsg(E2EWorkspace, timer, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, timer),
		BlockID:     "config",
		Values: map[string]any{
			"mode":      timeTriggerModeInterval,
			"interval":  "20ms",
			"dailyTime": "00:00",
		},
	})
	sinks.sink(t, sinkID).receive(t, "original trigger")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{timer, sinkID})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 2 {
		t.Fatalf("Paste returned %d nodes (%v), want 2", len(pasted), pasted)
	}
	pastedTimer := pastedNodeByClass(t, e.Engine, pasted, NodeTypeTimeTrigger)
	pastedSink := pastedNodeByClass(t, e.Engine, pasted, testTimeTriggerSinkNodeType)
	assertTimeTriggerWorker(t, e.Engine, pastedTimer)
	sinks.sink(t, pastedSink).receive(t, "pasted trigger")
}

func TestTimeTriggerScheduleHelpers(t *testing.T) {
	if _, err := parseTimeTriggerDailyTime("24:00"); err == nil {
		t.Fatal("parseTimeTriggerDailyTime accepted 24:00")
	}
	seconds, err := parseTimeTriggerDailyTime("00:00:30")
	if err != nil {
		t.Fatalf("parseTimeTriggerDailyTime: %v", err)
	}
	if seconds != 30 {
		t.Fatalf("seconds = %d, want 30", seconds)
	}

	now := time.Date(2026, 7, 16, 23, 59, 59, 0, time.UTC)
	if got := nextTimeTriggerDailyDelay(now, 0); got != time.Second {
		t.Fatalf("next daily delay = %v, want 1s", got)
	}
	now = time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	if got := nextTimeTriggerDailyDelay(now, 0); got != 24*time.Hour {
		t.Fatalf("next daily delay at exact target = %v, want 24h", got)
	}
}

func newTimeTriggerTestE2E(t *testing.T, sinks *timeTriggerSinkRegistry) *NodeE2E {
	t.Helper()
	engine := buildTimeTriggerTestEngine(t, configer.NewMemory(nil), sinks)
	return &NodeE2E{T: t, Engine: engine, LogFactory: NewDebugLogFactory(1), DebugSystem: newE2EDebugSystem(), menus: formular.NewMenuSnapshotState()}
}

func buildTimeTriggerTestEngine(t *testing.T, cfg configer.Config, sinks *timeTriggerSinkRegistry) Engine {
	t.Helper()
	engine, err := BuildEngine(
		context.Background(),
		cfg,
		NewDebugLogFactory(1024),
		newE2EDebugSystem(),
		nil,
		nil, nil,

		[]pasta.NodeClass{timeTriggerSinkClass{registry: sinks}},
		nil,
		true)

	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Close()
	})
	return engine
}

func assertNoTimeTriggerWorkers(t *testing.T, engine Engine) {
	t.Helper()
	workers, ok := engine.WorkersSnapshot(E2EWorkspace)
	if !ok {
		t.Fatal("WorkersSnapshot returned ok=false")
	}
	for _, worker := range workers {
		if worker.Class == NodeTypeTimeTrigger {
			t.Fatalf("unexpected time trigger worker: %#v", worker)
		}
	}
}

func assertTimeTriggerWorker(t *testing.T, engine Engine, node uint64) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		workers, ok := engine.WorkersSnapshot(E2EWorkspace)
		if !ok {
			t.Fatal("WorkersSnapshot returned ok=false")
		}
		for _, worker := range workers {
			if worker.Node == node && worker.Class == NodeTypeTimeTrigger && worker.WorkerName == "time trigger" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("time trigger worker for node %d did not start", node)
}

func pastedNodeByClass(t *testing.T, engine Engine, ids []uint64, class string) uint64 {
	t.Helper()
	for _, id := range ids {
		snapshot, ok := engine.NodeSnapshot(E2EWorkspace, id)
		if ok && snapshot.Class == class {
			return id
		}
	}
	t.Fatalf("pasted node with class %s missing in %v", class, ids)
	return 0
}

func nodeIDByName(t *testing.T, engine Engine, name string) uint64 {
	t.Helper()
	snapshot, ok := engine.WorkspaceSnapshot(E2EWorkspace)
	if !ok {
		t.Fatal("WorkspaceSnapshot returned ok=false")
	}
	for id, node := range snapshot.Nodes {
		if node.Name == name {
			return id
		}
	}
	t.Fatalf("node named %q missing", name)
	return 0
}

func (e *NodeE2E) assertTimeTriggerField(node uint64, field string, want any) {
	e.T.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	snapshot, ok := e.menus.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		e.T.Fatal("menu snapshot missing")
	}
	item, ok := FindItem(mustFindBlock(e.T, snapshot, "config"), field)
	if !ok {
		e.T.Fatalf("%s item missing", field)
	}
	if item.Value != want {
		e.T.Fatalf("%s value = %#v, want %#v", field, item.Value, want)
	}
}

func (e *NodeE2E) assertTimeTriggerFieldStatus(node uint64, field, want string) {
	e.T.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	snapshot, ok := e.menus.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		e.T.Fatal("menu snapshot missing")
	}
	item, ok := FindItem(mustFindBlock(e.T, snapshot, "config"), field)
	if !ok {
		e.T.Fatalf("%s item missing", field)
	}
	if item.Status != want {
		e.T.Fatalf("%s status = %q, want %q", field, item.Status, want)
	}
}

type timeTriggerSinkRegistry struct {
	nodes map[uint64]*timeTriggerSinkNode
}

func newTimeTriggerSinkRegistry() *timeTriggerSinkRegistry {
	return &timeTriggerSinkRegistry{nodes: make(map[uint64]*timeTriggerSinkNode)}
}

func (r *timeTriggerSinkRegistry) add(id uint64, node *timeTriggerSinkNode) {
	r.nodes[id] = node
}

func (r *timeTriggerSinkRegistry) sink(t *testing.T, id uint64) *timeTriggerSinkNode {
	t.Helper()
	node := r.nodes[id]
	if node == nil {
		t.Fatalf("sink node %d missing", id)
	}
	return node
}

type timeTriggerSinkClass struct {
	registry *timeTriggerSinkRegistry
}

func (timeTriggerSinkClass) ClassName() string        { return testTimeTriggerSinkNodeType }
func (timeTriggerSinkClass) ShortDescription() string { return "test trigger sink" }
func (timeTriggerSinkClass) LongDescription() string {
	return "Records pasta/trigger events for tests."
}
func (timeTriggerSinkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{
		{Direction: "left", Name: "Trigger", Types: []string{std.TypeTrigger}},
	}}
}
func (c timeTriggerSinkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &timeTriggerSinkNode{registry: c.registry, events: make(chan struct{}, 32)}, nil
}

type timeTriggerSinkNode struct {
	pasta.BasicNode

	registry *timeTriggerSinkRegistry
	in       uint64
	events   chan struct{}
}

func (n *timeTriggerSinkNode) OnInit(_ *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.in = restoredLeft(restored, 0)
	if n.registry != nil {
		n.registry.add(id, n)
	}
	return nil
}

func (n *timeTriggerSinkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "left" || linkType != std.TypeTrigger {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *timeTriggerSinkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && linkType == std.TypeTrigger && !std.IsRequest(event.Payload) {
		n.events <- struct{}{}
	}
	return nil
}

func (n *timeTriggerSinkNode) receive(t *testing.T, reason string) {
	t.Helper()
	select {
	case <-n.events:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("no trigger received for %s", reason)
	}
}

func (n *timeTriggerSinkNode) assertNoTrigger(t *testing.T, d time.Duration, reason string) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-n.events:
		t.Fatalf("unexpected trigger received for %s", reason)
	case <-timer.C:
	}
}
