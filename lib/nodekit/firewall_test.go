package nodekit

import (
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestParseFirewallConfigJSONAndFormatRoundTrip(t *testing.T) {
	t.Parallel()
	text := `{
		"exclude": [{"network":" TCP ","hosts":["API.EXAMPLE."],"ports":[443]}],
		"include": [{"network":"udp","localHosts":["192.0.2.1"],"portRanges":[{"first":5001,"last":5000}]}],
		"responseTTL":"5m"
	}`
	config, err := ParseFirewallConfigJSON(text)
	if err != nil {
		t.Fatalf("ParseFirewallConfigJSON() error = %v", err)
	}
	if config.ResponseTTL != 5*time.Minute {
		t.Fatalf("ResponseTTL = %v, want 5m", config.ResponseTTL)
	}
	if !config.BlocksOutgoing("tcp", "api.example:443") {
		t.Fatal("parsed config does not block its outgoing rule")
	}
	if !config.AllowsIncoming("udp", "198.51.100.2:9", "192.0.2.1:5000") {
		t.Fatal("parsed config does not allow its incoming rule")
	}

	formatted := FormatFirewallConfigJSON(config)
	if strings.Contains(formatted, "DNSCache") || !strings.Contains(formatted, `"responseTTL": "5m0s"`) {
		t.Fatalf("formatted config = %s", formatted)
	}
	roundTrip, err := ParseFirewallConfigJSON(formatted)
	if err != nil {
		t.Fatalf("parse formatted config: %v", err)
	}
	if !roundTrip.BlocksOutgoing("tcp", "api.example:443") || roundTrip.ResponseTTL != config.ResponseTTL {
		t.Fatalf("round-trip config = %#v, want equivalent to %#v", roundTrip, config)
	}
}

func TestFirewallConfigStructuredValueAndLegacyText(t *testing.T) {
	t.Parallel()
	config, err := ParseFirewallConfigJSON(`{
		"exclude":[{"network":"tcp","ports":[443]}],
		"responseTTL":"90s"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	value := FirewallConfigValue(config)
	if _, ok := value.(string); ok {
		t.Fatal("FirewallConfigValue returned a serialized string")
	}
	root, ok := configer.MapStringAny(value)
	if !ok {
		t.Fatalf("FirewallConfigValue() = %T, want config map", value)
	}
	if _, ok := root["exclude"]; !ok {
		t.Fatalf("structured config has no exclude subgraph: %#v", root)
	}

	structured := configer.NewMemory(map[string]any{"rules": value})
	text := FirewallConfigTextFromConfig(structured, configer.Path{"rules"})
	roundTrip, err := ParseFirewallConfigJSON(text)
	if err != nil {
		t.Fatalf("parse structured config text: %v", err)
	}
	if !roundTrip.BlocksOutgoing("tcp", "example.test:443") || roundTrip.ResponseTTL != 90*time.Second {
		t.Fatalf("structured round trip = %#v", roundTrip)
	}

	legacy := configer.NewMemory(map[string]any{"rules": `{"exclude":[{"network":"udp","ports":[53]}]}`})
	legacyText := FirewallConfigTextFromConfig(legacy, configer.Path{"rules"})
	legacyConfig, err := ParseFirewallConfigJSON(legacyText)
	if err != nil {
		t.Fatalf("parse legacy config text: %v", err)
	}
	if !legacyConfig.BlocksOutgoing("udp", "example.test:53") {
		t.Fatal("legacy string config was not loaded")
	}
}

func TestParseFirewallConfigJSONRejectsUnsafeAmbiguity(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		``,
		`{"excldue":[]}`,
		`{"responseTTL":"invalid"}`,
		`{"responseTTL":"0s"}`,
		`{"exclude":[]} {}`,
		`{"exclude":[{"ports":[70000]}]}`,
	} {
		if _, err := ParseFirewallConfigJSON(text); err == nil {
			t.Errorf("ParseFirewallConfigJSON(%q) error = nil", text)
		}
	}
}

func TestFirewallConfigFromObjectAndMergeByLink(t *testing.T) {
	t.Parallel()
	menu, err := ParseFirewallConfigJSON(`{"exclude":[{"network":"tcp","ports":[80]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	object, err := std.ObjectFromJSON([]byte(`{"exclude":[{"network":"udp","ports":[53]}],"responseTTL":"3m"}`))
	if err != nil {
		t.Fatal(err)
	}
	input, err := FirewallConfigFromObject(object)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeFirewallConfigMap(menu, map[uint64]*gonnect.FirewallConfig{2: input})
	if !merged.BlocksOutgoing("tcp", "127.0.0.1:80") || !merged.BlocksOutgoing("udp", "127.0.0.1:53") {
		t.Fatal("merged config does not contain both policy sources")
	}
	if merged.ResponseTTL != 3*time.Minute {
		t.Fatalf("merged ResponseTTL = %v, want 3m", merged.ResponseTTL)
	}
}
