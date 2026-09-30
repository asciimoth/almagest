package tunnodes

import (
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/formular"
	gonnectdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestSysnetDebugCapabilityStatesGateTunClasses(t *testing.T) {
	tests := []struct {
		name string
		cap  sysnet.Capability
		want bool
	}{
		{name: "available", cap: sysnet.Capability{State: sysnet.CapabilityAvailable}, want: true},
		{name: "unknown", cap: sysnet.Capability{State: sysnet.CapabilityUnknown}, want: true},
		{name: "unavailable", cap: sysnet.Capability{State: sysnet.CapabilityUnavailable, Reasons: []sysnet.CapabilityReason{sysnet.ReasonMissingDependency}}},
		{name: "unsupported", cap: sysnet.Capability{State: sysnet.CapabilityUnsupported, Reasons: []sysnet.CapabilityReason{sysnet.ReasonNotImplemented}}},
	}
	for _, target := range []struct {
		name      string
		target    sysnet.Target
		className string
	}{
		{name: "regular", target: sysnet.TargetTun, className: NodeTypeTun},
		{name: "default", target: sysnet.TargetDefaultTun, className: NodeTypeDefaultTun},
	} {
		for _, test := range tests {
			t.Run(target.name+"/"+test.name, func(t *testing.T) {
				system := &sysnetdebug.System{}
				report := system.Capabilities()
				setReportOperations(&report, target.target, sysnet.OpCreate, test.cap)
				if err := system.SetCapabilityReport(report); err != nil {
					t.Fatalf("SetCapabilityReport: %v", err)
				}
				classes := Classes(&tunSplitterTestRuntime{sys: system})
				if got := hasNodeClass(classes, target.className); got != test.want {
					t.Fatalf("class %q present = %v, want %v", target.className, got, test.want)
				}
			})
		}
	}
}

func TestSysnetDebugMenusReflectUnavailableCapabilities(t *testing.T) {
	system := &sysnetdebug.System{}
	report := system.Capabilities()
	unavailable := sysnet.Capability{
		State:   sysnet.CapabilityUnavailable,
		Reasons: []sysnet.CapabilityReason{sysnet.ReasonMissingDependency},
	}
	setReportOperation(&report, sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreateNamed, Family: sysnet.FamilyNone,
	}, unavailable)
	setReportOperation(&report, sysnet.OperationKey{
		Target: sysnet.TargetDefaultTun, Operation: sysnet.OpCreateNamed, Family: sysnet.FamilyIPv4,
	}, unavailable)
	setReportProfile(&report, sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingInclude,
	}, unavailable)
	setReportProfile(&report, sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingFull, Strict: true,
	}, unavailable)
	if err := system.SetCapabilityReport(report); err != nil {
		t.Fatalf("SetCapabilityReport: %v", err)
	}
	runtime := &tunSplitterTestRuntime{sys: system}

	regular := &tunNode{NodeBase: NodeBase{Runtime: runtime}, config: defaultTunNodeConfig()}
	regularName := mustBlockItem(t, regular.configBlock(), "name")
	assertUnavailableMenuItem(t, regularName, "missing dependency")

	defaultNode := &defaultTunNode{NodeBase: NodeBase{Runtime: runtime}, config: defaultDefaultTunConfig()}
	block := defaultNode.configBlock()
	assertUnavailableMenuItem(t, mustBlockItem(t, block, "name"), "missing dependency")
	assertUnavailableMenuItem(t, mustBlockItem(t, block, "include"), "missing dependency")
	assertUnavailableMenuItem(t, mustBlockItem(t, block, "strict"), "missing dependency")
	if exclude := mustBlockItem(t, block, "exclude"); exclude.Readonly {
		t.Fatalf("exclude field is readonly with an available exclude profile: %#v", exclude)
	}
}

func TestSysnetDebugUnknownNamedCapabilityKeepsExplicitAttemptEditable(t *testing.T) {
	system := &sysnetdebug.System{}
	report := system.Capabilities()
	setReportOperation(&report, sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreateNamed, Family: sysnet.FamilyNone,
	}, sysnet.Capability{State: sysnet.CapabilityUnknown, Reasons: []sysnet.CapabilityReason{sysnet.ReasonProbeNotRun}})
	if err := system.SetCapabilityReport(report); err != nil {
		t.Fatalf("SetCapabilityReport: %v", err)
	}
	node := &tunNode{
		NodeBase: NodeBase{Runtime: &tunSplitterTestRuntime{sys: system}},
		config:   defaultTunNodeConfig(),
	}
	item := mustBlockItem(t, node.configBlock(), "name")
	if item.Readonly {
		t.Fatalf("unknown name capability made explicit attempt readonly: %#v", item)
	}
	if !strings.Contains(item.Help, "availability is not established") ||
		!strings.Contains(item.Help, "probe not run") {
		t.Fatalf("unknown capability help = %q", item.Help)
	}
}

