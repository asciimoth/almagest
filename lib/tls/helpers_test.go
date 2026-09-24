package tlsnodes

import (
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	gonnecttls "github.com/asciimoth/gonnect/tls"
	"github.com/asciimoth/pasta/pasta"
)

func TestTLSConfigParsingHelpers(t *testing.T) {
	if got, err := durationFromSeconds(-1); err == nil || got != 0 {
		t.Fatalf("negative duration = %v, %v", got, err)
	}
	if got, err := durationFromSeconds(0); err != nil || got != 0 {
		t.Fatalf("zero duration = %v, %v", got, err)
	}
	if got, err := durationFromSeconds(2); err != nil || got != 2*time.Second {
		t.Fatalf("duration = %v, %v", got, err)
	}
	for text, want := range map[string]gonnecttls.InterceptionFilterMode{
		"": gonnecttls.InterceptionFilterOff, "OFF": gonnecttls.InterceptionFilterOff,
		" inclusive ": gonnecttls.InterceptionFilterInclusive, "EXCLUSIVE": gonnecttls.InterceptionFilterExclusive,
	} {
		if got, err := parseInterceptionMode(text); err != nil || got != want {
			t.Errorf("parseInterceptionMode(%q) = %v, %v", text, got, err)
		}
	}
	if _, err := parseInterceptionMode("invalid"); err == nil {
		t.Fatal("invalid interception mode was accepted")
	}
	if formatInterceptionMode(" INCLUSIVE ") != "inclusive" || formatInterceptionMode("invalid") != "off" {
		t.Fatal("formatInterceptionMode returned an incorrect result")
	}
	for _, text := range []string{"", "any", "required", "true", "yes", "forbidden", "false", "no"} {
		if _, err := parseInterceptionFlag(text); err != nil {
			t.Errorf("parseInterceptionFlag(%q): %v", text, err)
		}
	}
	if _, err := parseInterceptionFlag("invalid"); err == nil {
		t.Fatal("invalid interception flag was accepted")
	}

	rulesText := `[{"networks":["tcp"],"conn_srcs":["a"],"conn_dsts":["b"],"sni_hosts":["example"],"alpns":["h2"],"sni_available":"required","sni_encrypted":"forbidden","tls_versions":[772]}]`
	rules, err := parseInterceptionRules(rulesText)
	if err != nil || len(rules) != 1 || rules[0].SNIAvailable != gonnecttls.InterceptionFlagRequired || rules[0].SNIEncrypted != gonnecttls.InterceptionFlagForbidden {
		t.Fatalf("rules = %#v, %v", rules, err)
	}
	if _, err := parseInterceptionRules(`[{"sni_available":"invalid"}]`); err == nil {
		t.Fatal("invalid available flag was accepted")
	}
	if _, err := parseInterceptionRules(`[{"sni_encrypted":"invalid"}]`); err == nil {
		t.Fatal("invalid encrypted flag was accepted")
	}
	if _, err := parseInterceptionRules(`{`); err == nil {
		t.Fatal("invalid rules JSON was accepted")
	}

	remaps, err := parseTerminatorRemaps(`[{"original_dsts":["a"],"sni_hosts":["b"],"alpns":["h2"],"dst":" target:443 "}]`)
	if err != nil || len(remaps) != 1 || remaps[0].Dst != "target:443" {
		t.Fatalf("remaps = %#v, %v", remaps, err)
	}
	if _, err := parseTerminatorRemaps(`{`); err == nil {
		t.Fatal("invalid remap JSON was accepted")
	}

	values := map[string]any{"string": "value", "int": 3, "bool": true}
	if formApplyString(values, "string", "fallback") != "value" || formApplyString(values, "missing", "fallback") != "fallback" {
		t.Fatal("formApplyString returned an incorrect result")
	}
	if formApplyInt(values, "int", 9) != 3 || formApplyInt(values, "missing", 9) != 9 {
		t.Fatal("formApplyInt returned an incorrect result")
	}
	if !formApplyBool(values, "bool", false) || !formApplyBool(values, "missing", true) {
		t.Fatal("formApplyBool returned an incorrect result")
	}

	cfg := configer.NewMemory(nil)
	if err := saveNodeConfig(cfg, map[string]any{"b": 2, "a": 1}); err != nil {
		t.Fatal(err)
	}
	if got := configer.Int(cfg, configer.Path{"a"}, 0); got != 1 {
		t.Fatalf("saved a = %d", got)
	}
}

func TestTLSNodeValidationAndSaveBranches(t *testing.T) {
	wrapper := newTLSWrapperNode(nil, nil)
	wrapper.config.leafTTLSeconds = -1
	wrapper.validateConfig()
	if wrapper.formErr == "" {
		t.Fatal("wrapper accepted negative leaf TTL")
	}
	wrapper.config.leafTTLSeconds = 1
	wrapper.config.serverName = ""
	wrapper.validateConfig()
	if wrapper.formErr == "" {
		t.Fatal("wrapper accepted empty server name")
	}
	if err := wrapper.OnSave(configer.NewMemory(nil)); err != nil {
		t.Fatal(err)
	}
	_ = wrapper.OnInbox(pasta.InboxMessage{Payload: "ignored"})
	_ = wrapper.OnFormularMsg("ignored")

	mitm := newTLSMITMNode(nil, nil)
	for _, edit := range []func(*tlsMITMConfig){
		func(c *tlsMITMConfig) { c.sniffBufferSize = -1 },
		func(c *tlsMITMConfig) { c.sniffBufferSize = 0; c.leafTTLSeconds = -1 },
		func(c *tlsMITMConfig) { c.leafTTLSeconds = 1; c.filterMode = "invalid" },
		func(c *tlsMITMConfig) { c.filterMode = "off"; c.filterRules = "{" },
	} {
		edit(&mitm.config)
		mitm.validateConfig()
		if mitm.formErr == "" {
			t.Fatal("MITM invalid config was accepted")
		}
	}
	if err := mitm.OnSave(configer.NewMemory(nil)); err != nil {
		t.Fatal(err)
	}
	_ = mitm.OnInbox(pasta.InboxMessage{Payload: "ignored"})
	_ = mitm.OnFormularMsg("ignored")

	terminator := newTLSTerminatorNode(nil, nil)
	for _, edit := range []func(*tlsTerminatorConfig){
		func(c *tlsTerminatorConfig) { c.sniffBufferSize = -1 },
		func(c *tlsTerminatorConfig) { c.sniffBufferSize = 0; c.leafTTLSeconds = -1 },
		func(c *tlsTerminatorConfig) { c.leafTTLSeconds = 1; c.destinationRemaps = "{" },
	} {
		edit(&terminator.config)
		terminator.validateConfig()
		if terminator.formErr == "" {
			t.Fatal("terminator invalid config was accepted")
		}
	}
	if err := terminator.OnSave(configer.NewMemory(nil)); err != nil {
		t.Fatal(err)
	}
	_ = terminator.OnInbox(pasta.InboxMessage{Payload: "ignored"})
	_ = terminator.OnFormularMsg("ignored")
}
