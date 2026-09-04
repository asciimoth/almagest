package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestNodeNameIndexKeepsSecretNamesAndRecentOrder(t *testing.T) {
	index := newNodeNameIndex(2)
	index.replaceSecretNames([]string{"secret"})
	index.remember("first")
	index.remember("second")
	index.remember("first")
	index.remember("third")

	assertNodeNameAvailability(t, index, "secret", false)
	assertNodeNameAvailability(t, index, "first", false)
	assertNodeNameAvailability(t, index, "second", true)
	assertNodeNameAvailability(t, index, "third", false)

	index.replaceSecretNames([]string{"replacement"})
	assertNodeNameAvailability(t, index, "secret", true)
	assertNodeNameAvailability(t, index, "replacement", false)
}

func TestDaemonNodeNameIndexLoadsConfigAndAllSecretAllowLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("shared", "mutable"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("shared", []string{"shadowed-node"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("dynamic", "value"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("dynamic", []string{"old-node"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManagerWithOptions(path, bboltKVManagerOptions{
		ImmutableSecrets: map[string]immutableSecret{
			"shared": {
				secretRecord: secretRecord{
					Value: "protected",
					Nodes: []string{"protected-node"},
				},
				source: api.SecretSourceProtected,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	cfg := configer.NewMemory(map[string]any{
		"nodes": map[string]any{
			"configured-node": map[string]any{"Class": "test/Node"},
		},
	})
	index, err := newDaemonNodeNameIndex(manager, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	assertNodeNameAvailability(t, index.index, "configured-node", false)
	assertNodeNameAvailability(t, index.index, "shadowed-node", false)
	assertNodeNameAvailability(t, index.index, "protected-node", false)
	assertNodeNameAvailability(t, index.index, "old-node", false)

	if _, err := manager.setSecretNodes("dynamic", []string{"new-node"}); err != nil {
		t.Fatal(err)
	}
	assertNodeNameAvailability(t, index.index, "old-node", true)
	assertNodeNameAvailability(t, index.index, "new-node", false)
}

func TestSecretNodeNameSyncRemembersDeletedNodeName(t *testing.T) {
	candidate := firstGeneratedSecretInboxNodeName(t)
	index := newNodeNameIndex(2)
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{secretInboxNodeClass{}},
		nil,
		false,
		almagest.WithNodeNameAvailableCallback(index.available),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck

	id, err := engine.AddNode(almagest.MainWorkspace, "test/SecretInbox", candidate)
	if err != nil {
		t.Fatal(err)
	}
	admin := &AdminAPI{nodeNames: index}
	subscriptionID := admin.startSecretNodeNameSync(engine, nil)
	if subscriptionID == 0 {
		t.Fatal("node name sync subscription failed")
	}
	defer engine.UnsubscribeNotifications(subscriptionID)

	engine.RemoveNode(almagest.MainWorkspace, id)
	newID, err := engine.AddNode(almagest.MainWorkspace, "test/SecretInbox", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, newID)
	if !ok {
		t.Fatal("new node was not found")
	}
	if snapshot.Name == candidate {
		t.Fatalf("generated node name = %q, want deleted name to be skipped", snapshot.Name)
	}
}

func TestSecretNodeNameSyncRemembersRenamedNodeName(t *testing.T) {
	candidate := firstGeneratedSecretInboxNodeName(t)
	index := newNodeNameIndex(2)
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{secretInboxNodeClass{}},
		nil,
		false,
		almagest.WithNodeNameAvailableCallback(index.available),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck

	id, err := engine.AddNode(almagest.MainWorkspace, "test/SecretInbox", candidate)
	if err != nil {
		t.Fatal(err)
	}
	admin := &AdminAPI{nodeNames: index}
	subscriptionID := admin.startSecretNodeNameSync(engine, nil)
	if subscriptionID == 0 {
		t.Fatal("node name sync subscription failed")
	}
	defer engine.UnsubscribeNotifications(subscriptionID)

	engine.SetNodeName(almagest.MainWorkspace, id, "renamed")
	newID, err := engine.AddNode(almagest.MainWorkspace, "test/SecretInbox", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, newID)
	if !ok {
		t.Fatal("new node was not found")
	}
	if snapshot.Name == candidate {
		t.Fatalf("generated node name = %q, want renamed name to be skipped", snapshot.Name)
	}
}

func TestConfiguredNodeNamesAreSorted(t *testing.T) {
	cfg := configer.NewMemory(map[string]any{
		"nodes": map[string]any{
			"zeta":  map[string]any{},
			"alpha": map[string]any{},
		},
	})
	if got, want := configuredNodeNames(cfg), []string{"alpha", "zeta"}; !slices.Equal(got, want) {
		t.Fatalf("configured node names = %q, want %q", got, want)
	}
}

func firstGeneratedSecretInboxNodeName(t *testing.T) string {
	t.Helper()
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{secretInboxNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck
	id, err := engine.AddNode(almagest.MainWorkspace, "test/SecretInbox", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, id)
	if !ok {
		t.Fatal("probe node was not found")
	}
	return snapshot.Name
}

func assertNodeNameAvailability(t *testing.T, index *nodeNameIndex, name string, want bool) {
	t.Helper()
	if got := index.available(name); got != want {
		t.Fatalf("node name %q availability = %v, want %v", name, got, want)
	}
}
