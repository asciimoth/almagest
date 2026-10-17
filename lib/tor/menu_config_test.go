package tornodes

import (
	"math"
	"strings"
	"testing"

	"github.com/asciimoth/formular"
)

func TestTorMenuKeepsHostPolicyOutOfWorkspaceForm(t *testing.T) {
	node := &torDriverNode{config: defaultTorDriverConfig()}
	settings := node.settingsBlock()
	if len(settings.Items) != 2 || settings.Items[0].ID != torFieldEnabled || settings.Items[1].ID != torFieldMaxProxyConnections {
		t.Fatalf("node settings = %#v", settings.Items)
	}
	authorization := torClientAuthorizationTemplate()
	if len(authorization.Items) != 3 {
		t.Fatalf("client authorization fields = %#v", authorization.Items)
	}
	for _, item := range authorization.Items {
		if item.ID == "generate" {
			t.Fatal("client authorization still exposes an opt-in key generation control")
		}
	}
}

func TestTorServiceKeyNameAcceptsArbitraryDisplayName(t *testing.T) {
	name := torServiceKeyName("Test service / 私密")
	if !strings.HasPrefix(name, "service-") || len(name) > 128 {
		t.Fatalf("internal service key name = %q", name)
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			t.Fatalf("internal service key name %q contains invalid character %q", name, char)
		}
	}
	if name == torServiceKeyName("Another service") {
		t.Fatal("different service names produced the same internal key name")
	}
}

func TestTorMenuPolicyParserPreservesUint64Values(t *testing.T) {
	config := defaultTorDriverConfig()
	values := map[string]any{
		torFieldOutboundFailures: "default",
		torFieldBandwidthRate:    "18446744073709551614", torFieldBandwidthBurst: "18446744073709551615",
		torFieldConnectionPadding: "default", torFieldCircuitPadding: "default", torFieldReachableORPorts: "80, 443",
		torFieldMaxPendingCircuits: 1024, torFieldNumCPUs: 12, torFieldClientIP: "default",
		torFieldOnionTraffic: "default", torFieldEntryNodes: "", torFieldExitNodes: "",
		torFieldExcludeNodes: "", torFieldExcludeExitNodes: "", torFieldStrictNodes: true,
	}
	if err := applyTorPolicies(&config, values); err != nil {
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

func TestTorMenuPolicyParserRejectsDriverExcessPendingCircuits(t *testing.T) {
	config := defaultTorDriverConfig()
	values := map[string]any{
		torFieldOutboundFailures: "default",
		torFieldBandwidthRate:    "0", torFieldBandwidthBurst: "0",
		torFieldConnectionPadding: "default", torFieldCircuitPadding: "default",
		torFieldReachableORPorts: "", torFieldMaxPendingCircuits: 1025,
		torFieldNumCPUs: 0, torFieldClientIP: "default", torFieldOnionTraffic: "default",
		torFieldEntryNodes: "", torFieldExitNodes: "", torFieldExcludeNodes: "",
		torFieldExcludeExitNodes: "", torFieldStrictNodes: false,
	}
	if err := applyTorPolicies(&config, values); err == nil || !strings.Contains(err.Error(), "between 0 and 1024") {
		t.Fatalf("excess pending circuits error = %v", err)
	}
}

func TestTorMenuBridgeParserRejectsUnknownTemplate(t *testing.T) {
	config := defaultTorDriverConfig()
	err := applyTorBridges(&config, map[string]any{
		torFieldUseBridges: true, torFieldEnableObfs4: false,
		torFieldBridges: []formular.ArrayElementValue{{
			ID: "bridge-1", Template: "snowflake", Values: map[string]any{"address": "192.0.2.1:443", "fingerprint": ""},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("unknown bridge template error = %v", err)
	}
}

func TestTorMenuServiceParserRejectsDuplicatePort(t *testing.T) {
	config := defaultTorDriverConfig()
	err := applyTorServices(&config, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{
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
