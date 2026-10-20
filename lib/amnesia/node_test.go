package amnesianodes

import (
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/wireguard"
)

func TestAmnesiaCtrlAddsDeprecationWarning(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(AmnesiaCtrlClass{}); err != nil {
		t.Fatalf("add AmnesiaCtrl class: %v", err)
	}

	id, err := w.AddNodeByClass(NodeTypeAmnesiaCtrl, "legacy")
	if err != nil {
		t.Fatalf("add AmnesiaCtrl node: %v", err)
	}
	snapshot, ok := w.NodeSnapshot(id)
	if !ok {
		t.Fatalf("AmnesiaCtrl node %d is missing", id)
	}
	if len(snapshot.Popups) != 1 {
		t.Fatalf("AmnesiaCtrl popups = %#v, want one deprecation warning", snapshot.Popups)
	}
	warning := snapshot.Popups[0]
	if warning.Type != pasta.NodePopupWard || warning.Text != amnesiaCtrlDeprecationWarning {
		t.Fatalf("AmnesiaCtrl popup = %#v, want warning %q", warning, amnesiaCtrlDeprecationWarning)
	}
}

func TestAmnesiaCtrlClassPortsAndMenu(t *testing.T) {
	class := AmnesiaCtrlClass{}
	if NodeTypeAmnesiaCtrl != "almagest/AmnesiaCtrl" {
		t.Fatalf("node type = %q, want %q", NodeTypeAmnesiaCtrl, "almagest/AmnesiaCtrl")
	}
	if got := class.ClassName(); got != NodeTypeAmnesiaCtrl {
		t.Fatalf("class name = %q, want %q", got, NodeTypeAmnesiaCtrl)
	}
	params := class.DefaultNodeParams()
	if params.PrimaryType != wireguardnodes.TypeWGCtrl || len(params.InitialPorts) != 2 {
		t.Fatalf("default params = %#v", params)
	}
	if port := params.InitialPorts[0]; port.Direction != "right" || port.Name != "WireGuard" || len(port.Types) != 1 || port.Types[0] != wireguardnodes.TypeWGCtrl {
		t.Fatalf("WireGuard port = %#v", port)
	}
	if port := params.InitialPorts[1]; port.Direction != "right" || port.Name != "Network" || len(port.Types) != 1 || port.Types[0] != nodekit.TypeNetwork {
		t.Fatalf("Network port = %#v", port)
	}

	raw, err := (AmnesiaCtrlClass{}).NewNode(configer.NewMemory(map[string]any{
		fieldEnabled: true, fieldConnectionSecret: "connection-name", fieldServerCountryCode: "DE",
	}))
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	node := raw.(*amnesiaNode)
	node.installationUUID = "2dd447bb-4c2f-4e8b-8da4-e9816b44bdf5"
	if node.config.serverCountryCode != "de" {
		t.Fatalf("normalized country = %q, want de", node.config.serverCountryCode)
	}
	for _, block := range []formular.Block{node.statusBlock(), node.configBlock()} {
		if block.ID == "" || len(block.Items) == 0 {
			t.Fatalf("menu block = %#v", block)
		}
	}
	connection, found := amnesiaMenuItem(node.configBlock(), fieldConnectionSecret)
	if !found || connection.Help != "" || connection.Field == nil || connection.Value != "connection-name" || connection.Selector != nodekit.SecretNameSelector {
		t.Fatalf("connection secret selector = %#v", connection)
	}
	for _, fieldID := range []string{fieldEnabled, fieldConnectEvent} {
		item, found := amnesiaMenuItem(node.configBlock(), fieldID)
		if !found || item.Help != "" || item.Field == nil {
			t.Fatalf("config field %q = %#v", fieldID, item)
		}
	}
	transport, found := amnesiaMenuItem(node.statusBlock(), "peerTransport")
	if !found || transport.Field == nil || !transport.Readonly || transport.Value != "WireGuard default" {
		t.Fatalf("peer transport status = %#v", transport)
	}
	uuid, found := amnesiaMenuItem(node.statusBlock(), "installationUUID")
	if !found || uuid.Field == nil || !uuid.Readonly || uuid.Value != node.installationUUID {
		t.Fatalf("installation UUID status = %#v", uuid)
	}
}

