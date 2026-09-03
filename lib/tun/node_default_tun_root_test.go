// nolint
package tunnodes_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/tuntest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestDefaultTunClassFeatureGate(t *testing.T) {
	unsupported := newRecordingDefaultTunSystem()
	unsupported.DisableDefaultTun = true
	engineUnsupported := newDefaultTunTestEngine(t, unsupported, nil)
	if _, ok := engineUnsupported.NodeClassSnapshot(E2EWorkspace, NodeTypeDefaultTun); ok {
		t.Fatal("DefaultTun class registered when sysnet feature is disabled")
	}

	supported := newRecordingDefaultTunSystem()
	engineSupported := newDefaultTunTestEngine(t, supported, nil)
	class, ok := engineSupported.NodeClassSnapshot(E2EWorkspace, NodeTypeDefaultTun)
	if !ok {
		t.Fatal("DefaultTun class missing when sysnet feature is enabled")
	}
	if !class.Unique {
		t.Fatal("DefaultTun class Unique = false, want true")
	}
	if len(class.InitialPorts) != 2 {
		t.Fatalf("DefaultTun initial ports = %#v, want right Tun and DNS ports", class.InitialPorts)
	}
	if class.InitialPorts[0].Direction != "right" || class.InitialPorts[0].Name != "Tun" || len(class.InitialPorts[0].Types) != 1 || class.InitialPorts[0].Types[0] != TypeTun {
		t.Fatalf("DefaultTun first initial port = %#v, want right almagest/tun port", class.InitialPorts[0])
	}
	if class.InitialPorts[1].Direction != "right" || class.InitialPorts[1].Name != "DNS" || len(class.InitialPorts[1].Types) != 1 || class.InitialPorts[1].Types[0] != dnsnodes.TypeDNS {
		t.Fatalf("DefaultTun second initial port = %#v, want right almagest/dns port", class.InitialPorts[1])
	}
}

func TestDefaultTunDNSPortAttachesDetachesAndReattachesProvider(t *testing.T) {
	source := newOutDNSTestProvider(t, "default-tun-dns.test A 10.134.0.1")
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	sys.OutDNSProvider = source
	if err := w.AddNodeClass(dnsnodes.OutDNSClass{Runtime: &engine{sys: sys}}); err != nil {
		t.Fatalf("AddNodeClass OutDns: %v", err)
	}

	defaultTun, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	if len(sys.tuns) != 1 {
		t.Fatalf("built DefaultTun count = %d, want 1", len(sys.tuns))
	}
	dtun := sys.tuns[0]
	if got := waitRecordingDefaultTunDNS(t, dtun, false); got != nil {
		t.Fatalf("DefaultTun DNS without link = %T, want nil", got)
	}
	if got := dtun.dnsSetCount(); got == 0 {
		t.Fatal("DefaultTun did not call SetDns(nil) after initial build")
	}

	out, err := w.AddNodeByClass(dnsnodes.NodeTypeOutDNS, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutDns: %v", err)
	}
	outSnapshot := testkit.MustNodeSnapshot(t, w, out)
	defaultSnapshot := testkit.MustNodeSnapshot(t, w, defaultTun)
	if len(outSnapshot.LeftPorts) != 1 {
		t.Fatalf("OutDns left ports = %#v, want one", outSnapshot.LeftPorts)
	}
	if len(defaultSnapshot.RightPorts) != 2 {
		t.Fatalf("DefaultTun right ports = %#v, want Tun and DNS", defaultSnapshot.RightPorts)
	}

	link, linkType, err := w.AddLink(outSnapshot.LeftPorts[0], defaultSnapshot.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink DNS: %v", err)
	}
	if linkType != dnsnodes.TypeDNS {
		t.Fatalf("DNS link type = %q, want %q", linkType, dnsnodes.TypeDNS)
	}
	first := waitRecordingDefaultTunDNS(t, dtun, true)
	if got := dnsnodes.DNSSource(first); got != source {
		t.Fatalf("DefaultTun DNS source = %T %p, want OutDns source %p", got, got, source)
	}
	assertDNSAnswer(t, first, "default-tun-dns.test.", dns.TypeA, "10.134.0.1")

	w.RemoveLink(link)
	if got := waitRecordingDefaultTunDNS(t, dtun, false); got != nil {
		t.Fatalf("DefaultTun DNS after link removal = %T, want nil", got)
	}
	if dnsnodes.DNSIsReady(first) {
		t.Fatal("DefaultTun kept first DNS wrapper ready after link removal")
	}

	if _, _, err := w.AddLink(outSnapshot.LeftPorts[0], defaultSnapshot.RightPorts[1]); err != nil {
		t.Fatalf("reattach DNS link: %v", err)
	}
	second := waitRecordingDefaultTunDNS(t, dtun, true)
	if dnsnodes.SameDNSInstance(first, second) {
		t.Fatal("DefaultTun reused detached DNS wrapper after reattach")
	}
	if got := dnsnodes.DNSSource(second); got != source {
		t.Fatalf("reattached DefaultTun DNS source = %T %p, want OutDns source %p", got, got, source)
	}
	assertDNSAnswer(t, second, "default-tun-dns.test.", dns.TypeA, "10.134.0.1")
}

