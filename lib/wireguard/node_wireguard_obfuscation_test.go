package wireguardnodes

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
)

func TestWireGuardObfuscationMenuConditions(t *testing.T) {
	configBlock := (&wireGuardNode{config: defaultWireGuardConfig()}).configBlock()
	if err := configBlock.Validate(); err != nil {
		t.Fatalf("config block validation: %v", err)
	}
	toggle := wireGuardTestBlockItem(t, configBlock, wireGuardAmneziaEnabledField)
	if enabled, ok := formular.BoolValue(toggle.Value); !ok || enabled {
		t.Fatalf("node AWG toggle = %#v, want false", toggle.Value)
	}
	awgField := wireGuardTestBlockItem(t, configBlock, "amneziaJunkCount")
	wireGuardTestVisibleEquals(t, awgField, wireGuardAmneziaEnabledField, true)

	template := wireGuardPeerTemplate()
	if err := template.Validate(); err != nil {
		t.Fatalf("peer template validation: %v", err)
	}
	peerToggle := wireGuardTestTemplateItem(t, template, wireGuardAmneziaEnabledField)
	if enabled, ok := formular.BoolValue(peerToggle.Value); !ok || enabled {
		t.Fatalf("peer AWG toggle = %#v, want false", peerToggle.Value)
	}
	wireGuardTestVisibleEquals(t, peerToggle, "amneziaOverride", true)

	peerAWGField := wireGuardTestTemplateItem(t, template, "amneziaJunkCount")
	conditions := peerAWGField.StateConditions
	if conditions == nil || conditions.Visible == nil || len(conditions.Visible.All) != 2 {
		t.Fatalf("peer AWG visibility condition = %#v, want two conditions", conditions)
	}
	wireGuardTestConditionEquals(t, conditions.Visible.All[0], "amneziaOverride", true)
	wireGuardTestConditionEquals(t, conditions.Visible.All[1], wireGuardAmneziaEnabledField, true)
}

func TestWireGuardDisabledObfuscationUsesDefaultDeviceValues(t *testing.T) {
	privateKey, _ := wireGuardTestKeyPair(t, 201)
	badAWG := wireGuardTestAmneziaConfig()
	badAWG.initHeader = "invalid"
	config := wireGuardConfig{
		privateKey: privateKey,
		amneziaWG:  badAWG,
	}

	deviceConfig, err := wireGuardDeviceConfig(config)
	if err != nil {
		t.Fatalf("disabled AWG config: %v", err)
	}
	if want := wgdevice.DefaultAmneziaWGConfig(); !reflect.DeepEqual(deviceConfig.AmneziaWG, want) {
		t.Fatalf("disabled AWG config = %#v, want defaults %#v", deviceConfig.AmneziaWG, want)
	}

	config.amneziaWGEnabled = true
	if _, err := wireGuardDeviceConfig(config); err == nil {
		t.Fatal("enabled invalid AWG config succeeded")
	}
}

func TestWireGuardDisabledPeerObfuscationUsesDefaultOverride(t *testing.T) {
	_, publicKey := wireGuardTestKeyPair(t, 202)
	badAWG := wireGuardTestAmneziaConfig()
	badAWG.initHeader = "invalid"
	peer := wireGuardPeerConfig{
		id:                "plain-peer",
		publicKey:         publicKey,
		amneziaWGOverride: true,
		amneziaWG:         badAWG,
	}

	peerConfig, err := wireGuardPeerDeviceConfig(peer)
	if err != nil {
		t.Fatalf("disabled peer AWG config: %v", err)
	}
	if peerConfig.AmneziaWG == nil {
		t.Fatal("disabled peer AWG config has no override")
	}
	if want := wgdevice.DefaultAmneziaWGConfig(); !reflect.DeepEqual(*peerConfig.AmneziaWG, want) {
		t.Fatalf("disabled peer AWG config = %#v, want defaults %#v", *peerConfig.AmneziaWG, want)
	}

	peer.amneziaWGEnabled = true
	if _, err := wireGuardPeerDeviceConfig(peer); err == nil {
		t.Fatal("enabled invalid peer AWG config succeeded")
	}

	peer.amneziaWGOverride = false
	peerConfig, err = wireGuardPeerDeviceConfig(peer)
	if err != nil {
		t.Fatalf("inherited peer AWG config: %v", err)
	}
	if peerConfig.AmneziaWG != nil {
		t.Fatalf("inherited peer AWG config = %#v, want nil", peerConfig.AmneziaWG)
	}
}

