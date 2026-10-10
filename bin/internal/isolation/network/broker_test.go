package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

type fakeBackend struct {
	mu       sync.Mutex
	devices  []*fakeTun
	checkErr error
	create   func(context.Context, protocol.CreateTunRequest) (OwnedTun, error)
	dnsCalls int
	dnsErr   error
	dnsOn    bool
	dnsHook  func(context.Context, OwnedTun, OwnedDNSEndpoint, bool) error
	caps     *protocol.CapabilitySnapshot
}

func (f *fakeBackend) Capabilities(context.Context) (protocol.CapabilitySnapshot, error) {
	if f.caps != nil {
		return *f.caps, nil
	}
	return protocol.CapabilitySnapshot{SchemaVersion: 1, Revision: 1, Operations: []protocol.OperationCapability{{Operation: protocol.OperationCreateTun, Capability: protocol.Capability{State: protocol.CapabilityUnknown, AttemptAllowed: true}}}}, nil
}
func (f *fakeBackend) Check(context.Context, protocol.CreateTunRequest) (protocol.ValidationResult, error) {
	return protocol.ValidationResult{}, f.checkErr
}
func (f *fakeBackend) Create(ctx context.Context, req protocol.CreateTunRequest) (OwnedTun, error) {
	if f.create != nil {
		return f.create(ctx, req)
	}
	device := newFakeTun()
	device.state.Name = req.Options.Name
	device.state.MTU = req.Options.MTU
	f.mu.Lock()
	f.devices = append(f.devices, device)
	f.mu.Unlock()
	return device, nil
}
func (f *fakeBackend) ConfigureDNS(ctx context.Context, owned OwnedTun, endpoint OwnedDNSEndpoint, enabled bool) error {
	f.mu.Lock()
	f.dnsCalls++
	err, hook := f.dnsErr, f.dnsHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, owned, endpoint, enabled)
	}
	if err != nil {
		return err
	}
	if enabled && (endpoint == nil || endpoint.Resolver() == nil) {
		return protocol.ErrInvalidContract
	}
	f.mu.Lock()
	f.dnsOn = enabled
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.devices) }
func (f *fakeBackend) last() *fakeTun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.devices[len(f.devices)-1]
}

type fakeTun struct {
	mu                  sync.Mutex
	state               protocol.ResourceState
	events              chan tun.Event
	foreign             atomic.Bool
	closes, updates     atomic.Int32
	closeErr, updateErr error
}

type fakeDNSEndpoint struct {
	*fakeTun
	resolver gdns.Interface
}

func newFakeDNSEndpoint() *fakeDNSEndpoint {
	return &fakeDNSEndpoint{fakeTun: newFakeTun(), resolver: gdns.NewClient(nil, nil, nil)}
}

func (f *fakeDNSEndpoint) Resolver() gdns.Interface { return f.resolver }

func newFakeTun() *fakeTun {
	return &fakeTun{state: protocol.ResourceState{Name: "owned", MTU: 1420, Warnings: []string{"native_warning"}}, events: make(chan tun.Event, 8)}
}
func (f *fakeTun) Verify(context.Context) error {
	if f.foreign.Load() {
		return protocol.ErrRecoveryRequired
	}
	return nil
}
func (*fakeTun) RecoveryIdentity() string { return "fake-native-v1:42" }
func (f *fakeTun) State(context.Context) (protocol.ResourceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneState(f.state), nil
}
func (*fakeTun) Capabilities(context.Context) (protocol.CapabilitySnapshot, error) {
	var report protocol.CapabilitySnapshot
	for _, op := range []protocol.Operation{protocol.OperationSetTunMTU, protocol.OperationSetTunAddresses, protocol.OperationSetTunRoutes, protocol.OperationSetTunName} {
		report.Operations = append(report.Operations, protocol.OperationCapability{Operation: op, Capability: protocol.Capability{State: protocol.CapabilitySupported, AttemptAllowed: true}})
	}
	return report, nil
}
func (f *fakeTun) Events() <-chan tun.Event { return f.events }
func (f *fakeTun) Update(_ context.Context, req protocol.UpdateTunRequest) error {
	f.updates.Add(1)
	if f.updateErr != nil {
		return f.updateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Kind {
	case protocol.TunUpdateMTU:
		f.state.MTU = req.MTU
	case protocol.TunUpdateName:
		f.state.Name = req.Name
	case protocol.TunUpdateAddresses:
		f.state.Addresses = req.Addresses
	case protocol.TunUpdateRoutes:
		f.state.Routes = req.Routes
	}
	return nil
}
func (f *fakeTun) Close(context.Context) error {
	if f.closes.Add(1) == 1 {
		close(f.events)
	}
	return f.closeErr
}

func testBroker(t *testing.T) (*Broker, *fakeBackend) {
	t.Helper()
	p := policy.Default(policy.LaunchAuthority{Backend: "fake", CredentialClass: "test"}, policy.CommandAuthority{})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{}
	b, err := New(protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}, p, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	return b, f
}
func meta(b *Broker, id string) protocol.RequestMeta {
	return protocol.RequestMeta{RequestID: 1, SessionID: b.session.ID, SessionEpoch: b.session.Epoch, GraphEpoch: 1, OperationID: id}
}
func create(t *testing.T, b *Broker, id string, def bool) protocol.ResourceResult {
	t.Helper()
	result, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, id), Default: def, Options: protocol.TunOptions{MTU: 1420}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("create: %+v", result)
	}
	return result
}