func TestDefaultTunOldConfigWithoutDNSLinkRestoresDNSPortAndDropResolver(t *testing.T) {
	source := newOutDNSTestProvider(t, "old-default-tun.test A 10.134.0.5")
	sys := newRecordingDefaultTunSystem()
	sys.OutDNSProvider = source
	cfg := configer.NewMemory(map[string]any{
		"nodes": map[string]any{
			"default tun": map[string]any{
				"Class": NodeTypeDefaultTun,
			},
		},
	})
	engine := newDefaultTunTestEngine(t, sys, cfg)
	node := testkit.NodeIDByName(t, engine, E2EWorkspace, "default tun")
	snapshot, ok := engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if len(snapshot.RightPorts) != 2 {
		t.Fatalf("restored DefaultTun right ports = %#v, want Tun and DNS", snapshot.RightPorts)
	}
	if got := waitRecordingDefaultTunDNS(t, sys.tuns[0], false); got != nil {
		t.Fatalf("restored DefaultTun DNS without link = %T, want nil", got)
	}
}

func TestDefaultTunBuildsEmitsAndRebuildsFromFormAndTunConfig(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)
	testkit.AssertLeadingBlock(t, state, node, "effective")
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after OnReady = %d, want 1", got)
	}

	receiver := tuntest.NewReceiver(8)
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.In, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	firstPayload := tuntest.ReceivePayload(t, receiver.Events)
	firstTun := sys.tuns[0]
	if firstTun.closeCount() != 0 {
		t.Fatalf("first DefaultTun close count after link = %d, want 0", firstTun.closeCount())
	}
	w.EmitEvent(receiverID, link, TunConfig{DNS: "8.8.8.8"})
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after DNS-only TunConfig = %d, want 1", got)
	}
	tuntest.AssertNoEvent(t, receiver.Events)

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs":  "10.10.0.2/32\nfd00::2/128",
			"tunRoutes": "0.0.0.0/0\n::/0",
			"mtu":       1400,
			"dnsIP":     "10.10.0.2",
			"strict":    true,
			"exclude": []formular.ArrayElementValue{{
				Template: "rule",
				Values:   map[string]any{"type": "app", "rule": "org.example.skip"},
			}},
			"include": []formular.ArrayElementValue{},
		},
	})
	if got := sys.buildCount(); got != 2 {
		t.Fatalf("build count after form apply = %d, want 2", got)
	}
	secondPayload := tuntest.ReceivePayload(t, receiver.Events)
	if SameTunSource(firstPayload.Tun, secondPayload.Tun) {
		t.Fatal("form rebuild reused emitted Tun source, want a new DefaultTun")
	}
	if firstTun.closeCount() != 1 {
		t.Fatalf("old DefaultTun close count after rebuild = %d, want 1", firstTun.closeCount())
	}
	wantFormOpts := sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.10.0.2/32", "fd00::2/128"},
		TunRoutes: []string{"0.0.0.0/0", "::/0"},
		MTU:       1400,
		DnsIP:     "10.10.0.2",
		Strict:    true,
		Exclude:   []sysnet.Rule{{Type: "app", Rule: "org.example.skip"}},
		Include:   []sysnet.Rule{},
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantFormOpts) {
		t.Fatalf("form build opts = %#v, want %#v", got, wantFormOpts)
	}

	w.EmitEvent(receiverID, link, TunConfig{
		Addrs:  []string{"10.10.0.2/32", "10.20.0.2/32"},
		Routes: []string{"0.0.0.0/0", "10.20.0.0/16"},
		MTU:    1280,
	})
	if got := sys.buildCount(); got != 3 {
		t.Fatalf("build count after TunConfig = %d, want 3", got)
	}
	_ = tuntest.ReceivePayload(t, receiver.Events)
	wantMergedOpts := wantFormOpts
	wantMergedOpts.TunAddrs = []string{"10.10.0.2/32", "fd00::2/128", "10.20.0.2/32"}
	wantMergedOpts.TunRoutes = []string{"0.0.0.0/0", "::/0", "10.20.0.0/16"}
	wantMergedOpts.MTU = 1280
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantMergedOpts) {
		t.Fatalf("merged build opts = %#v, want %#v", got, wantMergedOpts)
	}
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "tunAddrs", "10.10.0.2/32\nfd00::2/128\n10.20.0.2/32")
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "tunRoutes", "0.0.0.0/0\n::/0\n10.20.0.0/16")
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "mtu", 1280)
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "dnsIP", "10.10.0.2")
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "strict", true)
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "exclude", "app org.example.skip")
	if firstTun.closeCount() != 1 || sys.tuns[1].closeCount() != 1 {
		t.Fatalf("rebuilt DefaultTun close counts = %d, %d; want 1, 1", firstTun.closeCount(), sys.tuns[1].closeCount())
	}

	w.RemoveNode(node)
	Eventually(t, func() bool {
		return firstTun.closeCount() > 0 && sys.tuns[1].closeCount() > 0 && sys.tuns[2].closeCount() > 0
	}, "DefaultTun instances to close")
	if firstTun.closeCount() == 0 || sys.tuns[1].closeCount() == 0 || sys.tuns[2].closeCount() == 0 {
		t.Fatalf("DefaultTun close counts after node removal = %d, %d, %d; want all closed", firstTun.closeCount(), sys.tuns[1].closeCount(), sys.tuns[2].closeCount())
	}
}