func TestValidationErrorForAttemptPreservesBlockingErrorIdentities(t *testing.T) {
	invalidCause := errors.New("invalid native option")
	primaryCause := errors.New("dependency operation failed")
	rollbackCause := errors.New("dependency rollback failed")
	report := sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{
		{
			Path:   "Tun.Create",
			State:  sysnet.CapabilityUnknown,
			Reason: sysnet.ReasonProbeNotRun,
		},
		{
			Path:  "Tun.Name",
			State: sysnet.CapabilityUnknown,
			Err:   errors.Join(sysnet.ErrInvalidOptions, invalidCause),
		},
		{
			Path:   "Tun.Create",
			State:  sysnet.CapabilityUnavailable,
			Reason: sysnet.ReasonMissingDependency,
			Err:    errors.Join(primaryCause, rollbackCause),
		},
	}}

	err := validationErrorForAttempt(report)
	for _, target := range []error{
		sysnet.ErrInvalidOptions,
		invalidCause,
		sysnet.ErrUnavailable,
		primaryCause,
		rollbackCause,
	} {
		if !errors.Is(err, target) {
			t.Fatalf("validation error %v does not preserve %v", err, target)
		}
	}
	if errors.Is(err, sysnet.ErrCapabilityUnknown) {
		t.Fatalf("advisory unknown capability blocked the attempt: %v", err)
	}
}

func TestValidationErrorForAttemptAllowsOnlyAdvisoryUnknownIssues(t *testing.T) {
	report := sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{
		{
			Path:   "DefaultTun.Profile",
			State:  sysnet.CapabilityUnknown,
			Reason: sysnet.ReasonProbeNotRun,
		},
		{
			Path:   "DefaultTun.Rule",
			State:  sysnet.CapabilityUnknown,
			Reason: sysnet.ReasonProbeFailed,
		},
	}}
	if err := validationErrorForAttempt(report); err != nil {
		t.Fatalf("advisory unknown capability error = %v, want nil", err)
	}
}

func TestSysnetDebugRuleMenusUseExactRoutingContext(t *testing.T) {
	system := &sysnetdebug.System{Rules: []sysnet.RuleCapability{
		{Type: "include-only", Description: "Include rule", ValueKind: sysnet.RuleValueName, SemanticsID: "test.include.v1"},
		{Type: "exclude-only", Description: "Exclude rule", ValueKind: sysnet.RuleValueName, SemanticsID: "test.exclude.v1"},
	}}
	report := system.Capabilities()
	unsupported := sysnet.Capability{State: sysnet.CapabilityUnsupported, Reasons: []sysnet.CapabilityReason{sysnet.ReasonNotImplemented}}
	setRuleBinding(&report, sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingInclude,
	}, "exclude-only", unsupported)
	setRuleBinding(&report, sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingExclude,
	}, "include-only", unsupported)
	if err := system.SetCapabilityReport(report); err != nil {
		t.Fatalf("SetCapabilityReport: %v", err)
	}
	node := &defaultTunNode{
		NodeBase: NodeBase{Runtime: &tunSplitterTestRuntime{sys: system}},
		config:   defaultDefaultTunConfig(),
	}
	if got, want := ruleTypes(node.routingRuleCapabilities("include")), []string{"include-only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("include rule types = %#v, want %#v", got, want)
	}
	if got, want := ruleTypes(node.routingRuleCapabilities("exclude")), []string{"exclude-only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exclude rule types = %#v, want %#v", got, want)
	}

	var gotContext sysnet.RuleContext
	system.CompleteRuleHook = func(_ sysnet.Rule, context sysnet.RuleContext) ([]string, error) {
		gotContext = context.Clone()
		return []string{"include-only-value"}, nil
	}
	node.setRuleDraftType("include", "new-rule", "include-only")
	hints := node.configAutocompleteHints(formular.AutocompleteRequestMessage{
		Field: formular.FieldRef{
			BlockID: "config", FieldID: "rule",
			ElementPath: []formular.ElementPathSegment{{ArrayFieldID: "include", ElementID: "new-rule"}},
		},
		Prefix: "include",
	})
	if !reflect.DeepEqual(hints, []string{"include-only-value"}) {
		t.Fatalf("completion hints = %#v", hints)
	}
	wantContext := sysnet.RoutingProfileKey{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingInclude}
	if gotContext.Routing == nil || *gotContext.Routing != wantContext || gotContext.Matcher != nil {
		t.Fatalf("completion context = %#v, want routing %#v", gotContext, wantContext)
	}
}

