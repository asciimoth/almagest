package tunnodes_test

import (
	"context"
	"reflect"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/tuntest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunClassFeatureGateAndNonUnique(t *testing.T) {
	unsupported := tuntest.NewRecordingSystem()
	unsupported.DisableTun = true
	engineUnsupported := newTunTestEngine(t, unsupported, nil)
	if _, ok := engineUnsupported.NodeClassSnapshot(e2etest.E2EWorkspace, tunnodes.NodeTypeTun); ok {
		t.Fatal("Tun class registered when sysnet feature is disabled")
	}

	supported := tuntest.NewRecordingSystem()
	engineSupported := newTunTestEngine(t, supported, nil)
	class, ok := engineSupported.NodeClassSnapshot(e2etest.E2EWorkspace, tunnodes.NodeTypeTun)
	if !ok {
		t.Fatal("Tun class missing when sysnet feature is enabled")
	}
	if class.Unique {
		t.Fatal("Tun class Unique = true, want false")
	}
	if len(class.InitialPorts) != 1 || class.InitialPorts[0].Direction != "right" || class.InitialPorts[0].Types[0] != tunnodes.TypeTun {
		t.Fatalf("Tun initial ports = %#v, want one right almagest/tun port", class.InitialPorts)
	}

	first, err := engineSupported.AddNode(e2etest.E2EWorkspace, tunnodes.NodeTypeTun, "tun-a")
	if err != nil {
		t.Fatalf("AddNode first Tun: %v", err)
	}
	second, err := engineSupported.AddNode(e2etest.E2EWorkspace, tunnodes.NodeTypeTun, "tun-b")
	if err != nil {
		t.Fatalf("AddNode second Tun: %v", err)
	}
	if first == second {
		t.Fatal("multiple Tun nodes returned the same node ID")
	}
}

func TestTunConfigPersistsAcrossSaveRestoreAndCopyPaste(t *testing.T) {
	sys := tuntest.NewRecordingSystem()
	engine := newTunTestEngine(t, sys, nil)
	node, err := engine.AddNode(e2etest.E2EWorkspace, tunnodes.NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNode Tun: %v", err)
	}
	applyTunTestConfig(t, engine, node, "persisted-tun")

	cfg := configer.NewMemory(nil)
	if err := engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restoreSys := tuntest.NewRecordingSystem()
	restored := newTunTestEngine(t, restoreSys, cfg)
	if got := restoreSys.LastOpts(); !reflect.DeepEqual(got, tunPersistedOpts()) {
		t.Fatalf("restored build opts = %#v, want %#v", got, tunPersistedOpts())
	}
	if got, want := restoreSys.SetNameCalls, []string{"persisted-tun"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored SetTunName calls = %#v, want %#v", got, want)
	}
	_ = restored

	clip := engine.Copy(e2etest.E2EWorkspace, []uint64{node})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	engine.RemoveNode(e2etest.E2EWorkspace, node)
	pasted := engine.Paste(e2etest.E2EWorkspace, clip)
	if len(pasted) != 1 {
		t.Fatalf("Paste nodes = %#v, want one node", pasted)
	}
	if got := sys.LastOpts(); !reflect.DeepEqual(got, tunPersistedOpts()) {
		t.Fatalf("pasted build opts = %#v, want %#v", got, tunPersistedOpts())
	}
}

func newTunTestEngine(t *testing.T, sys *tuntest.RecordingSystem, cfg configer.Config) almagest.Engine {
	t.Helper()
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	engine, err := almagest.BuildEngine(context.Background(), cfg, logging.NewDebugLogFactory(1024), sys, nil, nil, nil, nil, nil, nil, true)
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

func applyTunTestConfig(t *testing.T, engine almagest.Engine, node uint64, name string) {
	t.Helper()
	engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FormApplyMessage{
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