func TestDefaultTunDynamicConfigUsesSettersWhenOnlyMTUAndAddrsChange(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, true)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	receiver := tuntest.NewReceiver(4)
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.In, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = tuntest.ReceivePayload(t, receiver.Events)

	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.30.0.2/32"}, MTU: 1300})
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after dynamic TunConfig = %d, want 1", got)
	}
	if got, want := sys.setAddrsCalls, [][]string{{"10.30.0.2/32"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunAddrs calls = %#v, want %#v", got, want)
	}
	if got, want := sys.setMTUCalls, []int{1300}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunMTU calls = %#v, want %#v", got, want)
	}

	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.30.0.2/32"}, Routes: []string{"10.31.0.0/16"}, MTU: 1300})
	if got := sys.buildCount(); got != 2 {
		t.Fatalf("build count after suggested route change = %d, want 2", got)
	}
	if got, want := sys.lastOpts().TunRoutes, []string{"10.31.0.0/16"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TunRoutes after suggested route change = %#v, want %#v", got, want)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs":  "",
			"tunRoutes": "0.0.0.0/0",
			"mtu":       0,
			"dnsIP":     "",
			"strict":    false,
			"exclude":   []formular.ArrayElementValue{},
			"include":   []formular.ArrayElementValue{},
		},
	})
	if got := sys.buildCount(); got != 3 {
		t.Fatalf("build count after route change = %d, want 3", got)
	}
}