func TestSysnetDebugUnknownPreflightBuildsTun(t *testing.T) {
	system := &sysnetdebug.System{}
	report := system.Capabilities()
	setReportOperation(&report, sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyNone,
	}, sysnet.Capability{State: sysnet.CapabilityUnknown, Reasons: []sysnet.CapabilityReason{sysnet.ReasonProbeNotRun}})
	if err := system.SetCapabilityReport(report); err != nil {
		t.Fatalf("SetCapabilityReport: %v", err)
	}
	w := newCapabilityDebugWorkspace(t, system, TunClass{Runtime: &tunSplitterTestRuntime{sys: system}})
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	if got := mustNodeLabel(t, w, node); got != "ready" {
		t.Fatalf("Tun label = %q, want ready", got)
	}
	if _, ok := system.GetTunPeer("tun"); !ok {
		t.Fatal("unknown preflight did not attempt BuildTun")
	}
}

func TestSysnetDebugUnavailablePreflightPreservesLiveTun(t *testing.T) {
	system := &sysnetdebug.System{}
	w := newCapabilityDebugWorkspace(t, system, TunClass{Runtime: &tunSplitterTestRuntime{sys: system}})
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	entry, ok := system.GetTunPeer("tun")
	if !ok {
		t.Fatal("initial TUN entry missing")
	}
	report := system.Capabilities()
	setReportOperation(&report, sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyNone,
	}, sysnet.Capability{State: sysnet.CapabilityUnavailable, Reasons: []sysnet.CapabilityReason{sysnet.ReasonResourceBusy}})
	if err := system.SetCapabilityReport(report); err != nil {
		t.Fatalf("SetCapabilityReport: %v", err)
	}
	applyTunCapabilityForm(w, node, map[string]any{"mtu": 1300})
	if got := mustNodeLabel(t, w, node); got != "invalid config" {
		t.Fatalf("Tun label = %q, want invalid config", got)
	}
	if _, err := system.CapabilitiesForTun(entry.Tun); err != nil {
		t.Fatalf("live TUN was replaced after unavailable validation: %v", err)
	}
}

func TestSysnetDebugPerTunCapabilityForcesRebuild(t *testing.T) {
	var builds atomic.Int32
	system := &sysnetdebug.System{
		TunBuilder: func(opts sysnet.TunOpts) (tun.Tun, error) {
			builds.Add(1)
			base, peer := tun.Pipe(1, max(opts.MTU, 1500), 0, 0)
			t.Cleanup(func() { _ = peer.Close() })
			return base, nil
		},
	}
	w := newCapabilityDebugWorkspace(t, system, TunClass{Runtime: &tunSplitterTestRuntime{sys: system}})
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	entry, ok := system.GetTunPeer("tun")
	if !ok {
		t.Fatal("initial TUN entry missing")
	}
	if err := system.SetTunCapabilityReport(entry.Tun, sysnet.TunCapabilityReport{
		Operations: []sysnet.OperationCapability{{
			Key:        sysnet.OperationKey{Target: sysnet.TargetTun, Operation: sysnet.OpSetMTU, Family: sysnet.FamilyNone},
			Capability: sysnet.Capability{State: sysnet.CapabilityUnavailable, Reasons: []sysnet.CapabilityReason{sysnet.ReasonPermissionDenied}},
		}},
	}); err != nil {
		t.Fatalf("SetTunCapabilityReport: %v", err)
	}
	applyTunCapabilityForm(w, node, map[string]any{"mtu": 1300})
	if got := builds.Load(); got != 2 {
		t.Fatalf("TUN build count = %d, want 2 after unavailable live setter", got)
	}
	if _, err := system.CapabilitiesForTun(entry.Tun); !errors.Is(err, sysnet.ErrUnknownTun) {
		t.Fatalf("old TUN capability query error = %v, want ErrUnknownTun", err)
	}
}