func TestNetworkOwnership(t *testing.T) {
	t.Run("foreign_type_generation_restart", func(t *testing.T) {
		b, f := testBroker(t)
		owned := create(t, b, "create", false)
		for _, tc := range []struct {
			name   string
			change func(*protocol.ResourceRef)
			want   error
		}{
			{"foreign", func(r *protocol.ResourceRef) { r.SessionID[0] ^= 1 }, protocol.ErrForeignResource},
			{"type", func(r *protocol.ResourceRef) { r.Type = protocol.ResourceSocket }, protocol.ErrResourceType},
			{"tun_type", func(r *protocol.ResourceRef) { r.Type = protocol.ResourceDefaultTun }, protocol.ErrResourceType},
			{"generation", func(r *protocol.ResourceRef) { r.Generation++ }, protocol.ErrStaleGeneration},
			{"epoch", func(r *protocol.ResourceRef) { r.GraphEpoch++ }, protocol.ErrStaleGraphEpoch},
			{"unknown", func(r *protocol.ResourceRef) { r.ID[0] ^= 1 }, protocol.ErrForeignResource},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ref := owned.Resource
				tc.change(&ref)
				_, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, tc.name), Resource: ref})
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v want %v", err, tc.want)
				}
			})
		}
		if f.last().closes.Load() != 0 {
			t.Fatal("invalid reference closed device")
		}
		restarted, _ := testBroker(t)
		_, err := restarted.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(restarted, "old"), Resource: owned.Resource})
		if !errors.Is(err, protocol.ErrForeignResource) {
			t.Fatal(err)
		}
	})
	t.Run("identity_replacement", func(t *testing.T) {
		b, f := testBroker(t)
		owned := create(t, b, "create", false)
		f.last().foreign.Store(true)
		result, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close"), Resource: owned.Resource})
		if !errors.Is(err, protocol.ErrRecoveryRequired) || result.Outcome != protocol.OutcomeUncertain || f.last().closes.Load() != 0 {
			t.Fatalf("foreign cleanup: %+v %v", result, err)
		}
		status, _ := b.Status(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
		if status.LiveResources != 1 || !status.RecoveryRequired {
			t.Fatal(status)
		}
	})
	t.Run("updates_and_receipts", func(t *testing.T) {
		b, f := testBroker(t)
		owned := create(t, b, "create", false)
		req := protocol.UpdateTunRequest{Meta: meta(b, "update"), Resource: owned.Resource, Kind: protocol.TunUpdateMTU, MTU: 1500}
		result, err := b.UpdateTun(context.Background(), req)
		if err != nil || result.CurrentResource.Generation != 2 {
			t.Fatalf("%+v %v", result, err)
		}
		req.Meta.RequestID = 2
		_, err = b.UpdateTun(context.Background(), req)
		if err != nil || f.last().updates.Load() != 1 {
			t.Fatal("lost reply replay", err)
		}
		req.MTU = 1600
		_, err = b.UpdateTun(context.Background(), req)
		if !errors.Is(err, protocol.ErrOperationConflict) {
			t.Fatal(err)
		}
		req.Meta.OperationID = "stale"
		_, err = b.UpdateTun(context.Background(), req)
		if !errors.Is(err, protocol.ErrStaleGeneration) {
			t.Fatal(err)
		}
		receipt, err := b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "update"})
		if err != nil || receipt.State.MTU != 1500 {
			t.Fatalf("receipt %+v %v", receipt, err)
		}
		receipt.State.Warnings[0] = "changed"
		receipt, _ = b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "update"})
		if receipt.State.Warnings[0] != "native_warning" {
			t.Fatal("aliased receipt")
		}
		for i := range 2 {
			_, err = b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, fmt.Sprint("close", i)), Resource: result.CurrentResource})
			if err != nil {
				t.Fatal(err)
			}
		}
		if f.last().closes.Load() != 1 {
			t.Fatal("close not idempotent")
		}
	})
	t.Run("default_preflight_and_equivalence", func(t *testing.T) {
		b, f := testBroker(t)
		first := create(t, b, "first", true)
		second := create(t, b, "same", true)
		if first.Resource != second.Resource || f.count() != 1 {
			t.Fatal("equivalent default rebuilt")
		}
		f.checkErr = protocol.ErrCapabilityUnavailable
		_, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "bad"), Default: true, Options: protocol.TunOptions{MTU: 1500}})
		if !errors.Is(err, protocol.ErrCapabilityUnavailable) || f.last().closes.Load() != 0 {
			t.Fatal("failed preflight removed old default", err)
		}
		f.checkErr = nil
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				_, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, fmt.Sprint("replace", i)), Default: true, Options: protocol.TunOptions{MTU: uint32(1500 + i)}})
				if err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		status, _ := b.Status(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
		if status.LiveResources != 1 {
			t.Fatal(status)
		}
	})
	t.Run("partial_create", func(t *testing.T) {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprint(unknown), func(t *testing.T) {
				b, f := testBroker(t)
				device := newFakeTun()
				f.create = func(context.Context, protocol.CreateTunRequest) (OwnedTun, error) {
					if unknown {
						return nil, errors.New("unproved native effect")
					}
					return device, errors.New("setup failed")
				}
				result, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "partial")})
				if err == nil {
					t.Fatal("false success")
				}
				if unknown {
					if result.Outcome != protocol.OutcomeUncertain {
						t.Fatal(result)
					}
				} else if result.Outcome != protocol.OutcomeCompensated || device.closes.Load() != 1 {
					t.Fatal(result)
				}
			})
		}
	})
	t.Run("cancellation_late_create", func(t *testing.T) {
		b, f := testBroker(t)
		started, release := make(chan struct{}), make(chan struct{})
		device := newFakeTun()
		f.create = func(context.Context, protocol.CreateTunRequest) (OwnedTun, error) {
			close(started)
			<-release
			return device, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan protocol.ResourceResult, 1)
		go func() {
			result, _ := b.CreateTun(ctx, protocol.CreateTunRequest{Meta: meta(b, "late")})
			done <- result
		}()
		<-started
		cancel()
		result := <-done
		if result.Outcome != protocol.OutcomeUncertain {
			t.Fatal(result)
		}
		close(release)
		b.mu.Lock()
		receipt := b.receipts["late"]
		b.mu.Unlock()
		select {
		case <-receipt.done:
		case <-time.After(time.Second):
			t.Fatal("late cleanup blocked")
		}
		result, err := b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "late"})
		if result.Outcome != protocol.OutcomeCompensated || device.closes.Load() != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("%+v %v", result, err)
		}
	})
	t.Run("epoch_and_events", func(t *testing.T) {
		b, f := testBroker(t)
		owned := create(t, b, "first", false)
		first := <-b.Events()
		if first.Up {
			t.Fatal("presence treated as readiness")
		}
		f.last().events <- tun.EventUp
		select {
		case event := <-b.Events():
			if !event.Up || event.Revision <= first.Revision {
				t.Fatal(event)
			}
		case <-time.After(time.Second):
			t.Fatal("no up event")
		}
		_, err := b.ReleaseGraphEpoch(context.Background(), protocol.ReleaseGraphEpochRequest{Meta: meta(b, "release"), GraphEpoch: 1})
		if err != nil {
			t.Fatal(err)
		}
		request := protocol.ResourceRequest{Meta: meta(b, ""), Resource: owned.Resource}
		request.Meta.GraphEpoch = 2
		_, err = b.GetResourceState(context.Background(), request)
		if !errors.Is(err, protocol.ErrStaleGraphEpoch) {
			t.Fatal(err)
		}
		newReq := protocol.CreateTunRequest{Meta: meta(b, "new")}
		newReq.Meta.GraphEpoch = 2
		fresh, err := b.CreateTun(context.Background(), newReq)
		if err != nil || fresh.Resource.ID == owned.Resource.ID {
			t.Fatalf("%+v %v", fresh, err)
		}
	})
	t.Run("close_failure_and_limits", func(t *testing.T) {
		b, f := testBroker(t)
		owned := create(t, b, "first", false)
		f.last().closeErr = errors.New("cleanup failed")
		result, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close"), Resource: owned.Resource})
		if !errors.Is(err, protocol.ErrRecoveryRequired) || result.Outcome != protocol.OutcomeUncertain {
			t.Fatalf("%+v %v", result, err)
		}
		b.policy.Limits.MaxOperationReceipts = 2
		_, err = b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "over")})
		if !errors.Is(err, protocol.ErrLimitExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("dispatch_confusion", func(t *testing.T) {
		b, f := testBroker(t)
		body, _ := json.Marshal(protocol.CreateTunRequest{Meta: meta(b, "bad"), Default: true})
		_, err := b.Handle(context.Background(), protocol.OperationCreateTun, body)
		if !errors.Is(err, protocol.ErrInvalidContract) || f.count() != 0 {
			t.Fatal(err)
		}
		_, err = b.Handle(context.Background(), protocol.OperationOpenSocket, body)
		if !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal(err)
		}
	})
}