func TestDefaultTunMenuUsesArrayFieldsAndReportsValidationStatus(t *testing.T) {
	w, _ := newDefaultTunNodeTestWorkspace(t, false)
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)

	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	block := testkit.MustFindBlock(t, snapshot, "config")
	for _, id := range []string{"tunAddrs", "tunRoutes"} {
		item, ok := FindItem(block, id)
		if !ok {
			t.Fatalf("%s item missing", id)
		}
		if item.Field == nil || item.Field.Kind != formular.FieldArray {
			t.Fatalf("%s field kind = %#v, want array", id, item.Field)
		}
		if len(item.Field.Templates) != 1 || item.Field.Templates[0].Name != "value" {
			t.Fatalf("%s templates = %#v, want value template", id, item.Field.Templates)
		}
	}

	w.SendNodeFormularMsg(node, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(node)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       -1,
	})
	snapshot, ok = state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing after invalid validation")
	}
	item, ok := FindItem(testkit.MustFindBlock(t, snapshot, "config"), "mtu")
	if !ok {
		t.Fatal("mtu item missing after invalid validation")
	}
	if item.Status != formular.StatusError {
		t.Fatalf("mtu status = %q, want %q", item.Status, formular.StatusError)
	}

	w.SendNodeFormularMsg(node, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(node)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       1200,
	})
	snapshot, ok = state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing after valid validation")
	}
	item, ok = FindItem(testkit.MustFindBlock(t, snapshot, "config"), "mtu")
	if !ok {
		t.Fatal("mtu item missing after valid validation")
	}
	if item.Status != formular.StatusOK {
		t.Fatalf("mtu status = %q, want %q", item.Status, formular.StatusOK)
	}
}

func TestDefaultTunAppliesTunAddrsAndRoutesFromArrayFields(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs": []formular.ArrayElementValue{
				{Template: "value", Values: map[string]any{"value": "10.50.0.2/32"}},
				{Template: "value", Values: map[string]any{"value": "fd00::50/128"}},
			},
			"tunRoutes": []formular.ArrayElementValue{
				{Template: "value", Values: map[string]any{"value": "0.0.0.0/0"}},
				{Template: "value", Values: map[string]any{"value": "::/0"}},
			},
			"mtu":     1250,
			"dnsIP":   "10.50.0.2",
			"strict":  false,
			"exclude": []formular.ArrayElementValue{},
			"include": []formular.ArrayElementValue{},
		},
	})

	want := sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.50.0.2/32", "fd00::50/128"},
		TunRoutes: []string{"0.0.0.0/0", "::/0"},
		MTU:       1250,
		DnsIP:     "10.50.0.2",
		Exclude:   []sysnet.Rule{},
		Include:   []sysnet.Rule{},
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("array form build opts = %#v, want %#v", got, want)
	}
}

func TestDefaultTunRuleAutocompleteUsesSystemRules(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	sys.Rules = []sysnet.RuleTypeInfo{
		{Type: "app", Description: "Application"},
		{Type: "uid", Description: "User ID"},
	}
	sys.RuleCompletion = func(rule sysnet.Rule) []string {
		if rule.Type == "app" && rule.Rule == "org.mozilla.f" {
			return []string{"org.mozilla.firefox", "org.mozilla.fennec", "org.chromium.Chromium"}
		}
		return nil
	}

	var autocomplete []formular.AutocompleteHintsMessage
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind != pasta.NotificationNodeMenu {
			return
		}
		if msg, ok := notification.Formular.(formular.AutocompleteHintsMessage); ok {
			autocomplete = append(autocomplete, msg)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)

	w.SendNodeFormularMsg(node, formular.AutocompleteRequestMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageAutocompleteRequest, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config",
			FieldID: "type",
			ElementPath: []formular.ElementPathSegment{{
				ArrayFieldID: "include",
				ElementID:    "rule-0",
			}},
		},
		Prefix: "a",
	})
	if len(autocomplete) == 0 {
		t.Fatal("missing type autocomplete response")
	}
	if got, want := autocomplete[len(autocomplete)-1].Hints, []string{"app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("type hints = %#v, want %#v", got, want)
	}

	w.SendNodeFormularMsg(node, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config",
			FieldID: "type",
			ElementPath: []formular.ElementPathSegment{{
				ArrayFieldID: "include",
				ElementID:    "new-rule",
			}},
		},
		Value: "app",
	})
	w.SendNodeFormularMsg(node, formular.AutocompleteRequestMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageAutocompleteRequest, MenuID: pasta.NodeMenuID(node)},
		Field: formular.FieldRef{
			BlockID: "config",
			FieldID: "rule",
			ElementPath: []formular.ElementPathSegment{{
				ArrayFieldID: "include",
				ElementID:    "new-rule",
			}},
		},
		Prefix: "org.mozilla.f",
	})
	if len(autocomplete) < 2 {
		t.Fatal("missing rule autocomplete response")
	}
	want := []string{"org.mozilla.firefox", "org.mozilla.fennec"}
	if got := autocomplete[len(autocomplete)-1].Hints; !reflect.DeepEqual(got, want) {
		t.Fatalf("rule hints = %#v, want %#v", got, want)
	}
}