func TestAmnesiaConfigValidation(t *testing.T) {
	config := defaultAmnesiaConfig()
	if err := validateAmnesiaConfig(config); err == nil {
		t.Fatal("empty connection secret was accepted")
	}
	config.connectionSecret = "connection"
	config.serverCountryCode = "gbr"
	if err := validateAmnesiaConfig(config); err == nil {
		t.Fatal("three-letter server country was accepted")
	}
	config.serverCountryCode = "ge"
	config.gatewayURL = "file:///secret"
	if err := validateAmnesiaConfig(config); err == nil {
		t.Fatal("non-HTTP gateway URL was accepted")
	}
	config.gatewayURL = "https://gateway.example/"
	if err := validateAmnesiaConfig(config); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestAmnesiaFieldValidationDoesNotUseOtherUncommittedFields(t *testing.T) {
	config := defaultAmnesiaConfig()
	for _, fieldID := range []string{fieldServerCountryCode, fieldGatewayURL} {
		if err := validateAmnesiaConfigField(config, fieldID); err != nil {
			t.Errorf("empty optional field %q: %v", fieldID, err)
		}
	}

	config.serverCountryCode = "gbr"
	if err := validateAmnesiaConfigField(config, fieldServerCountryCode); err == nil {
		t.Error("invalid server country was accepted")
	}
	config.gatewayURL = "file:///secret"
	if err := validateAmnesiaConfigField(config, fieldGatewayURL); err == nil {
		t.Error("invalid gateway URL was accepted")
	}
}

func TestAmnesiaStorageIDSurvivesConfigRoundTripAndRename(t *testing.T) {
	raw, err := (AmnesiaCtrlClass{}).NewNode(configer.NewMemory(map[string]any{
		fieldConnectionSecret: "connection-name",
		fieldStorageID:        "invalid user value",
	}))
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	node := raw.(*amnesiaNode)
	if err := node.OnInit(nil, nil, 0, NodeTypeAmnesiaCtrl, &pasta.NodeInitData{Name: "Amnesia Service"}, false, false, false, false); err != nil {
		t.Fatalf("initialize node: %v", err)
	}
	want := amnesiaStorageID("Amnesia Service")
	if node.config.storageID != want {
		t.Fatalf("storage ID = %q, want %q", node.config.storageID, want)
	}

	saved := configer.NewMemory(map[string]any{})
	if err := node.OnSave(saved); err != nil {
		t.Fatalf("save node: %v", err)
	}
	restoredRaw, err := (AmnesiaCtrlClass{}).NewNode(saved)
	if err != nil {
		t.Fatalf("restore node: %v", err)
	}
	restored := restoredRaw.(*amnesiaNode)
	if err := restored.OnInit(nil, nil, 0, NodeTypeAmnesiaCtrl, &pasta.NodeInitData{Name: "Renamed Amnesia"}, false, false, false, false); err != nil {
		t.Fatalf("initialize restored node: %v", err)
	}
	if restored.config.storageID != want {
		t.Fatalf("restored storage ID = %q, want %q", restored.config.storageID, want)
	}
}

func TestAmnesiaNodeValidationAndFormattingEdges(t *testing.T) {
	node := &amnesiaNode{nodeName: "test"}
	if err := node.validateConnectionSecret(""); err == nil {
		t.Fatal("empty connection secret was accepted")
	}
	if err := node.validateConnectionSecret("connection"); err == nil {
		t.Fatal("connection secret was accepted without storage")
	}
	if err := node.validateGatewayPublicKeySecret(""); err != nil {
		t.Fatalf("empty gateway key secret error = %v", err)
	}
	if err := node.validateGatewayPublicKeySecret("gateway"); err == nil {
		t.Fatal("gateway key secret was accepted without storage")
	}
	node.secrets = &amnesiaTestSecrets{values: map[string]string{
		"invalid-connection": "not a profile",
		"empty-gateway":      " ",
		"gateway":            "public-key",
	}}
	if err := node.validateConnectionSecret("invalid-connection"); err == nil {
		t.Fatal("invalid connection value was accepted")
	}
	if err := node.validateGatewayPublicKeySecret("empty-gateway"); err == nil {
		t.Fatal("empty gateway key value was accepted")
	}
	if err := node.validateGatewayPublicKeySecret("gateway"); err != nil {
		t.Fatalf("gateway key validation error = %v", err)
	}
	if got := node.currentNodeName(); got != "test" {
		t.Fatalf("currentNodeName = %q", got)
	}
	if err := formatRuntimeError("prefix", nil); err != nil {
		t.Fatalf("formatRuntimeError(nil) = %v", err)
	}
	if got := amnesiaSubmittedFields(map[string]any{
		fieldEnabled: true, fieldConnectionSecret: "x", fieldServerCountryCode: "de",
		fieldConnectEvent: true, nodekit.TLSConfigFieldID: "tls", fieldGatewayURL: "url",
		fieldGatewayPublicKeySecret: "key", "unknown": true,
	}); got != "enabled,connectionSecret,serverCountryCode,connectEvent,tlsConfig,gatewayURL,gatewayPublicKeySecret" {
		t.Fatalf("amnesiaSubmittedFields = %q", got)
	}
	if description := (AmnesiaCtrlClass{}).LongDescription(); description == "" {
		t.Fatal("Amnesia class has an empty long description")
	}
}

func TestAmnesiaNodeConfigCommitBranches(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
	t.Cleanup(w.Close)
	class := AmnesiaCtrlClass{}
	raw, err := class.NewNode(nil)
	if err != nil {
		t.Fatal(err)
	}
	node := raw.(*amnesiaNode)
	node.config.connectionSecret = "connection"
	if _, err := w.AddNode(node, NodeTypeAmnesiaCtrl, "Amnesia"); err != nil {
		t.Fatal(err)
	}
	if err := node.commitConfig(map[string]any{fieldServerCountryCode: "invalid"}); err != nil {
		t.Fatal(err)
	}
	if node.formError == "" {
		t.Fatal("invalid config did not set a form error")
	}
	if err := node.commitConfig(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if node.formError != "" {
		t.Fatalf("unchanged config form error = %q", node.formError)
	}
	if err := node.commitConfig(map[string]any{fieldEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if node.config.enabled {
		t.Fatal("enabled config did not change")
	}
	menuID := pasta.NodeMenuID(node.ID)
	_ = node.OnFormularMsg(formular.FormApplyMessage{MessageBase: formular.MessageBase{MenuID: "wrong"}, BlockID: "config"})
	_ = node.OnFormularMsg(formular.FormApplyMessage{MessageBase: formular.MessageBase{MenuID: menuID}, BlockID: "wrong"})
	_ = node.OnFormularMsg(formular.FormApplyMessage{MessageBase: formular.MessageBase{MenuID: menuID}, BlockID: "config", Values: map[string]any{fieldEnabled: false}})
	_ = node.OnFormularMsg(formular.FieldValidateMessage{MessageBase: formular.MessageBase{MenuID: "wrong"}})
	_ = node.OnFormularMsg(formular.ButtonPressMessage{MessageBase: formular.MessageBase{MenuID: "wrong"}})
	_ = node.OnInbox(pasta.InboxMessage{Payload: "ignored"})
	_ = node.OnInbox(pasta.InboxMessage{Payload: storage.SecretValueChanged{SecretNamePrev: "unrelated", SecretNameNew: "unrelated"}})
	node.validateConfigField(formular.FieldValidateMessage{
		Field: formular.FieldRef{FieldID: fieldGatewayPublicKeySecret}, Value: "",
	})
}

func amnesiaMenuItem(block formular.Block, id string) (formular.Item, bool) {
	for _, item := range block.Items {
		if item.ID == id {
			return item, true
		}
	}
	return formular.Item{}, false
}
