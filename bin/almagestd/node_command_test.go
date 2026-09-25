package main

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	almagest "github.com/asciimoth/almagest/lib"
	experimentalnodes "github.com/asciimoth/almagest/lib/experimental"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestDaemonNodeClassesRequireExperimentalOptIn(t *testing.T) {
	host := experimentalnodes.XTorDriverHostConfig{TorExecutable: "/test/tor"}
	disabled := daemonNodeClasses(cmdrunner.Config{}, nil, false, ProtectedExperimentalConfig{TorDriver: host})
	if len(disabled) != 1 || disabled[0].ClassName() != NodeTypeProtectedCommand {
		t.Fatalf("classes without experimental opt-in = %#v, want only %q", disabled, NodeTypeProtectedCommand)
	}

	enabled := daemonNodeClasses(cmdrunner.Config{}, nil, true, ProtectedExperimentalConfig{TorDriver: host})
	if len(enabled) != 2 || enabled[1].ClassName() != experimentalnodes.NodeTypeXTorDriver {
		t.Fatalf("classes with experimental opt-in = %#v, want command and XTorDriver", enabled)
	}
	torClass, ok := enabled[1].(experimentalnodes.XTorDriverClass)
	if !ok || !reflect.DeepEqual(torClass.Host, host) {
		t.Fatalf("XTorDriver class = %#v, want host config %#v", enabled[1], host)
	}
	if got := daemonExperimentalNodeTypes(false); len(got) != 0 {
		t.Fatalf("experimental API types without opt-in = %#v, want empty", got)
	}
	if got := daemonExperimentalNodeTypes(true); len(got) != 1 || !got[experimentalnodes.NodeTypeXTorDriver] {
		t.Fatalf("experimental API types with opt-in = %#v, want XTorDriver", got)
	}
}

func TestCommandNodeClassShapeAndMenuFiltersAllowNodes(t *testing.T) {
	engine := buildCommandNodeTestEngine(t, configer.NewMemory(nil), cmdrunner.Config{Commands: map[string]cmdrunner.AllowedCommand{
		"visible": commandNodeTestAllowedCommand(true, "value"),
		"hidden":  commandNodeTestAllowedCommand(false, "secret"),
	}}, nil)

	class, ok := engine.NodeClassSnapshot(almagest.MainWorkspace, NodeTypeProtectedCommand)
	if !ok {
		t.Fatal("command node class missing")
	}
	if class.PrimaryType != std.TypeTrigger {
		t.Fatalf("primary type = %q, want %q", class.PrimaryType, std.TypeTrigger)
	}
	if len(class.InitialPorts) != 1 || class.InitialPorts[0].Direction != "left" || class.InitialPorts[0].Name != commandNodeTriggerPort {
		t.Fatalf("initial ports = %#v, want one left Trigger port", class.InitialPorts)
	}

	node := addCommandNode(t, engine, "cmd")
	menus := subscribeCommandNodeMenu(t, engine, node)
	item := waitCommandNodeMenuItem(t, menus, node, commandNodeConfigBlockID, commandNodeCommandField)
	if got, want := item.AllowedValues, []any{"visible"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("menu command choices = %#v, want %#v", got, want)
	}
	if item.Value != "" {
		t.Fatalf("menu command value = %#v, want empty", item.Value)
	}
}

