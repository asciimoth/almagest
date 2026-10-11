package roleentry

import (
	"context"
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
