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
		Options:        RuntimeOptions{LogLevel: "debug", LogWorkers: true, NoPool: true, NoDNSCacheExpiration: true, DebugNodes: true}}
	received := make(chan error, 1)
	go func() {
		gotBootstrap, gotInput, err := ReceiveRuntimeBootstrap(ctx, right)
		if err == nil && (gotBootstrap.Role != protocol.RoleRuntime || gotInput.Options.LogLevel != "debug" || len(gotInput.AdminEndpoints) != 2) {
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
	valid := RuntimeInput{Version: protocol.Version, AdminEndpoints: []RuntimeAdminEndpoint{{ID: 1, Kind: "pipe"}}}
	for name, change := range map[string]func(*RuntimeInput){
		"version":   func(i *RuntimeInput) { i.Version++ },
		"zero-id":   func(i *RuntimeInput) { i.AdminEndpoints[0].ID = 0 },
		"kind":      func(i *RuntimeInput) { i.AdminEndpoints[0].Kind = "native" },
		"duplicate": func(i *RuntimeInput) { i.AdminEndpoints = append(i.AdminEndpoints, i.AdminEndpoints[0]) },
		"log":       func(i *RuntimeInput) { i.Options.LogLevel = string(make([]byte, 17)) },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.AdminEndpoints = append([]RuntimeAdminEndpoint(nil), valid.AdminEndpoints...)
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
