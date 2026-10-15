package roleentry

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func runtimeBootstrap(t *testing.T) Bootstrap {
	t.Helper()
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return Bootstrap{Version: protocol.Version, Role: protocol.RoleRuntime, Session: protocol.Session{ID: id, Epoch: 1, Version: protocol.Version}}
}

func TestRuntimeBootstrapRoundTrip(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()  //nolint:errcheck
	defer right.Close() //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bootstrap := runtimeBootstrap(t)
	input := RuntimeInput{Version: protocol.Version, ConfigWritable: true, StartupSnapshot: true,
		AdminEndpoints: []RuntimeAdminEndpoint{{ID: 1, Kind: "web"}, {ID: 2, Kind: "unix"}},
		NodeCommands:   []RuntimeNodeCommand{{Name: "notify", Slots: []string{"message"}}},
		Options:        RuntimeOptions{LogLevel: "debug", LogWorkers: true, NoPool: true, NoDNSCacheExpiration: true, DebugNodes: true}}
	received := make(chan error, 1)
	go func() {
		gotBootstrap, gotInput, err := ReceiveRuntimeBootstrap(ctx, right)
		if err == nil && (gotBootstrap.Role != protocol.RoleRuntime || gotInput.Options.LogLevel != "debug" || len(gotInput.AdminEndpoints) != 2 ||
			len(gotInput.NodeCommands) != 1 || len(gotInput.NodeCommands[0].Slots) != 1) {
			err = protocol.ErrInvalidContract
		}
		if err == nil {
			err = AcknowledgeBootstrap(ctx, right, gotBootstrap)
		}
		received <- err
	}()
	ready, err := SendRuntimeBootstrap(ctx, left, bootstrap, input)
	if err != nil || !ready.RestrictionsApplied {
		t.Fatalf("ready=%+v %v", ready, err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeInputRejectsInvalidAuthority(t *testing.T) {
	valid := RuntimeInput{Version: protocol.Version, AdminEndpoints: []RuntimeAdminEndpoint{{ID: 1, Kind: "pipe"}},
		NodeCommands: []RuntimeNodeCommand{{Name: "notify", Slots: []string{"message"}}}}
	for name, change := range map[string]func(*RuntimeInput){
		"version":            func(i *RuntimeInput) { i.Version++ },
		"zero-id":            func(i *RuntimeInput) { i.AdminEndpoints[0].ID = 0 },
		"kind":               func(i *RuntimeInput) { i.AdminEndpoints[0].Kind = "native" },
		"duplicate endpoint": func(i *RuntimeInput) { i.AdminEndpoints = append(i.AdminEndpoints, i.AdminEndpoints[0]) },
		"empty command":      func(i *RuntimeInput) { i.NodeCommands[0].Name = "" },
		"duplicate command":  func(i *RuntimeInput) { i.NodeCommands = append(i.NodeCommands, i.NodeCommands[0]) },
		"empty slot":         func(i *RuntimeInput) { i.NodeCommands[0].Slots[0] = "" },
		"duplicate slot":     func(i *RuntimeInput) { i.NodeCommands[0].Slots = append(i.NodeCommands[0].Slots, "message") },
		"log":                func(i *RuntimeInput) { i.Options.LogLevel = string(make([]byte, 17)) },
		"e2e timer":          func(i *RuntimeInput) { i.Options.E2ETailscaleRejectAfterSeconds = 86401 },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.AdminEndpoints = append([]RuntimeAdminEndpoint(nil), valid.AdminEndpoints...)
			input.NodeCommands = []RuntimeNodeCommand{{Name: valid.NodeCommands[0].Name, Slots: append([]string(nil), valid.NodeCommands[0].Slots...)}}
			change(&input)
			if input.Validate() == nil {
				t.Fatalf("accepted %+v", input)
			}
		})
	}
}

func TestProductionRuntimeInputRequiresCompleteBoundedStatus(t *testing.T) {
	validInput := func() RuntimeInput {
		return RuntimeInput{Version: protocol.Version, Production: true, Isolation: RuntimeIsolationStatus{
			ProtocolVersion: protocol.Version,
			Restrictions:    map[string][]string{"network": {"trusted"}, "runtime": {"landlock", "seccomp"}},
			Roles:           map[string]string{"network": "ready", "runtime": "ready"},
			Capabilities:    map[string]bool{"commands": true},
			RecoveryState:   "complete",
		}}
	}
	valid := validInput()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid production input = %v", err)
	}
	for name, change := range map[string]func(*RuntimeInput){
		"missing protocol":  func(i *RuntimeInput) { i.Isolation.ProtocolVersion = 0 },
		"future protocol":   func(i *RuntimeInput) { i.Isolation.ProtocolVersion++ },
		"incomplete":        func(i *RuntimeInput) { i.Isolation.RecoveryState = "recovering" },
		"missing roles":     func(i *RuntimeInput) { i.Isolation.Roles = nil },
		"missing limits":    func(i *RuntimeInput) { i.Isolation.Restrictions = nil },
		"network not ready": func(i *RuntimeInput) { i.Isolation.Roles["network"] = "starting" },
		"runtime not ready": func(i *RuntimeInput) { i.Isolation.Roles["runtime"] = "starting" },
		"no network limits": func(i *RuntimeInput) { i.Isolation.Restrictions["network"] = nil },
		"empty role":        func(i *RuntimeInput) { i.Isolation.Roles = map[string]string{"": "ready"} },
		"empty role state":  func(i *RuntimeInput) { i.Isolation.Roles = map[string]string{"runtime": ""} },
		"empty restriction": func(i *RuntimeInput) { i.Isolation.Restrictions = map[string][]string{"runtime": {""}} },
		"empty capability":  func(i *RuntimeInput) { i.Isolation.Capabilities = map[string]bool{"": true} },
		"long recovery":     func(i *RuntimeInput) { i.Isolation.RecoveryState = string(make([]byte, 65)) },
	} {
		t.Run(name, func(t *testing.T) {
			input := validInput()
			change(&input)
			if err := input.Validate(); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}

	partial := RuntimeInput{Version: protocol.Version, Isolation: RuntimeIsolationStatus{RecoveryState: "complete"}}
	if err := partial.Validate(); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("partial optional status = %v", err)
	}
}

func adaptiveRuntimeInput() RuntimeInput {
	return RuntimeInput{Version: protocol.Version, Isolation: RuntimeIsolationStatus{
		ProtocolVersion: protocol.Version, StatusSchemaVersion: 1, RecoveryState: "complete",
		Restrictions: map[string][]string{"network": {"trusted"}, "runtime": {"seccomp"}},
		Roles:        map[string]string{"network": "ready", "runtime": "ready"}, Capabilities: map[string]bool{"native_network": true},
		RequestedPolicy: "auto", ProfileID: "linux-user-v1", ProfileRevision: 1, VerificationState: "verified", Reduced: true,
		Mechanisms: map[string]map[string]RuntimeIsolationMechanism{
			"network": {"capability-sets": {State: "applied"}},
			"runtime": {"role-cgroups": {State: "unavailable", Reason: "not-delegated"}, "seccomp": {State: "applied"}},
		},
		Credentials: map[string]RuntimeIsolationCredentials{
			"network": {RealUID: 1000, EffectiveUID: 1000, SavedUID: 1000, FilesystemUID: 1000, RealGID: 1000, EffectiveGID: 1000, SavedGID: 1000, FilesystemGID: 1000, SupplementaryGroups: []uint32{1000}},
			"runtime": {RealUID: 1000, EffectiveUID: 1000, SavedUID: 1000, FilesystemUID: 1000, RealGID: 1000, EffectiveGID: 1000, SavedGID: 1000, FilesystemGID: 1000},
		},
		CapabilitySets: map[string]RuntimeIsolationCapabilitySets{
			"network": {Inheritable: []string{"CAP_NET_ADMIN"}, Permitted: []string{"CAP_NET_ADMIN"}, Effective: []string{"CAP_NET_ADMIN"}, Bounding: []string{"CAP_NET_ADMIN"}, Ambient: []string{"CAP_NET_ADMIN"}},
			"runtime": {Bounding: []string{"CAP_NET_ADMIN"}},
		},
		ResourceLimits: map[string][]RuntimeIsolationResourceLimit{
			"network": {{Name: "descriptors", Requested: "2048", Effective: "2048", Unit: "count", Scope: "process", Source: "configured", Enforcement: "applied"}},
			"runtime": {{Name: "descriptors", Requested: "2048", Effective: "2048", Unit: "count", Scope: "process", Source: "configured", Enforcement: "applied"}},
		},
		Warnings:        []RuntimeIsolationWarning{{Role: "runtime", Code: "not-delegated", Effect: "No per-role memory, CPU, or task ceilings.", Feature: "A memory ceiling is unavailable."}},
		ObservationTime: "2026-10-15T12:00:00Z", SelectedPlanDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
}

func TestRuntimeInputValidatesAdaptiveIsolationStatus(t *testing.T) {
	if err := adaptiveRuntimeInput().Validate(); err != nil {
		t.Fatalf("valid adaptive input = %v", err)
	}
	for name, change := range map[string]func(*RuntimeInput){
		"partial":      func(i *RuntimeInput) { i.Isolation.ProfileID = "" },
		"schema":       func(i *RuntimeInput) { i.Isolation.StatusSchemaVersion = 2 },
		"policy":       func(i *RuntimeInput) { i.Isolation.RequestedPolicy = "best-effort" },
		"profile":      func(i *RuntimeInput) { i.Isolation.ProfileID = "custom" },
		"verification": func(i *RuntimeInput) { i.Isolation.VerificationState = "ready" },
		"role":         func(i *RuntimeInput) { i.Isolation.Mechanisms["daemon"] = i.Isolation.Mechanisms["runtime"] },
		"mechanism": func(i *RuntimeInput) {
			i.Isolation.Mechanisms["runtime"] = map[string]RuntimeIsolationMechanism{"namespaces": {State: "applied"}}
		},
		"mechanism state": func(i *RuntimeInput) {
			i.Isolation.Mechanisms["runtime"]["seccomp"] = RuntimeIsolationMechanism{State: "enabled"}
		},
		"reason": func(i *RuntimeInput) {
			i.Isolation.Mechanisms["runtime"]["seccomp"] = RuntimeIsolationMechanism{State: "unavailable", Reason: "anything"}
		},
		"groups": func(i *RuntimeInput) {
			value := i.Isolation.Credentials["runtime"]
			value.SupplementaryGroups = []uint32{1, 1}
			i.Isolation.Credentials["runtime"] = value
		},
		"capability": func(i *RuntimeInput) {
			value := i.Isolation.CapabilitySets["runtime"]
			value.Effective = []string{"CAP_MAGIC"}
			i.Isolation.CapabilitySets["runtime"] = value
		},
		"duplicate capability": func(i *RuntimeInput) {
			value := i.Isolation.CapabilitySets["runtime"]
			value.Bounding = []string{"CAP_NET_ADMIN", "CAP_NET_ADMIN"}
			i.Isolation.CapabilitySets["runtime"] = value
		},
		"missing credentials": func(i *RuntimeInput) { delete(i.Isolation.Credentials, "network") },
		"missing resources":   func(i *RuntimeInput) { i.Isolation.ResourceLimits = nil },
		"missing warnings":    func(i *RuntimeInput) { i.Isolation.Warnings = nil },
		"resource":            func(i *RuntimeInput) { i.Isolation.ResourceLimits["runtime"][0].Scope = "host" },
		"warning":             func(i *RuntimeInput) { i.Isolation.Warnings[0].Code = "free-form" },
		"warning role":        func(i *RuntimeInput) { i.Isolation.Warnings[0].Role = "daemon" },
		"warning feature":     func(i *RuntimeInput) { i.Isolation.Warnings[0].Feature = "" },
		"time":                func(i *RuntimeInput) { i.Isolation.ObservationTime = "today" },
		"digest":              func(i *RuntimeInput) { i.Isolation.SelectedPlanDigest = "ABC" },
	} {
		t.Run(name, func(t *testing.T) {
			input := adaptiveRuntimeInput()
			change(&input)
			if err := input.Validate(); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func FuzzRuntimeInput(f *testing.F) {
	f.Add(uint32(protocol.Version), uint16(1), "web", "info")
	f.Add(uint32(0), uint16(0), "native", string([]byte{0xff}))
	f.Fuzz(func(t *testing.T, version uint32, id uint16, kind, level string) {
		input := RuntimeInput{Version: version, AdminEndpoints: []RuntimeAdminEndpoint{{ID: id, Kind: kind}}, Options: RuntimeOptions{LogLevel: level}}
		err := input.Validate()
		valid := version == protocol.Version && id != 0 && (kind == "web" || kind == "unix" || kind == "pipe") && len(level) <= 16 && utf8.ValidString(level)
		if valid != (err == nil) {
			t.Fatalf("Validate(%+v)=%v", input, err)
		}
	})
}

func FuzzRuntimeIsolationStatus(f *testing.F) {
	f.Add("runtime", "ready", "landlock", "commands", "complete", true)
	f.Add("", "", string([]byte{0xff}), "", "recovering", false)
	f.Fuzz(func(t *testing.T, role, state, restriction, capability, recovery string, enabled bool) {
		input := RuntimeInput{Version: protocol.Version, Isolation: RuntimeIsolationStatus{
			ProtocolVersion: protocol.Version,
			Restrictions:    map[string][]string{role: {restriction}},
			Roles:           map[string]string{role: state},
			Capabilities:    map[string]bool{capability: enabled},
			RecoveryState:   recovery,
		}}
		err := input.Validate()
		valid := boundedStatusString(role) && boundedStatusString(state) && boundedStatusString(restriction) && boundedStatusString(capability) && boundedStatusString(recovery)
		if valid != (err == nil) {
			t.Fatalf("Validate(%+v) = %v", input.Isolation, err)
		}
	})
}