func TestWireGuardLegacyObfuscationConfigMigration(t *testing.T) {
	legacyAWG := wireGuardTestAmneziaConfig()
	legacy := defaultWireGuardConfig()
	legacy.read(configer.NewMemory(map[string]any{
		"amneziaWG": wireGuardAmneziaConfigMap(legacyAWG),
	}))
	if !legacy.amneziaWGEnabled {
		t.Fatal("legacy non-default node AWG profile was not enabled")
	}

	explicitlyDisabled := defaultWireGuardConfig()
	explicitlyDisabled.read(configer.NewMemory(map[string]any{
		wireGuardAmneziaEnabledField: false,
		"amneziaWG":                  wireGuardAmneziaConfigMap(legacyAWG),
	}))
	if explicitlyDisabled.amneziaWGEnabled {
		t.Fatal("explicitly disabled node AWG profile was enabled")
	}

	_, publicKey := wireGuardTestKeyPair(t, 203)
	legacyPeer := wireGuardPeerFromConfigMap(0, map[string]any{
		"id":                "legacy-peer",
		"publicKey":         publicKey,
		"amneziaWGOverride": true,
		"amneziaWG":         wireGuardAmneziaConfigMap(legacyAWG),
	})
	if !legacyPeer.amneziaWGEnabled {
		t.Fatal("legacy peer AWG override was not enabled")
	}
	explicitlyDisabledPeer := wireGuardPeerFromConfigMap(0, map[string]any{
		"id":                         "plain-peer",
		"publicKey":                  publicKey,
		"amneziaWGOverride":          true,
		wireGuardAmneziaEnabledField: false,
		"amneziaWG":                  wireGuardAmneziaConfigMap(legacyAWG),
	})
	if explicitlyDisabledPeer.amneziaWGEnabled {
		t.Fatal("explicitly disabled peer AWG override was enabled")
	}
}

func TestWireGuardObjectPeerObfuscationCompatibility(t *testing.T) {
	_, publicKey := wireGuardTestKeyPair(t, 204)
	legacyAWG := wireGuardTestAmneziaConfig()
	for _, tc := range []struct {
		name        string
		toggle      string
		wantEnabled bool
		wantDefault bool
	}{
		{name: "legacy override", wantEnabled: true},
		{name: "explicitly disabled", toggle: `,"amneziaWGEnabled":false`, wantDefault: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object, err := std.ObjectFromJSON([]byte(fmt.Sprintf(`{
				"peers":[{
					"id":"object-peer",
					"publicKey":%q,
					"amneziaWGOverride":true,
					"amneziaWG":%s
					%s
				}]
			}`, publicKey, wireGuardTestJSON(t, wireGuardAmneziaConfigMap(legacyAWG)), tc.toggle)))
			if err != nil {
				t.Fatalf("ObjectFromJSON: %v", err)
			}
			peers, err := wireGuardPeersFromObject(object)
			if err != nil || len(peers) != 1 {
				t.Fatalf("wireGuardPeersFromObject = %#v, %v", peers, err)
			}
			if peers[0].amneziaWGEnabled != tc.wantEnabled {
				t.Fatalf("object peer AWG enabled = %t, want %t", peers[0].amneziaWGEnabled, tc.wantEnabled)
			}
			peerConfig, err := wireGuardPeerDeviceConfig(peers[0])
			if err != nil || peerConfig.AmneziaWG == nil {
				t.Fatalf("object peer device config = %#v, %v", peerConfig, err)
			}
			gotDefault := reflect.DeepEqual(*peerConfig.AmneziaWG, wgdevice.DefaultAmneziaWGConfig())
			if gotDefault != tc.wantDefault {
				t.Fatalf("object peer uses defaults = %t, want %t", gotDefault, tc.wantDefault)
			}
		})
	}
}

func wireGuardTestBlockItem(t *testing.T, block formular.Block, id string) formular.Item {
	t.Helper()
	for _, item := range block.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("block item %q is missing", id)
	return formular.Item{}
}

func wireGuardTestTemplateItem(t *testing.T, template formular.ArrayTemplate, id string) formular.Item {
	t.Helper()
	for _, item := range template.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("template item %q is missing", id)
	return formular.Item{}
}

func wireGuardTestVisibleEquals(t *testing.T, item formular.Item, fieldID string, value any) {
	t.Helper()
	if item.StateConditions == nil || item.StateConditions.Visible == nil {
		t.Fatalf("item %q has no visibility condition", item.ID)
	}
	wireGuardTestConditionEquals(t, *item.StateConditions.Visible, fieldID, value)
}

func wireGuardTestConditionEquals(t *testing.T, condition formular.StateCondition, fieldID string, value any) {
	t.Helper()
	if condition.Source == nil || condition.Source.BlockID != "" || condition.Source.FieldID != fieldID ||
		condition.Operator != formular.StateOperatorEquals || !reflect.DeepEqual(condition.Value, value) {
		t.Fatalf("condition = %#v, want relative %s == %#v", condition, fieldID, value)
	}
}

func wireGuardTestJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return string(data)
}