func TestDefaultTunLogsConfigureFailure(t *testing.T) {
	logf := NewDebugLogFactory(1024)
	w := pasta.NewWorkspace(logf)
	t.Cleanup(w.Close)
	sys := newRecordingDefaultTunSystem()
	sys.buildErr = errors.New("create TUN failed")
	engine := &engine{ctx: context.Background(), sys: sys}
	if err := w.AddNodeClass(tunnodes.DefaultTunClass{Runtime: engine}); err != nil {
		t.Fatalf("AddNodeClass DefaultTun: %v", err)
	}
	if _, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun"); err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	entries := logf.Drain()
	for _, entry := range entries {
		if entry.Level == DebugLogLevelError &&
			entry.Source.Name == "node" &&
			entry.Source.NodeClass == NodeTypeDefaultTun &&
			strings.Contains(entry.Message, "configure default TUN failed: create TUN failed") {
			return
		}
	}
	t.Fatalf("missing DefaultTun configure failure log in %#v", entries)
}

func TestDefaultTunWarningsNoWarningsNoPopupAfterRebuild(t *testing.T) {
	w, _ := newDefaultTunNodeTestWorkspace(t, false)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}

	forceDefaultTunRebuild(t, w, node, "10.60.0.0/16")

	if got := defaultTunWarningPopups(t, w, node); len(got) != 0 {
		t.Fatalf("DefaultTun warning popups = %#v, want none", got)
	}
}

func TestDefaultTunWarningsShowWarningPopup(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	sys.defaultTunWarningsHook = func(sysnet.DefaultTun) []sysnet.Warning {
		return []sysnet.Warning{sysnet.WarningDefaultTunDNSRouteNotExclusive}
	}
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}

	got := defaultTunWarningPopups(t, w, node)
	want := []string{string(sysnet.WarningDefaultTunDNSRouteNotExclusive)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultTun warning popups = %#v, want %#v", got, want)
	}
}

func TestDefaultTunWarningsDisappearAfterRebuild(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	sys.defaultTunWarningsHook = func(sysnet.DefaultTun) []sysnet.Warning {
		return []sysnet.Warning{sysnet.WarningDefaultTunDNSRouteNotExclusive}
	}
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}

	sys.defaultTunWarningsHook = nil
	forceDefaultTunRebuild(t, w, node, "10.61.0.0/16")

	if got := defaultTunWarningPopups(t, w, node); len(got) != 0 {
		t.Fatalf("DefaultTun warning popups after rebuild = %#v, want none", got)
	}
}

func TestDefaultTunWarningsReplaceTrackedAndPreserveUnrelatedPopups(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	sys.defaultTunWarningsHook = func(sysnet.DefaultTun) []sysnet.Warning {
		return []sysnet.Warning{sysnet.WarningDefaultTunDNSRouteNotExclusive}
	}
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	if _, err := w.AddNodePopup(node, pasta.NodePopupWard, "manual_warning", false); err != nil {
		t.Fatalf("AddNodePopup unrelated warning: %v", err)
	}
	const replacement = sysnet.Warning("replacement_default_tun_warning")
	sys.defaultTunWarningsHook = func(sysnet.DefaultTun) []sysnet.Warning {
		return []sysnet.Warning{replacement}
	}

	forceDefaultTunRebuild(t, w, node, "10.62.0.0/16")

	got := defaultTunWarningPopups(t, w, node)
	want := []string{"manual_warning", string(replacement)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultTun warning popups after replacement = %#v, want %#v", got, want)
	}
}

func TestDefaultTunWarningsIgnoreStaleDefaultTunAfterRebuild(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	firstTun := sys.tuns[0]
	sys.defaultTunWarningsHook = func(dtun sysnet.DefaultTun) []sysnet.Warning {
		if dtun == firstTun {
			return []sysnet.Warning{sysnet.WarningDefaultTunDNSRouteNotExclusive}
		}
		return nil
	}
	forceDefaultTunRebuild(t, w, node, "10.63.0.0/16")

	if got := defaultTunWarningPopups(t, w, node); len(got) != 0 {
		t.Fatalf("DefaultTun stale warning popups = %#v, want none", got)
	}
}

