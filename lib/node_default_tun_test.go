// nolint
package almagest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestDefaultTunClassFeatureGate(t *testing.T) {
	unsupported := newRecordingDefaultTunSystem()
	unsupported.DisableDefaultTun = true
	engineUnsupported := newDefaultTunTestEngine(t, unsupported, nil)
	if _, ok := engineUnsupported.NodeClassSnapshot(E2EWorkspace, NodeTypeDefaultTun); ok {
		t.Fatal("DefaultTun class registered when sysnet feature is disabled")
	}

	supported := newRecordingDefaultTunSystem()
	engineSupported := newDefaultTunTestEngine(t, supported, nil)
	class, ok := engineSupported.NodeClassSnapshot(E2EWorkspace, NodeTypeDefaultTun)
	if !ok {
		t.Fatal("DefaultTun class missing when sysnet feature is enabled")
	}
	if !class.Unique {
		t.Fatal("DefaultTun class Unique = false, want true")
	}
	if len(class.InitialPorts) != 1 || class.InitialPorts[0].Direction != "right" || class.InitialPorts[0].Types[0] != TypeTun {
		t.Fatalf("DefaultTun initial ports = %#v, want one right almagest/tun port", class.InitialPorts)
	}
}

func TestDefaultTunBuildsEmitsAndRebuildsFromFormAndTunConfig(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after OnReady = %d, want 1", got)
	}

	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := mustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.in, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	firstPayload := receiveTunPayload(t, receiver.events)
	firstTun := sys.tuns[0]
	if firstTun.closeCount() != 0 {
		t.Fatalf("first DefaultTun close count after link = %d, want 0", firstTun.closeCount())
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs":  "10.10.0.2/32\nfd00::2/128",
			"tunRoutes": "0.0.0.0/0\n::/0",
			"mtu":       1400,
			"dnsIP":     "10.10.0.2",
			"strict":    true,
			"exclude": []formular.ArrayElementValue{{
				Template: "rule",
				Values:   map[string]any{"type": "app", "rule": "org.example.skip"},
			}},
			"include": []formular.ArrayElementValue{},
		},
	})
	if got := sys.buildCount(); got != 2 {
		t.Fatalf("build count after form apply = %d, want 2", got)
	}
	secondPayload := receiveTunPayload(t, receiver.events)
	if SameTunSource(firstPayload.Tun, secondPayload.Tun) {
		t.Fatal("form rebuild reused emitted Tun source, want a new DefaultTun")
	}
	if firstTun.closeCount() != 1 {
		t.Fatalf("old DefaultTun close count after rebuild = %d, want 1", firstTun.closeCount())
	}
	wantFormOpts := sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.10.0.2/32", "fd00::2/128"},
		TunRoutes: []string{"0.0.0.0/0", "::/0"},
		MTU:       1400,
		DnsIP:     "10.10.0.2",
		Strict:    true,
		Exclude:   []sysnet.Rule{{Type: "app", Rule: "org.example.skip"}},
		Include:   []sysnet.Rule{},
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantFormOpts) {
		t.Fatalf("form build opts = %#v, want %#v", got, wantFormOpts)
	}

	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.10.0.2/32", "10.20.0.2/32"}, MTU: 1280})
	if got := sys.buildCount(); got != 3 {
		t.Fatalf("build count after TunConfig = %d, want 3", got)
	}
	_ = receiveTunPayload(t, receiver.events)
	wantMergedOpts := wantFormOpts
	wantMergedOpts.TunAddrs = []string{"10.10.0.2/32", "fd00::2/128", "10.20.0.2/32"}
	wantMergedOpts.MTU = 1280
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantMergedOpts) {
		t.Fatalf("merged build opts = %#v, want %#v", got, wantMergedOpts)
	}
	if firstTun.closeCount() != 1 || sys.tuns[1].closeCount() != 1 {
		t.Fatalf("rebuilt DefaultTun close counts = %d, %d; want 1, 1", firstTun.closeCount(), sys.tuns[1].closeCount())
	}

	w.RemoveNode(node)
	Eventually(t, func() bool {
		return firstTun.closeCount() > 0 && sys.tuns[1].closeCount() > 0 && sys.tuns[2].closeCount() > 0
	}, "DefaultTun instances to close")
	if firstTun.closeCount() == 0 || sys.tuns[1].closeCount() == 0 || sys.tuns[2].closeCount() == 0 {
		t.Fatalf("DefaultTun close counts after node removal = %d, %d, %d; want all closed", firstTun.closeCount(), sys.tuns[1].closeCount(), sys.tuns[2].closeCount())
	}
}

