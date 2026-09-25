package experimentalnodes

import (
	"math"
	"strings"
	"testing"

	"github.com/asciimoth/formular"
)

func TestXTorMenuKeepsHostPolicyOutOfWorkspaceForm(t *testing.T) {
	node := &xTorDriverNode{config: defaultXTorDriverConfig()}
	settings := node.settingsBlock()
	if len(settings.Items) != 2 || settings.Items[0].ID != xTorFieldEnabled || settings.Items[1].ID != xTorFieldMaxProxyConnections {
		t.Fatalf("node settings = %#v", settings.Items)
	}
	authorization := xTorClientAuthorizationTemplate()
	if len(authorization.Items) != 3 {
		t.Fatalf("client authorization fields = %#v", authorization.Items)
	}
	for _, item := range authorization.Items {
		if item.ID == "generate" {
			t.Fatal("client authorization still exposes an opt-in key generation control")
		}
	}
}

func TestXTorServiceKeyNameAcceptsArbitraryDisplayName(t *testing.T) {
	name := xTorServiceKeyName("Test service / 私密")
	if !strings.HasPrefix(name, "service-") || len(name) > 128 {
		t.Fatalf("internal service key name = %q", name)
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			t.Fatalf("internal service key name %q contains invalid character %q", name, char)
		}
	}
	if name == xTorServiceKeyName("Another service") {
		t.Fatal("different service names produced the same internal key name")
	}
}

func TestXTorMenuPolicyParserPreservesUint64Values(t *testing.T) {
	config := defaultXTorDriverConfig()
	values := map[string]any{
		xTorFieldOutboundFailures: "default",
		xTorFieldBandwidthRate:    "18446744073709551614", xTorFieldBandwidthBurst: "18446744073709551615",
		xTorFieldConnectionPadding: "default", xTorFieldCircuitPadding: "default", xTorFieldReachableORPorts: "80, 443",
		xTorFieldMaxPendingCircuits: 1024, xTorFieldNumCPUs: 12, xTorFieldClientIP: "default",
		xTorFieldOnionTraffic: "default", xTorFieldEntryNodes: "", xTorFieldExitNodes: "",
		xTorFieldExcludeNodes: "", xTorFieldExcludeExitNodes: "", xTorFieldStrictNodes: true,
	}
	if err := applyXTorPolicies(&config, values); err != nil {
		t.Fatal(err)
	}
	if config.Driver.BandwidthRate != math.MaxUint64-1 || config.Driver.BandwidthBurst != math.MaxUint64 {
		t.Fatalf("bandwidth values = %d, %d", config.Driver.BandwidthRate, config.Driver.BandwidthBurst)
	}
	if len(config.Driver.ReachableORPorts) != 2 || config.Driver.ReachableORPorts[0] != 80 || config.Driver.ReachableORPorts[1] != 443 {
		t.Fatalf("reachable ports = %#v", config.Driver.ReachableORPorts)
	}
	if config.Driver.MaxPendingCircuits != 1024 {
		t.Fatalf("maximum pending circuits = %d, want 1024", config.Driver.MaxPendingCircuits)
	}
}

func TestXTorMenuPolicyParserRejectsDriverExcessPendingCircuits(t *testing.T) {
	config := defaultXTorDriverConfig()
	values := map[string]any{
		xTorFieldOutboundFailures: "default",
		xTorFieldBandwidthRate:    "0", xTorFieldBandwidthBurst: "0",
		xTorFieldConnectionPadding: "default", xTorFieldCircuitPadding: "default",
		xTorFieldReachableORPorts: "", xTorFieldMaxPendingCircuits: 1025,
		xTorFieldNumCPUs: 0, xTorFieldClientIP: "default", xTorFieldOnionTraffic: "default",
		xTorFieldEntryNodes: "", xTorFieldExitNodes: "", xTorFieldExcludeNodes: "",
		xTorFieldExcludeExitNodes: "", xTorFieldStrictNodes: false,
	}
	if err := applyXTorPolicies(&config, values); err == nil || !strings.Contains(err.Error(), "between 0 and 1024") {
		t.Fatalf("excess pending circuits error = %v", err)
	}
}

func TestXTorMenuBridgeParserRejectsUnknownTemplate(t *testing.T) {
	config := defaultXTorDriverConfig()
	err := applyXTorBridges(&config, map[string]any{
		xTorFieldUseBridges: true, xTorFieldEnableObfs4: false,
		xTorFieldBridges: []formular.ArrayElementValue{{
			ID: "bridge-1", Template: "snowflake", Values: map[string]any{"address": "192.0.2.1:443", "fingerprint": ""},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("unknown bridge template error = %v", err)
	}
}

func TestXTorMenuServiceParserRejectsDuplicatePort(t *testing.T) {
	config := defaultXTorDriverConfig()
	err := applyXTorServices(&config, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{
			ID: "service-1", Template: "service", Values: map[string]any{
				"name": "web", "ports": "443, 443", "maxStreams": 0,
				"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate port 443") {
		t.Fatalf("duplicate service port error = %v", err)
	}
}
