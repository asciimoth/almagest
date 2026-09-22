package dnsnodes_test

import (
	"strings"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

func TestDNSTimeoutMenusApplySaveAndRestoreE2E(t *testing.T) {
	e := newDNSE2E(t)
	type timeoutNode struct {
		name   string
		class  string
		block  string
		field  string
		custom int
	}
	nodes := []timeoutNode{
		{name: "client", class: dnsnodes.NodeTypeDNSClient, block: dnsnodes.DNSClientConfigBlockID, field: dnsnodes.DNSClientRequestTimeoutMSField, custom: 1101},
		{name: "server", class: dnsnodes.NodeTypeDNSServer, block: dnsnodes.DNSServerConfigBlockID, field: dnsnodes.DNSServerRequestTimeoutMSField, custom: 1102},
		{name: "fallback", class: dnsnodes.NodeTypeDnsFallback, block: dnsnodes.DNSFallbackConfigBlockID, field: dnsnodes.DNSFallbackAttemptTimeoutMSField, custom: 1103},
		{name: "tun2dns", class: dnsnodes.NodeTypeTun2Dns, block: dnsnodes.Tun2DNSConfigBlockID, field: dnsnodes.Tun2DNSRequestTimeoutMSField, custom: 1104},
		{name: "mesh", class: dnsnodes.NodeTypeMeshNames, block: dnsnodes.MeshNamesConfigBlockID, field: dnsnodes.MeshNamesRequestTimeoutMSField, custom: 1105},
	}

	for _, spec := range nodes {
		id := e.AddNode(spec.class, spec.name)
		e.SubscribeMenus(id)
		assertDNSTimeoutMenuValue(t, e, id, spec.block, spec.field, 0)

		e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldValidateMessage{
			MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, id),
			Field:       formular.FieldRef{BlockID: spec.block, FieldID: spec.field},
			Value:       -1,
		})
		Eventually(t, func() bool {
			item, ok := e.MenuItem(id, spec.block, spec.field)
			return ok && item.Status == formular.StatusError && strings.Contains(item.StatusText, "must not be negative")
		}, "%s negative timeout validation", spec.name)

		e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
			MessageBase: testkit.FormMessageBase(formular.MessageFormApply, id),
			BlockID:     spec.block,
			Values:      map[string]any{spec.field: spec.custom},
		})
		assertDNSTimeoutMenuValue(t, e, id, spec.block, spec.field, spec.custom)
	}

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for _, spec := range nodes {
		nodeCfg := cfg.View(configer.Path{"nodes", spec.name})
		if got := configer.Int(nodeCfg, configer.Path{spec.field}, -1); got != spec.custom {
			t.Fatalf("saved %s timeout = %d, want %d", spec.name, got, spec.custom)
		}
	}

	restored := newDNSE2EFromConfig(t, cfg)
	for _, spec := range nodes {
		id := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, spec.name)
		restored.SubscribeMenus(id)
		assertDNSTimeoutMenuValue(t, restored, id, spec.block, spec.field, spec.custom)
	}
}

func assertDNSTimeoutMenuValue(t *testing.T, e *dnsE2E, node uint64, block, field string, want int) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, block, field)
		if !ok || item.Field == nil || !item.Validation || !strings.Contains(item.Help, "Set 0 to use the gonnect default.") {
			return false
		}
		value, ok := formular.IntValue(item.Value)
		return ok && value == want && item.Status != formular.StatusError
	}, "DNS timeout field %s/%s = %d", block, field, want)
}