func TestCommandNodeSelectingCommandRebuildsInputPorts(t *testing.T) {
	engine := buildCommandNodeTestEngine(t, configer.NewMemory(nil), cmdrunner.Config{Commands: map[string]cmdrunner.AllowedCommand{
		"alpha": commandNodeTestAllowedCommand(true, "_source", "target_"),
		"beta":  commandNodeTestAllowedCommand(true, "path"),
	}}, nil)

	node := addCommandNode(t, engine, "cmd")
	assertCommandNodeLeftPorts(t, engine, node, []string{commandNodeTriggerPort})
	assertCommandNodeRightPortCount(t, engine, node, 0)

	applyCommandNodeCommand(t, engine, node, "alpha")
	assertCommandNodeLeftPorts(t, engine, node, []string{
		commandNodeTriggerPort,
		commandNodeSlotPortName("_source"),
		commandNodeSlotPortName("target_"),
	})
	assertCommandNodeRightPortCount(t, engine, node, 0)

	applyCommandNodeCommand(t, engine, node, "beta")
	assertCommandNodeLeftPorts(t, engine, node, []string{
		commandNodeTriggerPort,
		commandNodeSlotPortName("path"),
	})
	assertCommandNodeRightPortCount(t, engine, node, 0)
}

func TestCommandNodeTriggerSubmitsSelectedCommandWithStringInputs(t *testing.T) {
	dispatcher := &recordingCommandDispatcher{accept: true}
	engine := buildCommandNodeTestEngine(t, configer.NewMemory(nil), cmdrunner.Config{Commands: map[string]cmdrunner.AllowedCommand{
		"notify": commandNodeTestAllowedCommand(true, "message", "optional"),
	}}, dispatcher)

	cmd := addCommandNode(t, engine, "cmd")
	applyCommandNodeCommand(t, engine, cmd, "notify")

	message := addStdNode(t, engine, std.NodeTypeStringConstant, "message")
	applyStdConstantValue(t, engine, message, "hello from graph")
	mustAddTypedLinkByPortName(t, engine, std.TypeString, message, "right", "output", cmd, "left", commandNodeSlotPortName("message"))

	trigger := addStdNode(t, engine, std.NodeTypeTrigger, "trigger")
	mustAddTypedLinkByPortName(t, engine, std.TypeTrigger, trigger, "right", commandNodeTriggerPort, cmd, "left", commandNodeTriggerPort)

	if err := engine.Trigger(almagest.MainWorkspace, trigger); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	req := dispatcher.waitRequest(t)
	if req.Name != "notify" {
		t.Fatalf("request name = %q, want notify", req.Name)
	}
	if got := req.Slots["message"]; got != "hello from graph" {
		t.Fatalf("message slot = %q, want graph value", got)
	}
	if _, ok := req.Slots["optional"]; ok {
		t.Fatalf("optional slot was submitted without an input: %#v", req.Slots)
	}
}

func TestCommandNodeCopyPasteAndSaveRestorePreserveCommandPorts(t *testing.T) {
	commandCfg := cmdrunner.Config{Commands: map[string]cmdrunner.AllowedCommand{
		"notify": commandNodeTestAllowedCommand(true, "message"),
	}}
	engine := buildCommandNodeTestEngine(t, configer.NewMemory(nil), commandCfg, nil)
	cmd := addCommandNode(t, engine, "cmd")
	applyCommandNodeCommand(t, engine, cmd, "notify")

	clip := engine.Copy(almagest.MainWorkspace, []uint64{cmd})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := engine.Paste(almagest.MainWorkspace, clip)
	pastedCmd := commandNodePastedByClass(t, engine, pasted, NodeTypeProtectedCommand)
	assertCommandNodeLeftPorts(t, engine, pastedCmd, []string{
		commandNodeTriggerPort,
		commandNodeSlotPortName("message"),
	})
	assertCommandNodeLabel(t, engine, pastedCmd, "notify")

	saved := configer.NewMemory(nil)
	if err := engine.SaveConfig(saved); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got, err := saved.Get(configer.Path{"nodes", "cmd", commandNodeCommandField}); err != nil || got != "notify" {
		t.Fatalf("saved command = %#v, %v; want notify", got, err)
	}

	restored := buildCommandNodeTestEngine(t, saved, commandCfg, nil)
	restoredCmd := commandNodeByName(t, restored, "cmd")
	assertCommandNodeLeftPorts(t, restored, restoredCmd, []string{
		commandNodeTriggerPort,
		commandNodeSlotPortName("message"),
	})
	assertCommandNodeLabel(t, restored, restoredCmd, "notify")
}

