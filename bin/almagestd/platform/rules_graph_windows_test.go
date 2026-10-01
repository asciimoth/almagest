//go:build windows

package platform

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"golang.org/x/sys/windows"
)

func TestWindowsGraphDefaultTunRuleMenusAndPersistence(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native executable-tree graph rules require an elevated test identity")
	}
	engine, system, _ := newWindowsTunGraphEngine(t)
	recorder := newWindowsGraphMenuRecorder(t, engine)
	node, err := engine.AddNode(almagest.MainWorkspace, tunnodes.NodeTypeDefaultTun, "rule default tun")
	if err != nil {
		t.Fatalf("AddNode DefaultTun: %v", err)
	}
	engine.SubscribeNodeMenu(almagest.MainWorkspace, node, recorder.subscription)
	menu := recorder.snapshot(t, node)
	block := windowsGraphMenuBlock(t, menu, "config")
	if item := windowsGraphMenuItem(t, block, "strict"); !item.Readonly || !strings.Contains(item.Help, "not supported") {
		t.Fatalf("strict menu item = %#v, want readonly unsupported", item)
	}
	if item := windowsGraphMenuItem(t, block, "include"); !item.Readonly || !strings.Contains(item.Help, "not supported") {
		t.Fatalf("include menu item = %#v, want readonly unsupported", item)
	}
	if item := windowsGraphMenuItem(t, block, "exclude"); item.Readonly {
		t.Fatalf("exclude menu item = %#v, want editable", item)
	}
	engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.AutocompleteRequestMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageAutocompleteRequest, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config", FieldID: "type",
			ElementPath: []formular.ElementPathSegment{{ArrayFieldID: "exclude", ElementID: "new-rule"}},
		},
		Prefix: "win",
	})
	select {
	case hints := <-recorder.hints:
		if !reflect.DeepEqual(hints.Hints, []string{"win-exe-tree"}) {
			t.Fatalf("Windows exclusion type hints = %#v, want [win-exe-tree]", hints.Hints)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Windows exclusion type autocomplete did not respond")
	}

	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	configuredPath := strings.ReplaceAll(executable, `\`, "/")
	if len(configuredPath) >= 2 && configuredPath[1] == ':' {
		configuredPath = strings.ToLower(configuredPath[:1]) + configuredPath[1:]
	}
	applyWindowsTunGraphForm(t, engine, node, map[string]any{
		"exclude": []formular.ArrayElementValue{{
			Template: "rule",
			Values:   map[string]any{"type": "win-exe-tree", "rule": configuredPath},
		}},
	})
	waitWindowsGraphRuleNodeLabel(t, engine, node, "ready")
	assertWindowsGraphSavedRule(t, engine, "rule default tun", "exclude", "win-exe-tree", configuredPath)

	applyWindowsTunGraphForm(t, engine, node, map[string]any{
		"exclude": []formular.ArrayElementValue{{
			Template: "rule",
			Values:   map[string]any{"type": "uid", "rule": "1000"},
		}},
	})
	waitWindowsGraphRuleNodeLabel(t, engine, node, "invalid config")
	menu = recorder.snapshot(t, node)
	exclude := windowsGraphMenuItem(t, windowsGraphMenuBlock(t, menu, "config"), "exclude")
	if exclude.Field == nil || exclude.Status != formular.StatusError ||
		!strings.Contains(exclude.StatusText, "not supported") {
		t.Fatalf("unsupported Linux rule menu = %#v, want visible error", exclude)
	}
	assertWindowsGraphSavedRule(t, engine, "rule default tun", "exclude", "uid", "1000")
	restoredConfig := configer.NewMemory(nil)
	if err := engine.SaveConfig(restoredConfig); err != nil {
		t.Fatalf("save rejected rule for reload: %v", err)
	}
	restoredSystem, err := SystemIntegration(configer.NewMemory(nil), nil)
	if err != nil {
		t.Fatalf("restored SystemIntegration: %v", err)
	}
	restoredEngine, err := almagest.BuildEngine(
		context.Background(), restoredConfig, logging.NewDebugLogFactory(256),
		restoredSystem, nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		_ = restoredSystem.Close()
		t.Fatalf("restore rule graph: %v", err)
	}
	t.Cleanup(func() {
		if err := restoredEngine.Close(); err != nil {
			t.Errorf("restored engine.Close: %v", err)
		}
	})
	restoredNode, ok := restoredEngine.NodeByName(almagest.MainWorkspace, "rule default tun")
	if !ok {
		t.Fatal("restored DefaultTun node missing")
	}
	if snapshot, ok := restoredEngine.NodeSnapshot(almagest.MainWorkspace, restoredNode); !ok || snapshot.Label != "invalid config" {
		t.Fatalf("restored unsupported rule snapshot = %#v, want invalid config", snapshot)
	}

	excludeContext := sysnet.RuleContext{Routing: &sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingExclude,
	}}
	for _, invalid := range []string{`relative\app.exe`, `\\server\share\app.exe`, `C:\*.exe`, `C:\app.exe:stream`} {
		if err := system.CheckRule(sysnet.Rule{Type: "win-exe-tree", Rule: invalid}, excludeContext).Err(); err == nil {
			t.Errorf("CheckRule accepted invalid Windows path %q", invalid)
		}
	}
}

func TestWindowsGraphOwnershipMatchersAndPersistence(t *testing.T) {
	system, err := SystemIntegration(configer.NewMemory(nil), nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(256),
		system, nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		_ = system.Close()
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if engine != nil {
			if err := engine.Close(); err != nil {
				t.Errorf("engine.Close: %v", err)
			}
		}
	})

	flow, closeFlow := windowsOwnedTCPFlow(t)
	defer closeFlow()
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve executable: %v", err)
	}
	matcherRules := []sysnet.Rule{
		{Type: "win-pid", Rule: strconv.Itoa(os.Getpid())},
		{Type: "win-exe-path", Rule: strings.ToUpper(executable)},
	}
	for _, rule := range matcherRules {
		matcher, buildErr := system.BuildMatcher(rule)
		if buildErr != nil {
			t.Fatalf("BuildMatcher(%s): %v", rule.Type, buildErr)
		}
		matched, matchErr := matcher.Match(flow)
		_ = matcher.Close()
		if matchErr != nil || !matched {
			t.Fatalf("Match(%s) = %v, %v; want true, nil", rule.Type, matched, matchErr)
		}
	}

	node, err := engine.AddNode(almagest.MainWorkspace, tunnodes.NodeTypeTunSplitter, "owner splitter")
	if err != nil {
		t.Fatalf("AddNode TunSplitter: %v", err)
	}
	rules := "RULE win-pid " + strconv.Itoa(os.Getpid()) + "\nSLOT 2\n" +
		"RULE win-exe-path " + executable + "\nSLOT 3\nTRUE\nSLOT 1\n"
	applyWindowsGraphForm(engine, node, tunnodes.TunSplitterConfigBlockID, map[string]any{
		tunnodes.TunSplitterRulesField: rules,
	})
	cfg := configer.NewMemory(nil)
	if err := engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := cfg.Get(configer.Path{"nodes", "owner splitter", tunnodes.TunSplitterRulesField})
	if err != nil || got != rules {
		t.Fatalf("saved matcher rules = %#v, %v; want %q, nil", got, err, rules)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("close matcher graph before reload: %v", err)
	}
	engine = nil
	restoredSystem, err := SystemIntegration(configer.NewMemory(nil), nil)
	if err != nil {
		t.Fatalf("restored SystemIntegration: %v", err)
	}
	restoredEngine, err := almagest.BuildEngine(
		context.Background(), cfg, logging.NewDebugLogFactory(256),
		restoredSystem, nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		_ = restoredSystem.Close()
		t.Fatalf("restore matcher graph: %v", err)
	}
	engine = restoredEngine
	if restoredNode, ok := engine.NodeByName(almagest.MainWorkspace, "owner splitter"); !ok {
		t.Fatal("restored matcher splitter missing")
	} else if snapshot, exists := engine.NodeSnapshot(almagest.MainWorkspace, restoredNode); !exists || snapshot.Label != "waiting for tun" {
		t.Fatalf("restored matcher splitter snapshot = %#v, want waiting for tun", snapshot)
	}
}

type windowsGraphMenuRecorder struct {
	mu           sync.Mutex
	state        *formular.MenuSnapshotState
	hints        chan formular.AutocompleteHintsMessage
	subscription uint64
}

func newWindowsGraphMenuRecorder(t *testing.T, engine almagest.Engine) *windowsGraphMenuRecorder {
	t.Helper()
	recorder := &windowsGraphMenuRecorder{
		state: formular.NewMenuSnapshotState(),
		hints: make(chan formular.AutocompleteHintsMessage, 4),
	}
	recorder.subscription = engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil ||
			notification.Workspace.Kind != pasta.NotificationNodeMenu {
			return
		}
		if hints, ok := notification.Workspace.Formular.(formular.AutocompleteHintsMessage); ok {
			recorder.hints <- hints
			return
		}
		recorder.mu.Lock()
		recorder.state.Apply(notification.Workspace.Formular)
		recorder.mu.Unlock()
	})
	if recorder.subscription == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(recorder.subscription) })
	return recorder
}

func (r *windowsGraphMenuRecorder) snapshot(t *testing.T, node uint64) formular.MenuSnapshotMessage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := r.state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	return snapshot
}

func windowsGraphMenuBlock(t *testing.T, menu formular.MenuSnapshotMessage, id string) formular.Block {
	t.Helper()
	for _, block := range menu.Blocks {
		if block.ID == id {
			return block
		}
	}
	t.Fatalf("menu block %q missing", id)
	return formular.Block{}
}

func windowsGraphMenuItem(t *testing.T, block formular.Block, id string) formular.Item {
	t.Helper()
	for _, item := range block.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("menu item %q missing", id)
	return formular.Item{}
}

func assertWindowsGraphSavedRule(t *testing.T, engine almagest.Engine, nodeName, field, ruleType, ruleValue string) {
	t.Helper()
	cfg := configer.NewMemory(nil)
	if err := engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := cfg.Get(configer.Path{"nodes", nodeName, field})
	want := []any{map[string]any{"type": ruleType, "rule": ruleValue}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("saved %s rules = %#v, %v; want %#v, nil", field, got, err, want)
	}
}

func windowsOwnedTCPFlow(t *testing.T) (sockowner.FlowTuple, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		t.Fatalf("Dial: %v", err)
	}
	server := <-accepted
	local := client.LocalAddr().(*net.TCPAddr)
	remote := client.RemoteAddr().(*net.TCPAddr)
	return sockowner.FlowTuple{
			Proto: "tcp", LocalIP: local.IP, LocalPort: uint16(local.Port),
			RemoteIP: remote.IP, RemotePort: uint16(remote.Port),
		}, func() {
			_ = client.Close()
			_ = server.Close()
			_ = listener.Close()
		}
}

func waitWindowsGraphRuleNodeLabel(t *testing.T, engine almagest.Engine, node uint64, want string) {
	t.Helper()
	waitWindowsGraphNodeLabel(t, engine, node, want)
}