func TestSysnetDebugDefaultTunNameAndSetDNSFailureReachNode(t *testing.T) {
	dnsErr := errors.New("injected resolver update failure")
	system := &sysnetdebug.System{SetDNSHook: func(gonnectdns.Interface) error { return dnsErr }}
	w := newCapabilityDebugWorkspace(t, system, DefaultTunClass{Runtime: &tunSplitterTestRuntime{sys: system}})
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	if got := mustNodeLabel(t, w, node); got != "error" {
		t.Fatalf("DefaultTun label = %q, want error after SetDNS failure", got)
	}
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values:      map[string]any{"name": "named-default"},
	})
	entry, ok := system.GetDefaultTunPeer()
	if !ok {
		t.Fatal("DefaultTun entry missing")
	}
	if entry.Name != "named-default" || entry.Config.Name != "named-default" {
		t.Fatalf("DefaultTun name = %q, config name = %q", entry.Name, entry.Config.Name)
	}
	if got := mustNodeLabel(t, w, node); got != "error" {
		t.Fatalf("DefaultTun label after rename = %q, want DNS error to remain visible", got)
	}
}

func setReportOperations(report *sysnet.CapabilityReport, target sysnet.Target, operation sysnet.Operation, capability sysnet.Capability) {
	for i := range report.Operations {
		if report.Operations[i].Key.Target == target && report.Operations[i].Key.Operation == operation {
			report.Operations[i].Capability = capability.Clone()
		}
	}
}

func setReportOperation(report *sysnet.CapabilityReport, key sysnet.OperationKey, capability sysnet.Capability) {
	for i := range report.Operations {
		if report.Operations[i].Key == key {
			report.Operations[i].Capability = capability.Clone()
			return
		}
	}
}

func setReportProfile(report *sysnet.CapabilityReport, key sysnet.RoutingProfileKey, capability sysnet.Capability) {
	for i := range report.DefaultTunProfiles {
		if report.DefaultTunProfiles[i].Key == key {
			report.DefaultTunProfiles[i].Capability = capability.Clone()
			return
		}
	}
}

func setRuleBinding(report *sysnet.CapabilityReport, key sysnet.RoutingProfileKey, ruleType string, capability sysnet.Capability) {
	for i := range report.DefaultTunProfiles {
		if report.DefaultTunProfiles[i].Key != key {
			continue
		}
		for j := range report.DefaultTunProfiles[i].Rules {
			if report.DefaultTunProfiles[i].Rules[j].Type == ruleType {
				report.DefaultTunProfiles[i].Rules[j].Capability = capability.Clone()
				return
			}
		}
	}
}

func hasNodeClass(classes []pasta.NodeClass, className string) bool {
	for _, class := range classes {
		if class.ClassName() == className {
			return true
		}
	}
	return false
}

func mustBlockItem(t *testing.T, block formular.Block, id string) formular.Item {
	t.Helper()
	for _, item := range block.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("item %q missing from block %#v", id, block)
	return formular.Item{}
}

func assertUnavailableMenuItem(t *testing.T, item formular.Item, reason string) {
	t.Helper()
	if item.Field == nil || !item.Readonly {
		t.Fatalf("item %q is editable: %#v", item.ID, item)
	}
	if !strings.Contains(item.Help, reason) {
		t.Fatalf("item %q help = %q, want reason %q", item.ID, item.Help, reason)
	}
}

func ruleTypes(rules []sysnet.RuleCapability) []string {
	result := make([]string, len(rules))
	for i, rule := range rules {
		result[i] = rule.Type
	}
	return result
}

func newCapabilityDebugWorkspace(t *testing.T, system *sysnetdebug.System, class pasta.NodeClass) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
	t.Cleanup(func() { _ = system.Close() })
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(class); err != nil {
		t.Fatalf("AddNodeClass: %v", err)
	}
	return w
}

func mustNodeLabel(t *testing.T, w *pasta.Workspace, node uint64) string {
	t.Helper()
	snapshot, ok := w.NodeSnapshot(node)
	if !ok {
		t.Fatal("NodeSnapshot missing")
	}
	return snapshot.Label
}

func applyTunCapabilityForm(w *pasta.Workspace, node uint64, values map[string]any) {
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values:      values,
	})
}
