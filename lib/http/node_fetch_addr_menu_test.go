package httpnodes

import (
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func TestFetchAddrClassShapeAndMenu(t *testing.T) {
	t.Parallel()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	if err := w.AddNodeClass(FetchAddrClass{}); err != nil {
		t.Fatalf("AddNodeClass: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeFetchAddr, "fetch")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	defer w.Close()

	snapshot, ok := w.NodeSnapshot(node)
	if !ok {
		t.Fatal("node snapshot missing")
	}
	if len(snapshot.LeftPorts) != 1 || len(snapshot.RightPorts) != 8 {
		t.Fatalf("ports = %d left and %d right, want 1 left and 8 right", len(snapshot.LeftPorts), len(snapshot.RightPorts))
	}

	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	defer w.UnsubscribeNotifications(sub)
	w.SubscribeNodeMenu(node, sub)

	provider := configFieldItem(t, state, node, "check", "provider")
	if provider.Field == nil || provider.Value != DefaultFetchAddrProvider {
		t.Fatalf("provider field = %#v, want default %q", provider.Field, DefaultFetchAddrProvider)
	}
	if want := []any{FetchAddrProviderIPify, FetchAddrProviderIfConfig}; !reflect.DeepEqual(provider.AllowedValues, want) {
		t.Fatalf("provider choices = %#v, want %#v", provider.AllowedValues, want)
	}
	tlsConfig := configFieldItem(t, state, node, "check", nodeTLSConfigFieldID)
	if tlsConfig.Field == nil || tlsConfig.Value != "default" {
		t.Fatalf("TLS config selector = %#v, want default", tlsConfig.Field)
	}
	for _, field := range []string{"error", "publicIP", "countryCode", "city", "latitude", "longitude"} {
		item := configFieldItem(t, state, node, "result", field)
		if item.Field == nil || !item.Readonly {
			t.Fatalf("result field %q is not read-only: %#v", field, item.Field)
		}
	}
}

func TestFetchAddrProviderSelectionSaves(t *testing.T) {
	t.Parallel()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	if err := w.AddNodeClass(FetchAddrClass{}); err != nil {
		t.Fatalf("AddNodeClass: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeFetchAddr, "fetch")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	defer w.Close()

	w.SendNodeFormularMsg(node, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(node)},
		Field:       formular.FieldRef{BlockID: "check", FieldID: "provider"},
		Value:       FetchAddrProviderIPify,
	})
	out := configer.NewMemory(nil)
	if err := w.SaveConfig(out); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	view := out.View(configer.Path{"fetch"})
	if got := configer.String(view, configer.Path{"provider"}, ""); got != FetchAddrProviderIPify {
		t.Fatalf("saved provider = %q, want %q", got, FetchAddrProviderIPify)
	}
}