func TestDefaultTunDynamicConfigUsesSettersWhenOnlyMTUAndAddrsChange(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, true)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := mustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.in, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = receiveTunPayload(t, receiver.events)

	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.30.0.2/32"}, MTU: 1300})
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after dynamic TunConfig = %d, want 1", got)
	}
	if got, want := sys.setAddrsCalls, [][]string{{"10.30.0.2/32"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunAddrs calls = %#v, want %#v", got, want)
	}
	if got, want := sys.setMTUCalls, []int{1300}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunMTU calls = %#v, want %#v", got, want)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs":  "",
			"tunRoutes": "0.0.0.0/0",
			"mtu":       0,
			"dnsIP":     "",
			"strict":    false,
			"exclude":   []formular.ArrayElementValue{},
			"include":   []formular.ArrayElementValue{},
		},
	})
	if got := sys.buildCount(); got != 2 {
		t.Fatalf("build count after route change = %d, want 2", got)
	}
}

func TestDefaultTunMenuUsesArrayFieldsAndReportsValidationStatus(t *testing.T) {
	w, _ := newDefaultTunNodeTestWorkspace(t, false)
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)

	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	block := mustFindBlock(t, snapshot, "config")
	for _, id := range []string{"tunAddrs", "tunRoutes"} {
		item, ok := FindItem(block, id)
		if !ok {
			t.Fatalf("%s item missing", id)
		}
		if item.Field == nil || item.Field.Kind != formular.FieldArray {
			t.Fatalf("%s field kind = %#v, want array", id, item.Field)
		}
		if len(item.Field.Templates) != 1 || item.Field.Templates[0].Name != "value" {
			t.Fatalf("%s templates = %#v, want value template", id, item.Field.Templates)
		}
	}

	w.SendNodeFormularMsg(node, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(node)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       -1,
	})
	snapshot, ok = state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing after invalid validation")
	}
	item, ok := FindItem(mustFindBlock(t, snapshot, "config"), "mtu")
	if !ok {
		t.Fatal("mtu item missing after invalid validation")
	}
	if item.Status != formular.StatusError {
		t.Fatalf("mtu status = %q, want %q", item.Status, formular.StatusError)
	}

	w.SendNodeFormularMsg(node, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(node)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       1200,
	})
	snapshot, ok = state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing after valid validation")
	}
	item, ok = FindItem(mustFindBlock(t, snapshot, "config"), "mtu")
	if !ok {
		t.Fatal("mtu item missing after valid validation")
	}
	if item.Status != formular.StatusOK {
		t.Fatalf("mtu status = %q, want %q", item.Status, formular.StatusOK)
	}
}

func TestDefaultTunAppliesTunAddrsAndRoutesFromArrayFields(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs": []formular.ArrayElementValue{
				{Template: "value", Values: map[string]any{"value": "10.50.0.2/32"}},
				{Template: "value", Values: map[string]any{"value": "fd00::50/128"}},
			},
			"tunRoutes": []formular.ArrayElementValue{
				{Template: "value", Values: map[string]any{"value": "0.0.0.0/0"}},
				{Template: "value", Values: map[string]any{"value": "::/0"}},
			},
			"mtu":     1250,
			"dnsIP":   "10.50.0.2",
			"strict":  false,
			"exclude": []formular.ArrayElementValue{},
			"include": []formular.ArrayElementValue{},
		},
	})

	want := sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.50.0.2/32", "fd00::50/128"},
		TunRoutes: []string{"0.0.0.0/0", "::/0"},
		MTU:       1250,
		DnsIP:     "10.50.0.2",
		Exclude:   []sysnet.Rule{},
		Include:   []sysnet.Rule{},
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("array form build opts = %#v, want %#v", got, want)
	}
}

