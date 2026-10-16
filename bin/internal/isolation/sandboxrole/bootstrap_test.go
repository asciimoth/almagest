package sandboxrole

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func TestPolicyBootstrapRoundTripUsesChunks(t *testing.T) {
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleSandboxSupervisor,
		Session: protocol.Session{ID: id, Epoch: 1, Version: protocol.Version}}
	policy := bytes.Repeat([]byte("x"), 3*maxChunkBytes+17)
	left, right := net.Pipe()
	defer left.Close()  //nolint:errcheck
	defer right.Close() //nolint:errcheck
	written := make(chan error, 1)
	go func() { written <- SendPolicy(context.Background(), left, bootstrap, policy) }()
	gotBootstrap, gotPolicy, err := ReceivePolicy(context.Background(), right)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if gotBootstrap != bootstrap || !bytes.Equal(gotPolicy, policy) {
		t.Fatal("sandbox policy bootstrap changed its input")
	}
}

func TestPolicyBootstrapRejectsBounds(t *testing.T) {
	if err := SendPolicy(context.Background(), nil, roleentry.Bootstrap{}, []byte("x")); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("nil stream: %v", err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleSandboxSupervisor,
		Session: protocol.Session{ID: id, Epoch: 1, Version: protocol.Version}}
	left, right := net.Pipe()
	defer left.Close()  //nolint:errcheck
	defer right.Close() //nolint:errcheck
	if err := SendPolicy(context.Background(), left, bootstrap, nil); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("empty policy: %v", err)
	}
}

func TestPolicyBootstrapRejectsWrongDigest(t *testing.T) {
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleSandboxSupervisor,
		Session: protocol.Session{ID: id, Epoch: 1, Version: protocol.Version}}
	policy := []byte("policy")
	digest := sha256.Sum256(policy)
	digest[0]++
	left, right := net.Pipe()
	defer left.Close()  //nolint:errcheck
	defer right.Close() //nolint:errcheck
	written := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(time.Second)
		if err := transport.WriteFrameDeadline(left, bootstrap, deadline); err != nil {
			written <- err
			return
		}
		if err := transport.WriteFrameDeadline(left, policyHeader{Size: uint32(len(policy)), Digest: digest}, deadline); err != nil {
			written <- err
			return
		}
		written <- transport.WriteFrameDeadline(left, policyChunk{Sequence: 1, Data: policy}, deadline)
	}()
	if _, _, err := ReceivePolicy(context.Background(), right); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("ReceivePolicy error = %v", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}
