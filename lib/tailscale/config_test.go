package tailscalenodes

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgotailscale "github.com/asciimoth/wgo-tailscale"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func TestFreshTailscaleNodesUseDistinctStorageIDs(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
	t.Cleanup(func() { w.Close() })
	class := TailscaleCtrlClass{}

	nodes := make([]*tailscaleNode, 0, 2)
	for _, name := range []string{"Fresh Tailscale A", "Fresh Tailscale B"} {
		raw, err := class.NewNode(nil)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		node := raw.(*tailscaleNode)
		if _, err := w.AddNode(node, class.ClassName(), name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		if node.nodeName != name || node.config.storageID != tailscaleStorageID(name) {
			t.Fatalf("%s identity namespace = name %q storage %q", name, node.nodeName, node.config.storageID)
		}
		nodes = append(nodes, node)
	}
	if nodes[0].config.storageID == nodes[1].config.storageID {
		t.Fatalf("fresh Tailscale nodes share storage ID %q", nodes[0].config.storageID)
	}
}

func TestTailscaleStorageIDSurvivesConfigRoundTripAndRename(t *testing.T) {
	class := TailscaleCtrlClass{}
	raw, err := class.NewNode(nil)
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	node := raw.(*tailscaleNode)
	if err := node.OnInit(nil, nil, 0, NodeTypeTailscaleCtrl, &pasta.NodeInitData{Name: "Tailscale Service"}, false, false, false, false); err != nil {
		t.Fatalf("initialize node: %v", err)
	}
	want := tailscaleStorageID("Tailscale Service")

	saved := configer.NewMemory(map[string]any{})
	if err := node.OnSave(saved); err != nil {
		t.Fatalf("save node: %v", err)
	}
	restoredRaw, err := class.NewNode(saved)
	if err != nil {
		t.Fatalf("restore node: %v", err)
	}
	restored := restoredRaw.(*tailscaleNode)
	if err := restored.OnInit(nil, nil, 0, NodeTypeTailscaleCtrl, &pasta.NodeInitData{Name: "Renamed Tailscale"}, false, false, false, false); err != nil {
		t.Fatalf("initialize restored node: %v", err)
	}
	if restored.config.storageID != want {
		t.Fatalf("restored storage ID = %q, want %q", restored.config.storageID, want)
	}
}

func TestTailscaleClassPorts(t *testing.T) {
	params := (TailscaleCtrlClass{}).DefaultNodeParams()
	if params.PrimaryType != wireguardnodes.TypeWGCtrl {
		t.Fatalf("primary type = %q, want %q", params.PrimaryType, wireguardnodes.TypeWGCtrl)
	}
	if len(params.InitialPorts) != 4 {
		t.Fatalf("initial ports = %d, want 4", len(params.InitialPorts))
	}
	wants := []struct {
		direction string
		name      string
		typeName  string
	}{
		{direction: "left", name: "DNS", typeName: dnsnodes.TypeDNS},
		{direction: "right", name: "WireGuard", typeName: wireguardnodes.TypeWGCtrl},
		{direction: "right", name: "Network", typeName: nodekit.TypeNetwork},
		{direction: "right", name: "Firewall", typeName: std.TypeObject},
	}
	for i, want := range wants {
		port := params.InitialPorts[i]
		if port.Direction != want.direction || port.Name != want.name || len(port.Types) != 1 || port.Types[0] != want.typeName {
			t.Fatalf("port %d = %#v, want %s %s %s", i, port, want.direction, want.name, want.typeName)
		}
	}
}

func TestTailscaleConfigDefaultsNormalizeAndValidate(t *testing.T) {
	config := readTailscaleConfig(configer.NewMemory(map[string]any{
		fieldControlURL: " https://headscale.example.test/ ",
		fieldHostname:   " test-node ",
		fieldListenPort: 65535,
	}))
	if !config.enabled || config.controlURL != "https://headscale.example.test" || config.hostname != "test-node" {
		t.Fatalf("normalized config = %#v", config)
	}
	if err := validateTailscaleConfig(config); err != nil {
		t.Fatalf("validate normalized config: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*tailscaleConfig)
	}{
		{name: "control scheme", edit: func(c *tailscaleConfig) { c.controlURL = "ftp://headscale.test" }},
		{name: "missing host", edit: func(c *tailscaleConfig) { c.controlURL = "https:///control" }},
		{name: "negative port", edit: func(c *tailscaleConfig) { c.listenPort = -1 }},
		{name: "large port", edit: func(c *tailscaleConfig) { c.listenPort = 65536 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := config
			test.edit(&invalid)
			if err := validateTailscaleConfig(invalid); err == nil {
				t.Fatalf("validate %#v succeeded", invalid)
			}
		})
	}
	if got := defaultTailscaleConfig().controlURL; got != wgotailscale.DefaultControlURL {
		t.Fatalf("default control URL = %q, want %q", got, wgotailscale.DefaultControlURL)
	}
}

func TestTailscaleMenuShowsTailnetDataWithoutWireGuardStatusDuplication(t *testing.T) {
	online := true
	node := &tailscaleNode{
		config:   defaultTailscaleConfig(),
		firewall: emptyFirewallObject(),
		snapshot: wgotailscale.Snapshot{
			State:  wgotailscale.StateRunning,
			Domain: "example.test",
			Client: wgotailscale.ClientInfo{LoginName: "user@example.test", MapSequence: 7},
			Peers: []wgotailscale.PeerInfo{{
				Node: wgotailscale.NodeInfo{Name: "peer.example.test.", Online: &online},
				Path: wgotailscale.PathDirect,
			}},
			ACL: wgotailscale.ACLView{Revision: 3, Rules: []wgotailscale.ACLRule{{SourceIPs: []string{"100.64.0.1"}}}},
		},
	}
	block := node.statusBlock()
	items := make(map[string]formular.Item, len(block.Items))
	for _, item := range block.Items {
		items[item.ID] = item
	}
	for _, required := range []string{"login", "tailnet", "peers", "acl", "derp", "magicDNSRecords", "firewall"} {
		if _, ok := items[required]; !ok {
			t.Errorf("status menu has no %q field", required)
		}
	}
	for _, duplicate := range []string{"publicKey", "listenPort", "ownedIPs", "rxBytes", "txBytes", "latestHandshake"} {
		if _, ok := items[duplicate]; ok {
			t.Errorf("status menu duplicates WireGuard field %q", duplicate)
		}
	}
	if text, _ := formular.StringValue(items["acl"].Value); !strings.Contains(text, "100.64.0.1") {
		t.Fatalf("ACL field = %q, want source address", text)
	}
}

func TestInteractionMarkdownLinkOnlyAllowsHTTP(t *testing.T) {
	if got, ok := interactionMarkdownLink("https://login.example.test/a(b)"); !ok || got != "[Authorize this node](https://login.example.test/a%28b%29)" {
		t.Fatalf("HTTPS link = %q, %t", got, ok)
	}
	for _, raw := range []string{"javascript:alert(1)", "file:///tmp/auth", "not a URL"} {
		if got, ok := interactionMarkdownLink(raw); ok || got != "" {
			t.Fatalf("unsafe link %q = %q, %t", raw, got, ok)
		}
	}
}

func TestTailscaleNodeSecretValidationEdges(t *testing.T) {
	node := &tailscaleNode{nodeName: "test"}
	if err := node.validateAuthKeySecret(""); err != nil {
		t.Fatalf("empty optional secret error = %v", err)
	}
	if err := node.validateAuthKeySecret("auth"); err == nil {
		t.Fatal("auth secret was accepted without storage")
	}
	if _, err := node.readAuthKey(tailscaleConfig{authKeySecret: "auth"}, "test"); err == nil {
		t.Fatal("readAuthKey accepted unavailable storage")
	}
	if key, err := node.readAuthKey(tailscaleConfig{}, "test"); err != nil || key != "" {
		t.Fatalf("empty readAuthKey = %q, %v", key, err)
	}
	if got := node.currentNodeName(); got != "test" {
		t.Fatalf("currentNodeName = %q", got)
	}
	if description := (TailscaleCtrlClass{}).LongDescription(); description == "" {
		t.Fatal("Tailscale class has an empty long description")
	}
}

func TestTailscaleNodeConfigCommitBranches(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
	t.Cleanup(w.Close)
	class := TailscaleCtrlClass{}
	raw, err := class.NewNode(nil)
	if err != nil {
		t.Fatal(err)
	}
	node := raw.(*tailscaleNode)
	if _, err := w.AddNode(node, NodeTypeTailscaleCtrl, "Tailscale"); err != nil {
		t.Fatal(err)
	}
	if err := node.commitConfig(map[string]any{fieldControlURL: "ftp://invalid"}); err != nil {
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
		Field: formular.FieldRef{FieldID: fieldAuthKeySecret}, Value: "",
	})
}