func TestDefaultTunWarningsRefreshAfterDynamicUpdate(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, true)
	var warnings []sysnet.Warning
	sys.defaultTunWarningsHook = func(sysnet.DefaultTun) []sysnet.Warning {
		return warnings
	}
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	receiver := tuntest.NewReceiver(4)
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "tun receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.In, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = tuntest.ReceivePayload(t, receiver.Events)

	warnings = []sysnet.Warning{sysnet.WarningDefaultTunDNSRouteNotExclusive}
	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.64.0.2/32"}, MTU: 1300})
	want := []string{string(sysnet.WarningDefaultTunDNSRouteNotExclusive)}
	if got := defaultTunWarningPopups(t, w, node); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultTun warning popups after dynamic update = %#v, want %#v", got, want)
	}
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after dynamic warning update = %d, want 1", got)
	}

	warnings = nil
	w.EmitEvent(receiverID, link, TunConfig{Addrs: []string{"10.64.0.2/32", "10.64.0.3/32"}, MTU: 1280})
	if got := defaultTunWarningPopups(t, w, node); len(got) != 0 {
		t.Fatalf("DefaultTun warning popups after dynamic warning clear = %#v, want none", got)
	}
	if got := sys.buildCount(); got != 1 {
		t.Fatalf("build count after dynamic warning clear = %d, want 1", got)
	}
}

func TestDefaultTunConfigPersistsAcrossSaveRestoreAndCopyPaste(t *testing.T) {
	sys := newRecordingDefaultTunSystem()
	engine := newDefaultTunTestEngine(t, sys, nil)
	node, err := engine.AddNode(E2EWorkspace, NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNode DefaultTun: %v", err)
	}
	applyDefaultTunTestConfig(t, engine, node)

	cfg := configer.NewMemory(nil)
	if err := engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restoreSys := newRecordingDefaultTunSystem()
	restored := newDefaultTunTestEngine(t, restoreSys, cfg)
	if got := restoreSys.lastOpts(); !reflect.DeepEqual(got, defaultTunPersistedOpts()) {
		t.Fatalf("restored build opts = %#v, want %#v", got, defaultTunPersistedOpts())
	}
	_ = restored

	clip := engine.Copy(E2EWorkspace, []uint64{node})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	engine.RemoveNode(E2EWorkspace, node)
	pasted := engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 1 {
		t.Fatalf("Paste nodes = %#v, want one node", pasted)
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, defaultTunPersistedOpts()) {
		t.Fatalf("pasted build opts = %#v, want %#v", got, defaultTunPersistedOpts())
	}
}

func TestDefaultTunDNSProviderCopyPastePreservesLinksE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "copy-default-tun.test A 10.134.0.2", nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	defaultTun := e.AddNode(NodeTypeDefaultTun, "default tun")
	e.WaitNodeLabel(defaultTun, "ready")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", defaultTun, "right", "DNS")
	e.waitDefaultTunSystemDNSResponse("copy-default-tun.test.", "10.134.0.2")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{out, defaultTun})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	e.Engine.RemoveNode(E2EWorkspace, defaultTun)
	e.Engine.RemoveNode(E2EWorkspace, out)

	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 2 {
		t.Fatalf("Paste returned %d nodes (%v), want 2", len(pasted), pasted)
	}
	pastedDefaultTun := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, NodeTypeDefaultTun)
	e.WaitNodeLabel(pastedDefaultTun, "ready")
	e.waitDefaultTunSystemDNSResponse("copy-default-tun.test.", "10.134.0.2")
}

func TestDefaultTunDNSProviderSaveRestorePreservesLinksE2E(t *testing.T) {
	const records = "restore-default-tun.test A 10.134.0.3"
	e := newOutDNSE2E(t, nil, records, nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	defaultTun := e.AddNode(NodeTypeDefaultTun, "default tun")
	e.WaitNodeLabel(defaultTun, "ready")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", defaultTun, "right", "DNS")
	e.waitDefaultTunSystemDNSResponse("restore-default-tun.test.", "10.134.0.3")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newOutDNSE2E(t, cfg, records, nil)
	restoredDefaultTun := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "default tun")
	restored.WaitNodeLabel(restoredDefaultTun, "ready")
	restored.waitDefaultTunSystemDNSResponse("restore-default-tun.test.", "10.134.0.3")
}

