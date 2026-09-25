package almagest

import (
	"encoding/json"
	"testing"
)

func TestSanitizeXTorDriverClipboardRemovesNodeScopedSelectors(t *testing.T) {
	input := `{
		"version": 1,
		"nodes": [{
			"class": "almagest/XTorDriver",
			"config": {"configuration": {
				"enabled": true,
				"services": [
					{"name": "one", "keySecret": "service-secret", "ports": [80]},
					{"name": "two", "ports": [443]}
				],
				"clientAuthorizations": [
					{"address": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.onion", "privateKeySecret": "client-secret"}
				]
			}}
		}, {
			"class": "example/Other",
			"config": {"configuration": {"services": [{"keySecret": "keep"}]}}
		}]
	}`

	var payload map[string]any
	if err := json.Unmarshal([]byte(sanitizeXTorDriverClipboard(input)), &payload); err != nil {
		t.Fatal(err)
	}
	nodes := payload["nodes"].([]any)
	torConfig := nodes[0].(map[string]any)["config"].(map[string]any)["configuration"].(map[string]any)
	services := torConfig["services"].([]any)
	if _, ok := services[0].(map[string]any)["keySecret"]; ok {
		t.Fatal("onion service selector remained in clipboard")
	}
	if got := services[0].(map[string]any)["name"]; got != "one" {
		t.Fatalf("service name = %#v, want one", got)
	}
	authorizations := torConfig["clientAuthorizations"].([]any)
	if _, ok := authorizations[0].(map[string]any)["privateKeySecret"]; ok {
		t.Fatal("client authorization selector remained in clipboard")
	}
	other := nodes[1].(map[string]any)["config"].(map[string]any)["configuration"].(map[string]any)
	if got := other["services"].([]any)[0].(map[string]any)["keySecret"]; got != "keep" {
		t.Fatalf("unrelated selector = %#v, want keep", got)
	}
}

func TestSanitizeXTorDriverClipboardKeepsUnchangedAndMalformedData(t *testing.T) {
	for _, input := range []string{
		"",
		"not json",
		`{"version":1,"nodes":[{"class":"almagest/XTorDriver","config":{"configuration":{"services":[{"name":"one"}]}}}]}`,
		`{"version":1,"nodes":[{"class":"example/Other","config":{"configuration":{"services":[{"keySecret":"keep"}]}}}]}`,
	} {
		if got := sanitizeXTorDriverClipboard(input); got != input {
			t.Fatalf("sanitizeXTorDriverClipboard(%q) = %q, want unchanged", input, got)
		}
	}
}
