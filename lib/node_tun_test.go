package almagest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunClassFeatureGateAndNonUnique(t *testing.T) {
	unsupported := newRecordingTunSystem()
	unsupported.DisableTun = true
	engineUnsupported := newTunTestEngine(t, unsupported, nil)
	if _, ok := engineUnsupported.NodeClassSnapshot(E2EWorkspace, NodeTypeTun); ok {
		t.Fatal("Tun class registered when sysnet feature is disabled")
	}

	supported := newRecordingTunSystem()
	engineSupported := newTunTestEngine(t, supported, nil)
	class, ok := engineSupported.NodeClassSnapshot(E2EWorkspace, NodeTypeTun)
	if !ok {
		t.Fatal("Tun class missing when sysnet feature is enabled")
	}
	if class.Unique {
		t.Fatal("Tun class Unique = true, want false")
	}
	if len(class.InitialPorts) != 1 || class.InitialPorts[0].Direction != "right" || class.InitialPorts[0].Types[0] != TypeTun {
		t.Fatalf("Tun initial ports = %#v, want one right almagest/tun port", class.InitialPorts)
	}

	first, err := engineSupported.AddNode(E2EWorkspace, NodeTypeTun, "tun-a")
	if err != nil {
		t.Fatalf("AddNode first Tun: %v", err)
	}
	second, err := engineSupported.AddNode(E2EWorkspace, NodeTypeTun, "tun-b")
	if err != nil {
		t.Fatalf("AddNode second Tun: %v", err)
	}
	if first == second {
		t.Fatal("multiple Tun nodes returned the same node ID")
	}
}

func TestTunBuildsEmitsAndClosesPreviousBeforeRebuild(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, false)
	sys.requirePreviousClosedBeforeBuild = true
	state, sub := subscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)
	assertLeadingEffectiveBlock(t, state, node)
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after OnReady = %d, want 1", got)
	}

	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "receiver")
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

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "user-tun",
			"tunAddrs":  "10.60.0.2/32\nfd00::60/128",
			"tunRoutes": "10.70.0.0/16",
			"mtu":       1400,
		},
	})
	if got := sys.buildCount(); got != 2 {
		t.Fatalf("build count after form apply = %d, want 2", got)
	}
	if firstTun.closeCount() != 1 {
		t.Fatalf("old Tun close count during rebuild = %d, want 1", firstTun.closeCount())
	}
	secondPayload := receiveTunPayload(t, receiver.events)
	if SameTunSource(firstPayload.Tun, secondPayload.Tun) {
		t.Fatal("form rebuild reused emitted Tun source, want a new Tun")
	}
	wantFormOpts := sysnet.TunOpts{
		TunAddrs:  []string{"10.60.0.2/32", "fd00::60/128"},
		TunRoutes: []string{"10.70.0.0/16"},
		MTU:       1400,
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantFormOpts) {
		t.Fatalf("form build opts = %#v, want %#v", got, wantFormOpts)
	}
	if got, want := sys.setNameCalls, []string{"user-tun"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunName calls = %#v, want %#v", got, want)
	}

	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.60.0.2/32", "10.80.0.2/32"}, MTU: 1280})
	if got := sys.buildCount(); got != 3 {
		t.Fatalf("build count after TunConfig = %d, want 3", got)
	}
	_ = receiveTunPayload(t, receiver.events)
	wantMergedOpts := wantFormOpts
	wantMergedOpts.TunAddrs = []string{"10.60.0.2/32", "fd00::60/128", "10.80.0.2/32"}
	wantMergedOpts.MTU = 1280
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantMergedOpts) {
		t.Fatalf("merged build opts = %#v, want %#v", got, wantMergedOpts)
	}
	assertReadonlyMenuFieldValue(t, state, node, "effective", "name", "user-tun")
	assertReadonlyMenuFieldValue(t, state, node, "effective", "tunAddrs", "10.60.0.2/32\nfd00::60/128\n10.80.0.2/32")
	assertReadonlyMenuFieldValue(t, state, node, "effective", "tunRoutes", "10.70.0.0/16")
	assertReadonlyMenuFieldValue(t, state, node, "effective", "mtu", 1280)
	if sys.buildErr != nil {
		t.Fatalf("unexpected recording build error: %v", sys.buildErr)
	}

	w.RemoveNode(node)
	Eventually(t, func() bool {
		return sys.tuns[0].closeCount() > 0 && sys.tuns[1].closeCount() > 0 && sys.tuns[2].closeCount() > 0
	}, "Tun instances to close")
}