func TestCommandNodePasteAndRestoreReconcileChangedCommandTemplate(t *testing.T) {
	oldCfg := cmdrunner.Config{Commands: map[string]cmdrunner.AllowedCommand{
		"notify": commandNodeTestAllowedCommand(true, "message"),
	}}
	source := buildCommandNodeTestEngine(t, configer.NewMemory(nil), oldCfg, nil)
	cmd := addCommandNode(t, source, "cmd")
	applyCommandNodeCommand(t, source, cmd, "notify")
	message := addStdNode(t, source, std.NodeTypeStringConstant, "message")
	mustAddTypedLinkByPortName(t, source, std.TypeString, message, "right", "output", cmd, "left", commandNodeSlotPortName("message"))

	saved := configer.NewMemory(nil)
	if err := source.SaveConfig(saved); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	clip := source.Copy(almagest.MainWorkspace, []uint64{cmd, message})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}

	newCfg := cmdrunner.Config{Commands: map[string]cmdrunner.AllowedCommand{
		"notify": commandNodeTestAllowedCommand(true, "other"),
	}}
	restored := buildCommandNodeTestEngine(t, saved, newCfg, nil)
	restoredCmd := commandNodeByName(t, restored, "cmd")
	assertCommandNodeLeftPorts(t, restored, restoredCmd, []string{
		commandNodeTriggerPort,
		commandNodeSlotPortName("other"),
	})
	assertCommandNodeHasNoPort(t, restored, restoredCmd, commandNodeSlotPortName("message"))
	assertCommandNodeHasNoLinks(t, restored, restoredCmd)

	target := buildCommandNodeTestEngine(t, configer.NewMemory(nil), newCfg, nil)
	pasted := target.Paste(almagest.MainWorkspace, clip)
	pastedCmd := commandNodePastedByClass(t, target, pasted, NodeTypeProtectedCommand)
	assertCommandNodeLeftPorts(t, target, pastedCmd, []string{
		commandNodeTriggerPort,
		commandNodeSlotPortName("other"),
	})
	assertCommandNodeHasNoPort(t, target, pastedCmd, commandNodeSlotPortName("message"))
	assertCommandNodeHasNoLinks(t, target, pastedCmd)
}

func TestCommandNodeRestoreMissingCommandIsUnavailableAndDoesNotDispatch(t *testing.T) {
	dispatcher := &recordingCommandDispatcher{accept: true}
	mainCfg := configer.NewMemory(map[string]any{
		"nodes": map[string]any{
			"cmd": map[string]any{
				"Class":                 NodeTypeProtectedCommand,
				commandNodeCommandField: "gone",
			},
		},
	})
	engine := buildCommandNodeTestEngine(t, mainCfg, cmdrunner.Config{}, dispatcher)
	cmd := commandNodeByName(t, engine, "cmd")
	assertCommandNodeLeftPorts(t, engine, cmd, []string{commandNodeTriggerPort})
	assertCommandNodeLabel(t, engine, cmd, "unavailable command")

	if err := engine.Trigger(almagest.MainWorkspace, cmd); err != nil {
		t.Fatalf("Trigger command node: %v", err)
	}
	if got := dispatcher.requests(); len(got) != 0 {
		t.Fatalf("dispatcher received %#v, want none", got)
	}
}

type recordingCommandDispatcher struct {
	mu      sync.Mutex
	accept  bool
	records []cmdrunner.Request
}

func (d *recordingCommandDispatcher) Submit(req cmdrunner.Request) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, cloneCommandRequest(req))
	return d.accept
}

