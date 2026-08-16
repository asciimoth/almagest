package tunnodes_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunE2EMultipleNodesKeepIndependentSystemTuns(t *testing.T) {
	e := NewNodeE2E(t)
	first := e.AddNode(NodeTypeTun, "tun-a")
	second := e.AddNode(NodeTypeTun, "tun-b")

	applyTunE2ETestConfig(t, e.Engine, first, "e2e-tun-a")
	applyTunE2ETestConfig(t, e.Engine, second, "e2e-tun-b")

	firstEntry, ok := e.DebugSystem.GetTunPeer("e2e-tun-a")
	if !ok {
		t.Fatal("missing e2e-tun-a in debug system")
	}
	secondEntry, ok := e.DebugSystem.GetTunPeer("e2e-tun-b")
	if !ok {
		t.Fatal("missing e2e-tun-b in debug system")
	}
	if firstEntry.Tun == secondEntry.Tun {
		t.Fatal("multiple Tun nodes share one system Tun")
	}
	want := []string{"10.100.0.2/32"}
	if got := firstEntry.Config.TunAddrs; !reflect.DeepEqual(got, want) {
		t.Fatalf("first Tun addrs = %#v, want %#v", got, want)
	}
	if got := secondEntry.Config.TunAddrs; !reflect.DeepEqual(got, want) {
		t.Fatalf("second Tun addrs = %#v, want %#v", got, want)
	}
}

func TestTunE2EMultipleNodesSaveRestore(t *testing.T) {
	e := NewNodeE2E(t)
	first := e.AddNode(NodeTypeTun, "tun-a")
	second := e.AddNode(NodeTypeTun, "tun-b")
	applyTunE2ETestConfig(t, e.Engine, first, "restored-tun-a")
	applyTunE2ETestConfig(t, e.Engine, second, "restored-tun-b")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newNodeE2EWithConfig(t, cfg)
	if _, ok := restored.DebugSystem.GetTunPeer("restored-tun-a"); !ok {
		t.Fatal("restored debug system missing restored-tun-a")
	}
	if _, ok := restored.DebugSystem.GetTunPeer("restored-tun-b"); !ok {
		t.Fatal("restored debug system missing restored-tun-b")
	}
}

func applyTunE2ETestConfig(t *testing.T, engine Engine, node uint64, name string) {
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

func newNodeE2EWithConfig(t *testing.T, cfg configer.Config) *NodeE2E {
	t.Helper()
	return newNodeE2EWithConfigAndSecrets(t, cfg, newE2ESecretsStorage())
}

func newNodeE2EWithConfigAndSecrets(t *testing.T, cfg configer.Config, secrets *e2eSecretsStorage) *NodeE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	if secrets == nil {
		secrets = newE2ESecretsStorage()
	}
	engine, err := BuildEngine(
		context.Background(),
		cfg,
		logf,
		sys,
		nil,
		nil, secrets,
		nil,

		nil,
		pool,
		true)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	return NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}