func TestTunDynamicConfigUsesSettersWhenAvailable(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, true)
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := mustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.in, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = receiveTunPayload(t, receiver.events)

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "dyn-tun",
			"tunAddrs":  "10.90.0.2/32",
			"tunRoutes": "10.91.0.0/16",
			"mtu":       1350,
		},
	})
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after dynamic form apply = %d, want 1", got)
	}
	if got, want := sys.setNameCalls, []string{"dyn-tun"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunName calls = %#v, want %#v", got, want)
	}
	if got, want := sys.setAddrsCalls, [][]string{{"10.90.0.2/32"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunAddrs calls = %#v, want %#v", got, want)
	}
	if got, want := sys.setRoutesCalls, [][]string{{"10.91.0.0/16"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunRoutes calls = %#v, want %#v", got, want)
	}
	if got, want := sys.setMTUCalls, []int{1350}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunMTU calls = %#v, want %#v", got, want)
	}

	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.90.0.2/32", "10.92.0.2/32"}, MTU: 1280})
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after dynamic TunConfig = %d, want 1", got)
	}
	if got, want := sys.setAddrsCalls, [][]string{{"10.90.0.2/32"}, {"10.90.0.2/32", "10.92.0.2/32"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunAddrs calls after TunConfig = %#v, want %#v", got, want)
	}
	if got, want := sys.setMTUCalls, []int{1350, 1280}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunMTU calls after TunConfig = %#v, want %#v", got, want)
	}
}

func TestTunNameValidationUsesSystem(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, false)
	sys.TunNameVerifyer = func(name string) bool {
		return strings.HasPrefix(name, "ok")
	}
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "bad-name",
			"tunAddrs":  "",
			"tunRoutes": "",
			"mtu":       0,
		},
	})
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after invalid name = %d, want unchanged 1", got)
	}
	snapshot := mustNodeSnapshot(t, w, node)
	if snapshot.Label != "invalid config" {
		t.Fatalf("node label after invalid name = %q, want invalid config", snapshot.Label)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "ok-name",
			"tunAddrs":  "",
			"tunRoutes": "",
			"mtu":       0,
		},
	})
	if got := sys.buildCount(); got != 2 {
		t.Fatalf("build count after valid name = %d, want 2", got)
	}
	if got, want := sys.setNameCalls, []string{"ok-name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunName calls = %#v, want %#v", got, want)
	}
}

func TestTunConfigPersistsAcrossSaveRestoreAndCopyPaste(t *testing.T) {
	sys := newRecordingTunSystem()
	engine := newTunTestEngine(t, sys, nil)
	node, err := engine.AddNode(E2EWorkspace, NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNode Tun: %v", err)
	}
	applyTunTestConfig(t, engine, node, "persisted-tun")

	cfg := configer.NewMemory(nil)
	if err := engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restoreSys := newRecordingTunSystem()
	restored := newTunTestEngine(t, restoreSys, cfg)
	if got := restoreSys.lastOpts(); !reflect.DeepEqual(got, tunPersistedOpts()) {
		t.Fatalf("restored build opts = %#v, want %#v", got, tunPersistedOpts())
	}
	if got, want := restoreSys.setNameCalls, []string{"persisted-tun"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored SetTunName calls = %#v, want %#v", got, want)
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
	if got := sys.lastOpts(); !reflect.DeepEqual(got, tunPersistedOpts()) {
		t.Fatalf("pasted build opts = %#v, want %#v", got, tunPersistedOpts())
	}
}

func newTunNodeTestWorkspace(t *testing.T, dynamic bool) (*pasta.Workspace, *recordingTunSystem) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	sys := newRecordingTunSystem()
	sys.DisableDynTun = !dynamic
	engine := &engine{ctx: context.Background(), sys: sys}
	if err := w.AddNodeClass(tunClass{engine: engine}); err != nil {
		t.Fatalf("AddNodeClass Tun: %v", err)
	}
	return w, sys
}