func TestNetworkBrokerJournalsNativeTunLifetime(t *testing.T) {
	p := policy.Default(policy.LaunchAuthority{Backend: "fake", CredentialClass: "test"}, policy.CommandAuthority{})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	journal, err := supervision.OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewWithJournal(protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}, p, &fakeBackend{}, journal)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "create-journaled"), Options: protocol.TunOptions{MTU: 1420}})
	if err != nil {
		t.Fatal(err)
	}
	records := journal.Snapshot()
	if len(records) != 1 || records[0].State != supervision.EffectOwned || records[0].Identity != "fake-native-v1:42" || records[0].ResourceType != protocol.ResourceTun {
		t.Fatalf("journal after create = %#v", records)
	}
	_, err = b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close-journaled"), Resource: result.Resource})
	if err != nil {
		t.Fatal(err)
	}
	if records = journal.Snapshot(); len(records) != 0 {
		t.Fatalf("journal after close = %#v", records)
	}
}

func TestNetworkBrokerLeavesDurableUncertainIdentityOnCleanupFailure(t *testing.T) {
	p := policy.Default(policy.LaunchAuthority{Backend: "fake", CredentialClass: "test"}, policy.CommandAuthority{})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	journal, err := supervision.OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	b, err := NewWithJournal(protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}, p, backend, journal)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "create-uncertain"), Options: protocol.TunOptions{MTU: 1420}})
	if err != nil {
		t.Fatal(err)
	}
	backend.last().closeErr = errors.New("native close failed")
	_, err = b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close-uncertain"), Resource: result.Resource})
	if !errors.Is(err, protocol.ErrRecoveryRequired) {
		t.Fatalf("close error = %v", err)
	}
	records := journal.Snapshot()
	if len(records) != 1 || records[0].State != supervision.EffectUncertain || records[0].Identity != "fake-native-v1:42" {
		t.Fatalf("journal after failed close = %#v", records)
	}
}

func TestNetworkBrokerDoesNotMutateWhenPendingJournalWriteFails(t *testing.T) {
	p := policy.Default(policy.LaunchAuthority{Backend: "fake", CredentialClass: "test"}, policy.CommandAuthority{})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	journal, err := supervision.OpenJournal(filepath.Join(directory, "recovery.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	b, err := NewWithJournal(protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}, p, backend, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	_, err = b.CreateTun(context.Background(), protocol.CreateTunRequest{Meta: meta(b, "journal-failure"), Options: protocol.TunOptions{MTU: 1420}})
	if err == nil {
		t.Fatal("create succeeded without a durable pending record")
	}
	if backend.count() != 0 {
		t.Fatalf("native create calls = %d", backend.count())
	}
}