func TestDefaultTunRuleAutocompleteUsesSystemRules(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	sys.Rules = []sysnet.RuleTypeInfo{
		{Type: "app", Description: "Application"},
		{Type: "uid", Description: "User ID"},
	}
	sys.RuleCompletion = func(rule sysnet.Rule) []string {
		if rule.Type == "app" && rule.Rule == "org.mozilla.f" {
			return []string{"org.mozilla.firefox", "org.mozilla.fennec", "org.chromium.Chromium"}
		}
		return nil
	}

	var autocomplete []formular.AutocompleteHintsMessage
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind != pasta.NotificationNodeMenu {
			return
		}
		if msg, ok := notification.Formular.(formular.AutocompleteHintsMessage); ok {
			autocomplete = append(autocomplete, msg)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)

	w.SendNodeFormularMsg(node, formular.AutocompleteRequestMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageAutocompleteRequest, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config",
			FieldID: "type",
			ElementPath: []formular.ElementPathSegment{{
				ArrayFieldID: "include",
				ElementID:    "rule-0",
			}},
		},
		Prefix: "a",
	})
	if len(autocomplete) == 0 {
		t.Fatal("missing type autocomplete response")
	}
	if got, want := autocomplete[len(autocomplete)-1].Hints, []string{"app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("type hints = %#v, want %#v", got, want)
	}

	w.SendNodeFormularMsg(node, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config",
			FieldID: "type",
			ElementPath: []formular.ElementPathSegment{{
				ArrayFieldID: "include",
				ElementID:    "new-rule",
			}},
		},
		Value: "app",
	})
	w.SendNodeFormularMsg(node, formular.AutocompleteRequestMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageAutocompleteRequest, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config",
			FieldID: "rule",
			ElementPath: []formular.ElementPathSegment{{
				ArrayFieldID: "include",
				ElementID:    "new-rule",
			}},
		},
		Prefix: "org.mozilla.f",
	})
	if len(autocomplete) < 2 {
		t.Fatal("missing rule autocomplete response")
	}
	want := []string{"org.mozilla.firefox", "org.mozilla.fennec"}
	if got := autocomplete[len(autocomplete)-1].Hints; !reflect.DeepEqual(got, want) {
		t.Fatalf("rule hints = %#v, want %#v", got, want)
	}
}

func TestDefaultTunOnStopClosesActiveDefaultTun(t *testing.T) {
	dtun := newRecordingDefaultTun(1500)
	node := &defaultTunNode{
		dtun:         dtun,
		hasEffective: true,
	}

	node.OnStop()

	if got := dtun.closeCount(); got != 1 {
		t.Fatalf("DefaultTun close count after OnStop = %d, want 1", got)
	}
	if node.dtun != nil {
		t.Fatal("DefaultTun node kept dtun after OnStop")
	}
	if node.hasEffective {
		t.Fatal("DefaultTun node kept effective config after OnStop")
	}
}

func TestDefaultTunLogsConfigureFailure(t *testing.T) {
	logf := NewDebugLogFactory(1024)
	w := pasta.NewWorkspace(logf)
	t.Cleanup(w.Close)
	sys := newRecordingDefaultTunSystem()
	sys.buildErr = errors.New("create TUN failed")
	engine := &engine{ctx: context.Background(), sys: sys}
	if err := w.AddNodeClass(defaultTunClass{engine: engine}); err != nil {
		t.Fatalf("AddNodeClass DefaultTun: %v", err)
	}
	if _, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun"); err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	entries := logf.Drain()
	for _, entry := range entries {
		if entry.Level == DebugLogLevelError &&
			entry.Source.Name == "node" &&
			entry.Source.NodeClass == NodeTypeDefaultTun &&
			strings.Contains(entry.Message, "configure default TUN failed: create TUN failed") {
			return
		}
	}
	t.Fatalf("missing DefaultTun configure failure log in %#v", entries)
}

func TestDefaultTunConfigPersistsAcrossSaveRestoreAndCopyPaste(t *testing.T) {
	sys := newRecordingDefaultTunSystem()
	engine := newDefaultTunTestEngine(t, sys, nil)
	node, err := engine.AddNode(E2EWorkspace, NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNode DefaultTun: %v", err)
	}
	applyDefaultTunTestConfig(t, engine, node)

	cfg := configer.NewMemory(nil)
	if err := engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restoreSys := newRecordingDefaultTunSystem()
	restored := newDefaultTunTestEngine(t, restoreSys, cfg)
	if got := restoreSys.lastOpts(); !reflect.DeepEqual(got, defaultTunPersistedOpts()) {
		t.Fatalf("restored build opts = %#v, want %#v", got, defaultTunPersistedOpts())
	}
	_ = restored

	clip := engine.Copy(E2EWorkspace, []uint64{node})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	engine.RemoveNode(E2EWorkspace, node)
	pasted := engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 1 {
		t.Fatalf("Paste nodes = %#v, want one node", pasted)
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, defaultTunPersistedOpts()) {
		t.Fatalf("pasted build opts = %#v, want %#v", got, defaultTunPersistedOpts())
	}
}