func (d *recordingCommandDispatcher) waitRequest(t *testing.T) cmdrunner.Request {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		if len(d.records) > 0 {
			req := cloneCommandRequest(d.records[0])
			d.mu.Unlock()
			return req
		}
		d.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for command request")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (d *recordingCommandDispatcher) requests() []cmdrunner.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]cmdrunner.Request, 0, len(d.records))
	for _, req := range d.records {
		out = append(out, cloneCommandRequest(req))
	}
	return out
}

func cloneCommandRequest(req cmdrunner.Request) cmdrunner.Request {
	out := req
	if len(req.Slots) > 0 {
		out.Slots = make(map[string]string, len(req.Slots))
		for key, value := range req.Slots {
			out.Slots[key] = value
		}
	}
	return out
}

func buildCommandNodeTestEngine(t *testing.T, cfg configer.Config, commandCfg cmdrunner.Config, dispatcher cmdrunner.Dispatcher) almagest.Engine {
	t.Helper()
	if dispatcher == nil {
		dispatcher = &recordingCommandDispatcher{accept: true}
	}
	engine, err := almagest.BuildEngine(
		context.Background(),
		cfg,
		logging.NewDebugLogFactory(1024),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		daemonNodeClasses(commandCfg, dispatcher, false, ProtectedExperimentalConfig{}),
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

func commandNodeTestAllowedCommand(allowNodes bool, slots ...string) cmdrunner.AllowedCommand {
	command := cmdrunner.AllowedCommand{
		Command:    []string{"/bin/echo"},
		Slots:      map[string]cmdrunner.Slot{},
		AllowNodes: allowNodes,
	}
	for _, slot := range slots {
		command.Command = append(command.Command, "${"+slot+"}")
		command.Slots[slot] = cmdrunner.Slot{MaxLength: 128, AllowEmpty: true}
	}
	if len(command.Slots) == 0 {
		command.Slots = nil
	}
	return command
}

func addCommandNode(t *testing.T, engine almagest.Engine, name string) uint64 {
	t.Helper()
	id, err := engine.AddNode(almagest.MainWorkspace, NodeTypeProtectedCommand, name)
	if err != nil {
		t.Fatalf("AddNode command: %v", err)
	}
	return id
}

func addStdNode(t *testing.T, engine almagest.Engine, class, name string) uint64 {
	t.Helper()
	id, err := engine.AddNode(almagest.MainWorkspace, class, name)
	if err != nil {
		t.Fatalf("AddNode %s: %v", class, err)
	}
	return id
}

func applyCommandNodeCommand(t *testing.T, engine almagest.Engine, node uint64, command string) {
	t.Helper()
	engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: commandNodeFormMessageBase(formular.MessageFormApply, node),
		BlockID:     commandNodeConfigBlockID,
		Values:      map[string]any{commandNodeCommandField: command},
	})
}

func applyStdConstantValue(t *testing.T, engine almagest.Engine, node uint64, value any) {
	t.Helper()
	engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: commandNodeFormMessageBase(formular.MessageFormApply, node),
		BlockID:     "state",
		Values:      map[string]any{"value": value},
	})
}

func commandNodeFormMessageBase(messageType string, node uint64) formular.MessageBase {
	return formular.MessageBase{
		Type:           messageType,
		MenuID:         pasta.NodeMenuID(node),
		MenuGeneration: 1,
	}
}

type commandNodeMenuCapture struct {
	mu    sync.Mutex
	state *formular.MenuSnapshotState
}

