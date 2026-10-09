package network

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func TestNetworkControl(t *testing.T) {
	b, f := testBroker(t)
	a, z := net.Pipe()
	c, d := net.Pipe()
	e, g := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Serve(ctx, transport.Lanes{Control: z, Data: d, Diagnostic: g}) }()
	peer, err := transport.New(ctx, transport.Lanes{Control: a, Data: c, Diagnostic: e}, b.session, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	req := protocol.CreateTunRequest{Meta: meta(b, "wire"), Options: protocol.TunOptions{MTU: 1400}}
	var result protocol.ResourceResult
	if err := peer.Call(ctx, protocol.OperationCreateTun, req, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCommitted || !result.Resource.ID.Valid() {
		t.Fatal(result)
	}
	select {
	case event := <-peer.Events():
		if event.Resource != result.Resource {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A failed mutation must keep its outcome in the reply body.
	f.last().updateErr = errors.New("partial native update")
	var update protocol.MutationResult
	err = peer.Call(ctx, protocol.OperationSetTunMTU, protocol.UpdateTunRequest{Meta: meta(b, "fail"), Resource: result.Resource, Kind: protocol.TunUpdateMTU, MTU: 1500}, &update)
	if err != nil || update.Outcome != protocol.OutcomeUncertain || update.Code != "recovery" {
		t.Fatalf("%+v %v", update, err)
	}
	var receipt protocol.ResourceResult
	err = peer.Call(ctx, protocol.OperationGetOperationResult, protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "fail"}, &receipt)
	if err != nil || receipt.Outcome != protocol.OutcomeUncertain {
		t.Fatalf("%+v %v", receipt, err)
	}
	_ = peer.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("peer death did not clean up")
	}
	if f.last().closes.Load() != 1 {
		t.Fatal("peer death leaked device")
	}
}

func TestNetworkBounds(t *testing.T) {
	t.Run("stalled_native", func(t *testing.T) {
		b, f := testBroker(t)
		b.timeout = 20 * time.Millisecond
		started, release := make(chan struct{}), make(chan struct{})
		device := newFakeTun()
		f.create = func(context.Context, protocol.CreateTunRequest) (OwnedTun, error) {
			close(started)
			<-release
			return device, nil
		}
		defer close(release)
		result, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "stalled")})
		if !errors.Is(err, protocol.ErrRecoveryRequired) || result.Outcome != protocol.OutcomeUncertain {
			t.Fatalf("%+v %v", result, err)
		}
		<-started
		status, err := b.Status(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
		if err != nil || !status.RecoveryRequired || status.LiveResources != 1 {
			t.Fatalf("%+v %v", status, err)
		}
		if err := b.Shutdown(context.Background()); !errors.Is(err, protocol.ErrRecoveryRequired) {
			t.Fatal(err)
		}
	})
	t.Run("events", func(t *testing.T) {
		b, f := testBroker(t)
		create(t, b, "one", false)
		device := f.last()
		b.mu.Lock()
		for _, r := range b.resources {
			for range protocol.MaxConcurrentRequests + 1 {
				b.eventLocked(r, false)
			}
		}
		if !b.closed || !b.recovery {
			t.Fatal("event overflow did not revoke session")
		}
		b.mu.Unlock()
		if err := b.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if device.closes.Load() != 1 {
			t.Fatal("overflow cleanup")
		}
	})
	t.Run("policy", func(t *testing.T) {
		b, _ := testBroker(t)
		b.policy.Limits.MaxLiveResources = 1
		create(t, b, "one", false)
		_, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "two")})
		if !errors.Is(err, protocol.ErrLimitExceeded) {
			t.Fatal(err)
		}
		_, err = b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "invalid"), RuleMode: "include"})
		if !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal(err)
		}
	})
}

func FuzzTunValidation(f *testing.F) {
	f.Add("test", uint32(1400), "", false)
	f.Fuzz(func(t *testing.T, name string, mtu uint32, mode string, def bool) {
		b, _ := testBroker(t)
		_ = b.validateOptions(protocol.CreateTunRequest{Options: protocol.TunOptions{Name: name, MTU: mtu}, RuleMode: mode, Default: def})
	})
}