func TestDefaultTunDNSProviderLinkAttachDetachReattachE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "lifecycle-default-tun.test A 10.134.0.4", nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	defaultTun := e.AddNode(NodeTypeDefaultTun, "default tun")
	e.WaitNodeLabel(defaultTun, "ready")
	e.waitDefaultTunSystemDNSFailure("lifecycle-default-tun.test.")

	link := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", defaultTun, "right", "DNS")
	e.waitDefaultTunSystemDNSResponse("lifecycle-default-tun.test.", "10.134.0.4")

	e.Engine.RemoveLink(E2EWorkspace, link)
	e.waitDefaultTunSystemDNSFailure("lifecycle-default-tun.test.")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", defaultTun, "right", "DNS")
	e.waitDefaultTunSystemDNSResponse("lifecycle-default-tun.test.", "10.134.0.4")
}

func newDefaultTunNodeTestWorkspace(t *testing.T, dynamic bool) (*pasta.Workspace, *recordingDefaultTunSystem) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	sys := newRecordingDefaultTunSystem()
	sys.DisableDynDefaultTun = !dynamic
	engine := &engine{ctx: context.Background(), sys: sys}
	if err := w.AddNodeClass(tunnodes.DefaultTunClass{Runtime: engine}); err != nil {
		t.Fatalf("AddNodeClass DefaultTun: %v", err)
	}
	return w, sys
}

func newDefaultTunTestEngine(t *testing.T, sys *recordingDefaultTunSystem, cfg configer.Config) Engine {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	engine, err := BuildEngine(context.Background(), cfg, NewDebugLogFactory(1024), sys, nil, nil, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return engine
}

func applyDefaultTunTestConfig(t *testing.T, engine Engine, node uint64) {
	t.Helper()
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunAddrs":  "10.40.0.2/32",
			"tunRoutes": "0.0.0.0/0",
			"mtu":       1250,
			"dnsIP":     "10.40.0.2",
			"strict":    true,
			"exclude":   []formular.ArrayElementValue{},
			"include": []formular.ArrayElementValue{{
				Template: "rule",
				Values:   map[string]any{"type": "uid", "rule": "1000"},
			}},
		},
	})
}

func defaultTunPersistedOpts() sysnet.DefaultTunOpts {
	return sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.40.0.2/32"},
		TunRoutes: []string{"0.0.0.0/0"},
		MTU:       1250,
		DnsIP:     "10.40.0.2",
		Strict:    true,
		Exclude:   []sysnet.Rule{},
		Include:   []sysnet.Rule{{Type: "uid", Rule: "1000"}},
	}
}

func forceDefaultTunRebuild(t *testing.T, w *pasta.Workspace, node uint64, route string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"tunRoutes": route,
		},
	})
}

func defaultTunWarningPopups(t *testing.T, w *pasta.Workspace, node uint64) []string {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	out := make([]string, 0, len(snapshot.Popups))
	for _, popup := range snapshot.Popups {
		if popup.Type == pasta.NodePopupWard {
			out = append(out, popup.Text)
		}
	}
	return out
}

type recordingDefaultTunSystem struct {
	*sysnetdebug.System

	buildOpts              []sysnet.DefaultTunOpts
	tuns                   []*recordingDefaultTun
	setMTUCalls            []int
	setAddrsCalls          [][]string
	buildErr               error
	defaultTunWarningsHook func(sysnet.DefaultTun) []sysnet.Warning
}

func newRecordingDefaultTunSystem() *recordingDefaultTunSystem {
	return &recordingDefaultTunSystem{System: &sysnetdebug.System{}}
}

func (s *recordingDefaultTunSystem) BuildDefaultTun(opts sysnet.DefaultTunOpts) (sysnet.DefaultTun, error) {
	if err := s.VerifyDefaultTunOpts(opts); err != nil {
		return nil, err
	}
	if s.buildErr != nil {
		return nil, s.buildErr
	}
	dtun := newRecordingDefaultTun(opts.MTU)
	s.buildOpts = append(s.buildOpts, opts.Copy())
	s.tuns = append(s.tuns, dtun)
	return dtun, nil
}