func newTunTestEngine(t *testing.T, sys *recordingTunSystem, cfg configer.Config) Engine {
	t.Helper()
	pool := newE2ETestDebugPool(t)
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

func applyTunTestConfig(t *testing.T, engine Engine, node uint64, name string) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      name,
			"tunAddrs":  "10.100.0.2/32",
			"tunRoutes": "10.101.0.0/16",
			"mtu":       1250,
		},
	})
}

func tunPersistedOpts() sysnet.TunOpts {
	return sysnet.TunOpts{
		TunAddrs:  []string{"10.100.0.2/32"},
		TunRoutes: []string{"10.101.0.0/16"},
		MTU:       1250,
	}
}

type recordingTunSystem struct {
	*sysnetdebug.System

	buildOpts                        []sysnet.TunOpts
	tuns                             []*recordingTun
	setMTUCalls                      []int
	setAddrsCalls                    [][]string
	setRoutesCalls                   [][]string
	setNameCalls                     []string
	buildErr                         error
	requirePreviousClosedBeforeBuild bool
}

func newRecordingTunSystem() *recordingTunSystem {
	return &recordingTunSystem{System: &sysnetdebug.System{}}
}

func (s *recordingTunSystem) BuildTun(opts sysnet.TunOpts) (tun.Tun, error) {
	if err := s.VerifyTunOpts(opts); err != nil {
		return nil, err
	}
	if s.requirePreviousClosedBeforeBuild && len(s.tuns) > 0 && s.tuns[len(s.tuns)-1].closeCount() == 0 {
		s.buildErr = errors.New("previous Tun was still open before BuildTun")
		return nil, s.buildErr
	}
	if s.buildErr != nil {
		return nil, s.buildErr
	}
	rtun := newRecordingTun(opts.MTU)
	s.buildOpts = append(s.buildOpts, copyTunOpts(opts))
	s.tuns = append(s.tuns, rtun)
	return rtun, nil
}

func (s *recordingTunSystem) SetTunMTU(t tun.Tun, mtu int) error {
	rtun, ok := t.(*recordingTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.setMTUCalls = append(s.setMTUCalls, mtu)
	rtun.mtu = mtu
	return nil
}

func (s *recordingTunSystem) SetTunAddrs(t tun.Tun, addrs []string) error {
	rtun, ok := t.(*recordingTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.setAddrsCalls = append(s.setAddrsCalls, slicesClone(addrs))
	rtun.addrs = slicesClone(addrs)
	return nil
}

func (s *recordingTunSystem) SetTunRoutes(t tun.Tun, routes []string) error {
	rtun, ok := t.(*recordingTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.setRoutesCalls = append(s.setRoutesCalls, slicesClone(routes))
	rtun.routes = slicesClone(routes)
	return nil
}

func (s *recordingTunSystem) SetTunName(t tun.Tun, name string) ([]string, error) {
	rtun, ok := t.(*recordingTun)
	if !ok {
		return nil, sysnet.ErrUnknownTun
	}
	if valid, free := s.TunNameVerify(name); !valid || (!free && name != rtun.name) {
		return nil, sysnet.ErrUnknownTun
	}
	s.setNameCalls = append(s.setNameCalls, name)
	rtun.name = name
	return []string{name}, nil
}

func (s *recordingTunSystem) buildCount() int {
	return len(s.buildOpts)
}

func (s *recordingTunSystem) lastOpts() sysnet.TunOpts {
	if len(s.buildOpts) == 0 {
		return sysnet.TunOpts{}
	}
	return copyTunOpts(s.buildOpts[len(s.buildOpts)-1])
}

type recordingTun struct {
	tun.Tun
	peer tun.Tun

	name         string
	mtu          int
	addrs        []string
	routes       []string
	closeCounter atomic.Int32
}

func newRecordingTun(mtu int) *recordingTun {
	if mtu <= 0 {
		mtu = 1500
	}
	base, peer := tun.Pipe(1, mtu, 0, 0)
	return &recordingTun{Tun: base, peer: peer, mtu: mtu}
}

func (t *recordingTun) MTU() (int, error) {
	return t.mtu, nil
}

func (t *recordingTun) Close() error {
	t.closeCounter.Add(1)
	return errors.Join(t.Tun.Close(), t.peer.Close())
}

func (t *recordingTun) closeCount() int {
	return int(t.closeCounter.Load())
}