func subscribeCommandNodeMenu(t *testing.T, engine almagest.Engine, node uint64) *commandNodeMenuCapture {
	t.Helper()
	capture := &commandNodeMenuCapture{state: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil {
			return
		}
		if notification.Workspace.Kind == pasta.NotificationNodeMenu {
			capture.mu.Lock()
			defer capture.mu.Unlock()
			capture.state.Apply(notification.Workspace.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(sub) })
	engine.SubscribeNodeMenu(almagest.MainWorkspace, node, sub)
	return capture
}

func waitCommandNodeMenuItem(t *testing.T, capture *commandNodeMenuCapture, node uint64, blockID, itemID string) formular.Item {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		snapshot, ok := capture.state.Snapshot(pasta.NodeMenuID(node))
		capture.mu.Unlock()
		if ok {
			for _, block := range snapshot.Blocks {
				if block.ID != blockID {
					continue
				}
				for _, item := range block.Items {
					if item.ID == itemID {
						return item
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("menu item %s/%s missing", blockID, itemID)
	return formular.Item{}
}

func mustAddTypedLinkByPortName(t *testing.T, engine almagest.Engine, wantType string, leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	t.Helper()
	left := commandNodePortByName(t, engine, leftNode, leftDir, leftName)
	right := commandNodePortByName(t, engine, rightNode, rightDir, rightName)
	link, gotType, err := engine.AddLink(almagest.MainWorkspace, left, right)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	if gotType != wantType {
		t.Fatalf("AddLink type = %q, want %q", gotType, wantType)
	}
	return link
}

func commandNodePortByName(t *testing.T, engine almagest.Engine, node uint64, direction, name string) uint64 {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	ports := snapshot.LeftPorts
	if direction == "right" {
		ports = snapshot.RightPorts
	}
	for _, port := range ports {
		portSnapshot, ok := engine.PortSnapshot(almagest.MainWorkspace, port)
		if ok && portSnapshot.Direction == direction && portSnapshot.Name == name {
			return port
		}
	}
	t.Fatalf("node %d has no %s port named %q", node, direction, name)
	return 0
}

func assertCommandNodeLeftPorts(t *testing.T, engine almagest.Engine, node uint64, want []string) {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	got := make([]string, 0, len(snapshot.LeftPorts))
	for _, port := range snapshot.LeftPorts {
		portSnapshot, ok := engine.PortSnapshot(almagest.MainWorkspace, port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		got = append(got, portSnapshot.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("left port names = %#v, want %#v", got, want)
	}
}

func assertCommandNodeRightPortCount(t *testing.T, engine almagest.Engine, node uint64, want int) {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if len(snapshot.RightPorts) != want {
		t.Fatalf("right port count = %d, want %d", len(snapshot.RightPorts), want)
	}
}

func assertCommandNodeHasNoPort(t *testing.T, engine almagest.Engine, node uint64, name string) {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	for _, port := range append(snapshot.LeftPorts, snapshot.RightPorts...) {
		portSnapshot, ok := engine.PortSnapshot(almagest.MainWorkspace, port)
		if ok && portSnapshot.Name == name {
			t.Fatalf("node %d unexpectedly has port %q", node, name)
		}
	}
}

func assertCommandNodeHasNoLinks(t *testing.T, engine almagest.Engine, node uint64) {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	for _, port := range append(snapshot.LeftPorts, snapshot.RightPorts...) {
		portSnapshot, ok := engine.PortSnapshot(almagest.MainWorkspace, port)
		if ok && len(portSnapshot.Links) > 0 {
			t.Fatalf("port %q links = %v, want none", portSnapshot.Name, portSnapshot.Links)
		}
	}
}

func assertCommandNodeLabel(t *testing.T, engine almagest.Engine, node uint64, want string) {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if snapshot.Label != want {
		t.Fatalf("node label = %q, want %q", snapshot.Label, want)
	}
}

func commandNodePastedByClass(t *testing.T, engine almagest.Engine, ids []uint64, class string) uint64 {
	t.Helper()
	for _, id := range ids {
		snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, id)
		if ok && snapshot.Class == class {
			return id
		}
	}
	t.Fatalf("pasted node with class %s missing in %v", class, ids)
	return 0
}

func commandNodeByName(t *testing.T, engine almagest.Engine, name string) uint64 {
	t.Helper()
	id, ok := engine.NodeByName(almagest.MainWorkspace, name)
	if !ok {
		t.Fatalf("node named %q missing", name)
	}
	return id
}