func newDefaultTunNodeTestWorkspace(t *testing.T, dynamic bool) (*pasta.Workspace, *recordingDefaultTunSystem) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	sys := newRecordingDefaultTunSystem()
	sys.DisableDynDefaultTun = !dynamic
	engine := &engine{ctx: context.Background(), sys: sys}
	if err := w.AddNodeClass(defaultTunClass{engine: engine}); err != nil {
		t.Fatalf("AddNodeClass DefaultTun: %v", err)
	}
	return w, sys
}

func newDefaultTunTestEngine(t *testing.T, sys *recordingDefaultTunSystem, cfg configer.Config) Engine {
	t.Helper()
	pool := bufpool.NewTestDebugPool(t)
	t.Cleanup(func() { pool.Close() })
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	engine, err := BuildEngine(context.Background(), cfg, NewDebugLogFactory(1024), sys, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return engine
}

func applyDefaultTunTestConfig(t *testing.T, engine Engine, node uint64) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs":  "10.40.0.2/32",
			"tunRoutes": "0.0.0.0/0",
			"mtu":       1250,
			"dnsIP":     "10.40.0.2",
			"strict":    true,
			"exclude":   []formular.ArrayElementValue{},
			"include": []formular.ArrayElementValue{{
				Template: "rule",
				Values:   map[string]any{"type": "uid", "rule": "1000"},
			}},
		},
	})
}

func defaultTunPersistedOpts() sysnet.DefaultTunOpts {
	return sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.40.0.2/32"},
		TunRoutes: []string{"0.0.0.0/0"},
		MTU:       1250,
		DnsIP:     "10.40.0.2",
		Strict:    true,
		Exclude:   []sysnet.Rule{},
		Include:   []sysnet.Rule{{Type: "uid", Rule: "1000"}},
	}
}

type recordingDefaultTunSystem struct {
	*sysnetdebug.System

	buildOpts     []sysnet.DefaultTunOpts
	tuns          []*recordingDefaultTun
	setMTUCalls   []int
	setAddrsCalls [][]string
	buildErr      error
}

func newRecordingDefaultTunSystem() *recordingDefaultTunSystem {
	return &recordingDefaultTunSystem{System: &sysnetdebug.System{}}
}

func (s *recordingDefaultTunSystem) BuildDefaultTun(opts sysnet.DefaultTunOpts) (sysnet.DefaultTun, error) {
	if err := s.VerifyDefaultTunOpts(opts); err != nil {
		return nil, err
	}
	if s.buildErr != nil {
		return nil, s.buildErr
	}
	dtun := newRecordingDefaultTun(opts.MTU)
	s.buildOpts = append(s.buildOpts, opts.Copy())
	s.tuns = append(s.tuns, dtun)
	return dtun, nil
}

func (s *recordingDefaultTunSystem) SetTunMTU(t tun.Tun, mtu int) error {
	dtun, ok := t.(*recordingDefaultTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.setMTUCalls = append(s.setMTUCalls, mtu)
	dtun.mtu = mtu
	return nil
}

func (s *recordingDefaultTunSystem) SetTunAddrs(t tun.Tun, addrs []string) error {
	if _, ok := t.(*recordingDefaultTun); !ok {
		return sysnet.ErrUnknownTun
	}
	s.setAddrsCalls = append(s.setAddrsCalls, slicesClone(addrs))
	return nil
}

func (s *recordingDefaultTunSystem) buildCount() int {
	return len(s.buildOpts)
}

func (s *recordingDefaultTunSystem) lastOpts() sysnet.DefaultTunOpts {
	if len(s.buildOpts) == 0 {
		return sysnet.DefaultTunOpts{}
	}
	return s.buildOpts[len(s.buildOpts)-1].Copy()
}

type recordingDefaultTun struct {
	tun.Tun
	peer tun.Tun

	mtu          int
	closeCounter atomic.Int32
	resolver     dns.Interface
}

func newRecordingDefaultTun(mtu int) *recordingDefaultTun {
	if mtu <= 0 {
		mtu = 1500
	}
	base, peer := tun.Pipe(1, mtu, 0, 0)
	return &recordingDefaultTun{Tun: base, peer: peer, mtu: mtu}
}

func (t *recordingDefaultTun) SetDns(resolver dns.Interface) {
	t.resolver = resolver
}

func (t *recordingDefaultTun) MTU() (int, error) {
	return t.mtu, nil
}

func (t *recordingDefaultTun) Close() error {
	t.closeCounter.Add(1)
	return errors.Join(t.Tun.Close(), t.peer.Close())
}

func (t *recordingDefaultTun) closeCount() int {
	return int(t.closeCounter.Load())
}

func slicesClone[T any](in []T) []T {
	out := make([]T, len(in))
	copy(out, in)
	return out
}
