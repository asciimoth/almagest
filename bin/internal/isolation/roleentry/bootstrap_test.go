package roleentry

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func bootstrapFixture(t *testing.T) Bootstrap {
	t.Helper()
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return Bootstrap{Version: protocol.Version, Role: protocol.RoleConfigWriter, Target: "config.hujson",
		Session: protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: [32]byte{1}}}
}

func TestBootstrapReadinessExchange(t *testing.T) {
	parent, child := net.Pipe()
	defer parent.Close() //nolint:errcheck
	defer child.Close()  //nolint:errcheck
	value := bootstrapFixture(t)
	if err := value.Validate(value.Role); err != nil {
		t.Fatalf("fixture validation: %v", err)
	}
	for _, wire := range []any{value, Ready{Version: protocol.Version, Role: value.Role, PolicyDigest: value.Session.PolicyDigest, RestrictionsApplied: true}} {
		data, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		switch wire.(type) {
		case Bootstrap:
			if err := transport.Decode(data, new(Bootstrap)); err != nil {
				t.Fatalf("decode bootstrap %s: %v", data, err)
			}
		case Ready:
			if err := transport.Decode(data, new(Ready)); err != nil {
				t.Fatalf("decode ready %s: %v", data, err)
			}
		}
	}
	childResult := make(chan error, 1)
	go func() {
		got, err := ReceiveBootstrap(context.Background(), child, protocol.RoleConfigWriter)
		if err == nil && got != value {
			err = errors.New("bootstrap changed across exchange")
		}
		if err == nil {
			err = AcknowledgeBootstrap(context.Background(), child, got)
		}
		childResult <- err
	}()
	ready, err := SendBootstrap(context.Background(), parent, value)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-childResult; err != nil {
		t.Fatal(err)
	}
	if ready.Role != value.Role || ready.PolicyDigest != value.Session.PolicyDigest || !ready.RestrictionsApplied {
		t.Fatalf("ready = %#v", ready)
	}
}

func TestBootstrapRejectsAuthorityExpansion(t *testing.T) {
	valid := bootstrapFixture(t)
	tests := []Bootstrap{
		{},
		func() Bootstrap { b := valid; b.Version++; return b }(),
		func() Bootstrap { b := valid; b.Role = protocol.RoleCommandHelper; return b }(),
		func() Bootstrap { b := valid; b.Target = "../config"; return b }(),
		func() Bootstrap { b := valid; b.Target = "/config"; return b }(),
		func() Bootstrap { b := valid; b.Target = "config:stream"; return b }(),
	}
	for _, input := range tests {
		if err := input.Validate(protocol.RoleConfigWriter); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Errorf("Validate(%#v) = %v", input, err)
		}
	}
}

func TestBootstrapRejectsMalformedFrameAndFalseReadiness(t *testing.T) {
	parent, child := net.Pipe()
	defer parent.Close() //nolint:errcheck
	defer child.Close()  //nolint:errcheck
	value := bootstrapFixture(t)
	go func() {
		var got Bootstrap
		_ = transport.ReadFrame(child, &got)
		_ = transport.WriteFrame(child, Ready{Version: protocol.Version, Role: got.Role, PolicyDigest: got.Session.PolicyDigest})
	}()
	if _, err := SendBootstrap(context.Background(), parent, value); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("false readiness error = %v", err)
	}
}

func TestBootstrapSilenceHonorsCancellation(t *testing.T) {
	parent, child := net.Pipe()
	defer parent.Close() //nolint:errcheck
	defer child.Close()  //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ReceiveBootstrap(ctx, child, protocol.RoleConfigWriter)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("silent bootstrap = %v after %s", err, time.Since(started))
	}
}

func FuzzBootstrapValidation(f *testing.F) {
	f.Add("config.hujson")
	f.Add("../config")
	f.Add("config:stream")
	f.Fuzz(func(t *testing.T, target string) {
		value := Bootstrap{Version: protocol.Version, Role: protocol.RoleConfigWriter, Target: target,
			Session: protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version, PolicyDigest: [32]byte{1}}}
		err := value.Validate(protocol.RoleConfigWriter)
		if err == nil && !validTarget(target) {
			t.Fatal("validation accepted an unsafe target")
		}
	})
}