func (s *recordingDefaultTunSystem) DefaultTunWarnings(t sysnet.DefaultTun) []sysnet.Warning {
	dtun, ok := t.(*recordingDefaultTun)
	if !ok || s.defaultTunWarningsHook == nil {
		return nil
	}
	found := false
	for _, built := range s.tuns {
		if built == dtun {
			found = true
			break
		}
	}
	if !found || dtun.closeCount() > 0 {
		return nil
	}
	return slicesClone(s.defaultTunWarningsHook(t))
}

func (s *recordingDefaultTunSystem) SetTunMTU(t tun.Tun, mtu int) error {
	dtun, ok := t.(*recordingDefaultTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.setMTUCalls = append(s.setMTUCalls, mtu)
	dtun.mtu = mtu
	return nil
}

func (s *recordingDefaultTunSystem) SetTunAddrs(t tun.Tun, addrs []string) error {
	if _, ok := t.(*recordingDefaultTun); !ok {
		return sysnet.ErrUnknownTun
	}
	s.setAddrsCalls = append(s.setAddrsCalls, slicesClone(addrs))
	return nil
}

func (s *recordingDefaultTunSystem) buildCount() int {
	return len(s.buildOpts)
}

func (s *recordingDefaultTunSystem) lastOpts() sysnet.DefaultTunOpts {
	if len(s.buildOpts) == 0 {
		return sysnet.DefaultTunOpts{}
	}
	return s.buildOpts[len(s.buildOpts)-1].Copy()
}

type recordingDefaultTun struct {
	tun.Tun
	peer tun.Tun

	mtu          int
	closeCounter atomic.Int32

	mu          sync.Mutex
	resolver    dns.Interface
	dnsSetCalls []dns.Interface
}

func newRecordingDefaultTun(mtu int) *recordingDefaultTun {
	if mtu <= 0 {
		mtu = 1500
	}
	base, peer := tun.Pipe(1, mtu, 0, 0)
	return &recordingDefaultTun{Tun: base, peer: peer, mtu: mtu}
}

func (t *recordingDefaultTun) SetDns(resolver dns.Interface) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.resolver = resolver
	t.dnsSetCalls = append(t.dnsSetCalls, resolver)
}

func (t *recordingDefaultTun) MTU() (int, error) {
	return t.mtu, nil
}

func (t *recordingDefaultTun) Close() error {
	t.closeCounter.Add(1)
	return errors.Join(t.Tun.Close(), t.peer.Close())
}

func (t *recordingDefaultTun) closeCount() int {
	return int(t.closeCounter.Load())
}

func (t *recordingDefaultTun) currentDNS() dns.Interface {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.resolver
}

func (t *recordingDefaultTun) dnsSetCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.dnsSetCalls)
}

func waitRecordingDefaultTunDNS(t *testing.T, dtun *recordingDefaultTun, wantReady bool) dns.Interface {
	t.Helper()

	var got dns.Interface
	Eventually(t, func() bool {
		got = dtun.currentDNS()
		if wantReady {
			return dnsnodes.DNSIsReady(got)
		}
		return got == nil
	}, "DefaultTun DNS provider ready=%v", wantReady)
	return got
}

func (e *dnsE2E) waitDefaultTunSystemDNSResponse(name, want string) {
	e.T.Helper()

	Eventually(e.T, func() bool {
		resp, err := e.queryDefaultTunSystemDNS(name)
		if err != nil || resp == nil || resp.RCode != dns.RCodeSuccess {
			return false
		}
		return strings.Contains(dnsnodes.FormatMessage(resp), want)
	}, "DefaultTun system DNS response for %s containing %q", name, want)
}

func (e *dnsE2E) waitDefaultTunSystemDNSFailure(name string) {
	e.T.Helper()

	Eventually(e.T, func() bool {
		resp, err := e.queryDefaultTunSystemDNS(name)
		return err != nil || resp == nil || resp.RCode != dns.RCodeSuccess
	}, "DefaultTun system DNS failure for %s", name)
}

func (e *dnsE2E) queryDefaultTunSystemDNS(name string) (*dns.Message, error) {
	e.T.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	return dns.Query(ctx, e.DebugSystem, dnsTestQuery(name, dns.TypeA))
}

func slicesClone[T any](in []T) []T {
	out := make([]T, len(in))
	copy(out, in)
	return out
}
