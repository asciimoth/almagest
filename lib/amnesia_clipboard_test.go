package almagest_test

import (
	"strings"
	"testing"

	amnesianodes "github.com/asciimoth/almagest/lib/amnesia"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

func TestAmnesiaCopyPasteDoesNotInheritUUIDOrSecretReferences(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	source := e.AddNode(amnesianodes.NodeTypeAmnesia, "source amnesia")
	e.SubscribeMenus(source)
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, source, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, source),
		BlockID:     "config",
		Values: map[string]any{
			"enabled":                true,
			"connectionSecret":       "source-connection-secret",
			"serverCountryCode":      "nl",
			"gatewayURL":             "https://gateway.example.test/",
			"gatewayPublicKeySecret": "source-gateway-key-secret",
		},
	})
	sourceUUID := waitAmnesiaMenuString(t, e, source, "status", "installationUUID", func(value string) bool {
		return value != ""
	})

	beforeCopy := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(beforeCopy); err != nil {
		t.Fatalf("save source config: %v", err)
	}
	sourceStorageID := configer.String(
		beforeCopy,
		configer.Path{"nodes", "source amnesia", "storageID"},
		"",
	)
	if sourceStorageID == "" {
		t.Fatal("source storage ID is empty")
	}

	clipboard := e.Engine.Copy(e2etest.E2EWorkspace, []uint64{source})
	if clipboard == "" {
		t.Fatal("copy returned an empty clipboard")
	}
	for _, sensitive := range []string{
		"source-connection-secret",
		"source-gateway-key-secret",
		sourceStorageID,
	} {
		if strings.Contains(clipboard, sensitive) {
			t.Fatalf("clipboard contains Amnesia client identity value %q", sensitive)
		}
	}

	pasted := e.Engine.Paste(e2etest.E2EWorkspace, clipboard)
	if len(pasted) != 1 {
		t.Fatalf("paste returned %d nodes (%v), want one", len(pasted), pasted)
	}
	copyNode := testkit.PastedNodeByClass(
		t,
		e.Engine,
		e2etest.E2EWorkspace,
		pasted,
		amnesianodes.NodeTypeAmnesia,
	)
	e.SubscribeMenus(copyNode)
	copyUUID := waitAmnesiaMenuString(t, e, copyNode, "status", "installationUUID", func(value string) bool {
		return value != ""
	})
	if copyUUID == sourceUUID {
		t.Fatalf("pasted UUID = source UUID %q", sourceUUID)
	}
	for _, field := range []string{"connectionSecret", "gatewayPublicKeySecret"} {
		if value := waitAmnesiaMenuString(t, e, copyNode, "config", field, func(string) bool { return true }); value != "" {
			t.Fatalf("pasted %s = %q, want empty", field, value)
		}
	}
	if country := waitAmnesiaMenuString(t, e, copyNode, "config", "serverCountryCode", func(string) bool { return true }); country != "nl" {
		t.Fatalf("pasted server country = %q, want nl", country)
	}
	if gateway := waitAmnesiaMenuString(t, e, copyNode, "config", "gatewayURL", func(string) bool { return true }); gateway != "https://gateway.example.test/" {
		t.Fatalf("pasted gateway URL = %q", gateway)
	}

	afterPaste := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(afterPaste); err != nil {
		t.Fatalf("save pasted config: %v", err)
	}
	copySnapshot, ok := e.Engine.NodeSnapshot(e2etest.E2EWorkspace, copyNode)
	if !ok {
		t.Fatal("pasted node snapshot is missing")
	}
	copyStorageID := configer.String(
		afterPaste,
		configer.Path{"nodes", copySnapshot.Name, "storageID"},
		"",
	)
	if copyStorageID == "" || copyStorageID == sourceStorageID {
		t.Fatalf("pasted storage ID = %q, source = %q", copyStorageID, sourceStorageID)
	}
}

func waitAmnesiaMenuString(
	t *testing.T,
	e *e2etest.NodeE2E,
	node uint64,
	blockID, fieldID string,
	accept func(string) bool,
) string {
	t.Helper()
	var value string
	testkit.Eventually(t, func() bool {
		item, ok := e.MenuItem(node, blockID, fieldID)
		if !ok {
			return false
		}
		value, ok = formular.StringValue(item.Value)
		return ok && accept(value)
	}, "Amnesia menu field %s/%s", blockID, fieldID)
	return value
}
