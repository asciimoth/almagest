package httpnodes

import (
	"context"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestHTTPClientMenuRefreshesTLSConfigListOnInbox(t *testing.T) {
	tlsStorage := testkit.NewTLSConfigStorage(map[string]string{
		nodekit.DefaultTLSConfigName: "default.test",
	})
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	if err := w.AddNodeClass(HTTPClientClass{Runtime: testRuntime{tlsConfigs: tlsStorage}}); err != nil {
		t.Fatalf("AddNodeClass HTTPClient: %v", err)
	}

	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("Subscribe returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	clientID, err := w.AddNodeByClass(NodeTypeHTTPClient, "client")
	if err != nil {
		t.Fatalf("AddNodeByClass HTTPClient: %v", err)
	}
	w.SubscribeNodeMenu(clientID, 1)
	assertConfigFieldAllowedValues(t, state, clientID, "request", nodeTLSConfigFieldID, []any{nodekit.DefaultTLSConfigName})

	tlsStorage.Set("custom", "custom.test")
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: clientID,
		Payload:      storage.TlsConfigChanged{NamePrev: "custom", NameNew: "custom"},
	})
	assertConfigFieldAllowedValues(t, state, clientID, "request", nodeTLSConfigFieldID, []any{nodekit.DefaultTLSConfigName, "custom"})
}

func TestHTTPClientRealtimeTLSConfigSelectorSavesSelection(t *testing.T) {
	tlsStorage := testkit.NewTLSConfigStorage(map[string]string{
		nodekit.DefaultTLSConfigName: "default.test",
		"custom":                     "custom.test",
	})
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	if err := w.AddNodeClass(HTTPClientClass{Runtime: testRuntime{tlsConfigs: tlsStorage}}); err != nil {
		t.Fatalf("AddNodeClass HTTPClient: %v", err)
	}
	clientID, err := w.AddNodeByClass(NodeTypeHTTPClient, "client")
	if err != nil {
		t.Fatalf("AddNodeByClass HTTPClient: %v", err)
	}

	w.SendNodeFormularMsg(clientID, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(clientID)},
		Field:       formular.FieldRef{BlockID: "request", FieldID: nodeTLSConfigFieldID},
		Value:       std.String(" custom "),
	})

	out := configer.NewMemory(nil)
	if err := w.SaveConfig(out); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := configer.String(out.View(configer.Path{"client"}), configer.Path{nodeTLSConfigFieldID}, "")
	if got != "custom" {
		t.Fatalf("saved TLS config = %q, want custom", got)
	}
	assertTLSGets(t, tlsStorage, []testkit.TLSConfigGet{
		{Name: nodekit.DefaultTLSConfigName, Node: "client"},
		{Name: "custom", Node: "client"},
	})
}

type testRuntime struct {
	tlsConfigs storage.TlsConfigStorage
}

func (r testRuntime) Context() context.Context { return context.Background() }

func (r testRuntime) Pool() bufpool.Pool { return nil }

func (r testRuntime) System() sysnet.System { return nil }

func (r testRuntime) KVManager() storage.KVManager { return nil }

func (r testRuntime) DnsCacheManager() storage.DnsCacheManager { return nil }

func (r testRuntime) SecretsStorage() storage.SecretsStorage { return nil }

func (r testRuntime) TlsConfigStorage() storage.TlsConfigStorage { return r.tlsConfigs }

func (r testRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner { return nil }

func (r testRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

func assertTLSGets(t *testing.T, store *testkit.TLSConfigStorage, want []testkit.TLSConfigGet) {
	t.Helper()
	if got := store.Gets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TLS config gets = %#v, want %#v", got, want)
	}
}

func assertConfigFieldAllowedValues(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string, want []any) {
	t.Helper()
	item := configFieldItem(t, state, node, blockID, fieldID)
	if item.Field == nil {
		t.Fatalf("%s.%s field missing", blockID, fieldID)
	}
	if !reflect.DeepEqual(item.AllowedValues, want) {
		t.Fatalf("%s.%s allowed values = %#v, want %#v", blockID, fieldID, item.AllowedValues, want)
	}
}

func configFieldItem(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string) formular.Item {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	block, ok := findBlock(snapshot, blockID)
	if !ok {
		t.Fatalf("%s block missing", blockID)
	}
	item, ok := findItem(block, fieldID)
	if !ok {
		t.Fatalf("%s.%s item missing", blockID, fieldID)
	}
	return item
}

func findBlock(snapshot formular.MenuSnapshotMessage, id string) (formular.Block, bool) {
	for _, block := range snapshot.Blocks {
		if block.ID == id {
			return block, true
		}
	}
	return formular.Block{}, false
}

func findItem(block formular.Block, id string) (formular.Item, bool) {
	for _, item := range block.Items {
		if item.ID == id {
			return item, true
		}
	}
	return formular.Item{}, false
}
