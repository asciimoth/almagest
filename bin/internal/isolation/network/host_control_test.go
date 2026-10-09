package network

import (
	"context"
	"errors"
	"math"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func registerDNS(t *testing.T, b *Broker) (protocol.ResourceRef, *fakeDNSEndpoint) {
	t.Helper()
	endpoint := newFakeDNSEndpoint()
	ref, err := b.RegisterDNSEndpoint(context.Background(), 1, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return ref, endpoint
}

func TestHostDNSControl(t *testing.T) {
	t.Run("attach_detach_and_receipts", func(t *testing.T) {
		b, backend := testBroker(t)
		device := create(t, b, "default", true)
		endpoint, _ := registerDNS(t, b)
		req := protocol.ConfigureDNSRequest{Meta: meta(b, "dns-on"), Tun: device.Resource, Enabled: true, Endpoint: endpoint}
		result, err := b.ConfigureDNS(context.Background(), req)
		if err != nil || result.Outcome != protocol.OutcomeCommitted || result.CurrentResource.Generation != 2 {
			t.Fatalf("attach = %+v, %v", result, err)
		}
		if backend.dnsCalls != 1 || !backend.dnsOn {
			t.Fatalf("native attach calls=%d on=%v", backend.dnsCalls, backend.dnsOn)
		}
		// A lost reply is recovered without repeating the host mutation.
		req.Meta.RequestID++
		again, err := b.ConfigureDNS(context.Background(), req)
		if err != nil || again.CurrentResource != result.CurrentResource || backend.dnsCalls != 1 {
			t.Fatalf("receipt = %+v, %v, calls=%d", again, err, backend.dnsCalls)
		}
		// A new equivalent operation is also a no-op.
		req.Meta = meta(b, "dns-on-equivalent")
		req.Tun = result.CurrentResource
		again, err = b.ConfigureDNS(context.Background(), req)
		if err != nil || again.CurrentResource != result.CurrentResource || backend.dnsCalls != 1 {
			t.Fatalf("equivalent = %+v, %v, calls=%d", again, err, backend.dnsCalls)
		}
		off := protocol.ConfigureDNSRequest{Meta: meta(b, "dns-off"), Tun: result.CurrentResource}
		result, err = b.ConfigureDNS(context.Background(), off)
		if err != nil || result.CurrentResource.Generation != 3 || backend.dnsOn || backend.dnsCalls != 2 {
			t.Fatalf("detach = %+v, %v, calls=%d on=%v", result, err, backend.dnsCalls, backend.dnsOn)
		}
	})

	t.Run("endpoint_close_detaches_first", func(t *testing.T) {
		b, backend := testBroker(t)
		device := create(t, b, "default", true)
		endpointRef, endpoint := registerDNS(t, b)
		attached, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "on"), Tun: device.Resource, Enabled: true, Endpoint: endpointRef})
		if err != nil {
			t.Fatal(err)
		}
		closed, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close-endpoint"), Resource: endpointRef})
		if err != nil || closed.Outcome != protocol.OutcomeCommitted || endpoint.closes.Load() != 1 || backend.dnsOn || backend.dnsCalls != 2 {
			t.Fatalf("close = %+v, %v, closes=%d calls=%d on=%v", closed, err, endpoint.closes.Load(), backend.dnsCalls, backend.dnsOn)
		}
		b.mu.Lock()
		owner := b.resources[attached.CurrentResource.ID]
		generation, dns := owner.ref.Generation, owner.dns
		b.mu.Unlock()
		if generation != 3 || dns != (protocol.ResourceRef{}) {
			t.Fatalf("owner generation=%d dns=%+v", generation, dns)
		}
	})

	t.Run("foreign_endpoint_is_not_detached_or_closed", func(t *testing.T) {
		b, backend := testBroker(t)
		device := create(t, b, "default", true)
		endpointRef, endpoint := registerDNS(t, b)
		attached, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "on"), Tun: device.Resource, Enabled: true, Endpoint: endpointRef})
		if err != nil {
			t.Fatal(err)
		}
		endpoint.foreign.Store(true)
		result, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close-endpoint"), Resource: endpointRef})
		if !errors.Is(err, protocol.ErrRecoveryRequired) || result.Outcome != protocol.OutcomeUncertain || endpoint.closes.Load() != 0 || backend.dnsCalls != 1 || !backend.dnsOn {
			t.Fatalf("foreign close = %+v, %v, closes=%d calls=%d on=%v", result, err, endpoint.closes.Load(), backend.dnsCalls, backend.dnsOn)
		}
		b.mu.Lock()
		stillAttached := b.resources[attached.CurrentResource.ID].dns
		b.mu.Unlock()
		if stillAttached != endpointRef {
			t.Fatal("foreign endpoint changed owner record")
		}
	})
}

func TestHostDNSValidationAndFailures(t *testing.T) {
	t.Run("references", func(t *testing.T) {
		b, _ := testBroker(t)
		regular := create(t, b, "regular", false)
		def := create(t, b, "default", true)
		endpoint, _ := registerDNS(t, b)
		cases := []struct {
			name string
			req  protocol.ConfigureDNSRequest
			want error
		}{
			{"regular tun", protocol.ConfigureDNSRequest{Tun: regular.Resource, Enabled: true, Endpoint: endpoint}, protocol.ErrResourceType},
			{"missing endpoint", protocol.ConfigureDNSRequest{Tun: def.Resource, Enabled: true}, protocol.ErrResourceType},
			{"endpoint on disable", protocol.ConfigureDNSRequest{Tun: def.Resource, Endpoint: endpoint}, protocol.ErrInvalidContract},
			{"wrong endpoint type", protocol.ConfigureDNSRequest{Tun: def.Resource, Enabled: true, Endpoint: def.Resource}, protocol.ErrResourceType},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				tc.req.Meta = meta(b, tc.name)
				_, err := b.ConfigureDNS(context.Background(), tc.req)
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
			})
		}
		foreign := endpoint
		foreign.SessionID[0] ^= 1
		_, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "foreign"), Tun: def.Resource, Enabled: true, Endpoint: foreign})
		if !errors.Is(err, protocol.ErrForeignResource) {
			t.Fatal(err)
		}
	})

	t.Run("policy", func(t *testing.T) {
		b, _ := testBroker(t)
		deny := false
		b.policy.NetworkRestrictions = &policy.NetworkRestrictions{AllowHostDNS: &deny, AllowStrictMode: &deny, DeniedRuleTypes: []string{"app"}}
		def := create(t, b, "default", true)
		endpoint, _ := registerDNS(t, b)
		_, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "dns"), Tun: def.Resource, Enabled: true, Endpoint: endpoint})
		if !errors.Is(err, protocol.ErrPolicyDenied) {
			t.Fatal(err)
		}
		for name, req := range map[string]protocol.CreateTunRequest{
			"strict": {Meta: meta(b, "strict"), Default: true, Strict: true},
			"rule":   {Meta: meta(b, "rule"), Default: true, RuleMode: "include", Rules: []protocol.Rule{{Type: "app", Value: "browser"}}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := b.CreateTun(context.Background(), req)
				if !errors.Is(err, protocol.ErrPolicyDenied) {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("native_results", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			err     error
			outcome protocol.Outcome
			want    error
		}{
			{"proved no effects", &NoEffectsError{Err: protocol.ErrCapabilityUnavailable}, protocol.OutcomeRejected, protocol.ErrCapabilityUnavailable},
			{"uncertain partial effect", errors.New("provider failed after apply"), protocol.OutcomeUncertain, protocol.ErrRecoveryRequired},
		} {
			t.Run(tc.name, func(t *testing.T) {
				b, backend := testBroker(t)
				def := create(t, b, "default", true)
				endpoint, _ := registerDNS(t, b)
				backend.dnsErr = tc.err
				result, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "dns"), Tun: def.Resource, Enabled: true, Endpoint: endpoint})
				if result.Outcome != tc.outcome || !errors.Is(err, tc.want) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			})
		}
	})

	t.Run("stale_and_invalid_native_state", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			mutate func(*Broker, *fakeBackend, *resource, *resource)
			want   error
		}{
			{"closed tun", func(_ *Broker, _ *fakeBackend, tun, _ *resource) { tun.state.Closed = true }, protocol.ErrCapabilityUnavailable},
			{"closed endpoint", func(_ *Broker, _ *fakeBackend, _, endpoint *resource) { endpoint.state.Closed = true }, protocol.ErrCapabilityUnavailable},
			{"wrong endpoint implementation", func(_ *Broker, _ *fakeBackend, _, endpoint *resource) { endpoint.native = newFakeTun() }, protocol.ErrResourceType},
			{"generation exhausted", func(_ *Broker, _ *fakeBackend, tun, _ *resource) { tun.ref.Generation = math.MaxUint64 }, protocol.ErrLimitExceeded},
			{"foreign tun identity", func(_ *Broker, backend *fakeBackend, _, _ *resource) { backend.last().foreign.Store(true) }, protocol.ErrRecoveryRequired},
			{"foreign endpoint identity", func(_ *Broker, _ *fakeBackend, _, endpoint *resource) {
				endpoint.native.(*fakeDNSEndpoint).foreign.Store(true)
			}, protocol.ErrRecoveryRequired},
		} {
			t.Run(tc.name, func(t *testing.T) {
				b, backend := testBroker(t)
				device := create(t, b, "default", true)
				endpointRef, _ := registerDNS(t, b)
				b.mu.Lock()
				tunResource, endpointResource := b.resources[device.Resource.ID], b.resources[endpointRef.ID]
				tc.mutate(b, backend, tunResource, endpointResource)
				device.Resource = tunResource.ref
				b.mu.Unlock()
				result, err := b.ConfigureDNS(context.Background(), protocol.ConfigureDNSRequest{Meta: meta(b, "dns"), Tun: device.Resource, Enabled: true, Endpoint: endpointRef})
				if !errors.Is(err, tc.want) {
					t.Fatalf("result=%+v err=%v want=%v", result, err, tc.want)
				}
			})
		}
	})

	t.Run("cancellation_after_native_apply", func(t *testing.T) {
		b, backend := testBroker(t)
		device := create(t, b, "default", true)
		endpoint, _ := registerDNS(t, b)
		ctx, cancel := context.WithCancel(context.Background())
		backend.dnsHook = func(context.Context, OwnedTun, OwnedDNSEndpoint, bool) error {
			cancel()
			return nil
		}
		result, err := b.ConfigureDNS(ctx, protocol.ConfigureDNSRequest{Meta: meta(b, "dns"), Tun: device.Resource, Enabled: true, Endpoint: endpoint})
		if result.Outcome != protocol.OutcomeUncertain || !errors.Is(err, protocol.ErrRecoveryRequired) || !errors.Is(err, context.Canceled) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestHostControlCapabilitiesApplyPolicy(t *testing.T) {
	b, backend := testBroker(t)
	available := protocol.Capability{State: protocol.CapabilitySupported, AttemptAllowed: true}
	backend.caps = &protocol.CapabilitySnapshot{
		SchemaVersion: 1,
		Operations: []protocol.OperationCapability{
			{Operation: protocol.OperationConfigureDNS, Capability: available},
		},
		DefaultTunProfiles: []protocol.DefaultTunProfile{
			{Mode: "exclude", Strict: true, Capability: available, Rules: []protocol.RuleBinding{{Type: "app", Capability: available}, {Type: "uid", Capability: available}}},
		},
	}
	deny := false
	b.policy.NetworkRestrictions = &policy.NetworkRestrictions{AllowHostDNS: &deny, AllowStrictMode: &deny, DeniedRuleTypes: []string{"app"}}
	snapshot, err := b.Capabilities(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Operations[0].State != protocol.CapabilityUnavailable || snapshot.Operations[0].AttemptAllowed {
		t.Fatalf("DNS capability = %+v", snapshot.Operations[0])
	}
	profile := snapshot.DefaultTunProfiles[0]
	if profile.Capability.State != protocol.CapabilityUnavailable || profile.Capability.AttemptAllowed {
		t.Fatalf("strict profile = %+v", profile.Capability)
	}
	if profile.Rules[0].Capability.State != protocol.CapabilityUnavailable || profile.Rules[0].Capability.AttemptAllowed ||
		profile.Rules[1].Capability.State != protocol.CapabilitySupported || !profile.Rules[1].Capability.AttemptAllowed {
		t.Fatalf("rule capabilities = %+v", profile.Rules)
	}
}

func TestDNSEndpointRegistrationBounds(t *testing.T) {
	b, _ := testBroker(t)
	if _, err := b.RegisterDNSEndpoint(context.Background(), 0, newFakeDNSEndpoint()); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal(err)
	}
	if _, err := b.RegisterDNSEndpoint(context.Background(), 2, newFakeDNSEndpoint()); !errors.Is(err, protocol.ErrStaleGraphEpoch) {
		t.Fatal(err)
	}
	foreign := newFakeDNSEndpoint()
	foreign.foreign.Store(true)
	if _, err := b.RegisterDNSEndpoint(context.Background(), 1, foreign); !errors.Is(err, protocol.ErrRecoveryRequired) {
		t.Fatal(err)
	}
	b.policy.Limits.MaxLiveResources = 1
	if _, err := b.RegisterDNSEndpoint(context.Background(), 1, newFakeDNSEndpoint()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RegisterDNSEndpoint(context.Background(), 1, newFakeDNSEndpoint()); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
}

func TestHostDNSWireControl(t *testing.T) {
	b, backend := testBroker(t)
	device := create(t, b, "default", true)
	endpoint, _ := registerDNS(t, b)
	controlClient, controlBroker := net.Pipe()
	dataClient, dataBroker := net.Pipe()
	diagnosticClient, diagnosticBroker := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- b.Serve(ctx, transport.Lanes{Control: controlBroker, Data: dataBroker, Diagnostic: diagnosticBroker})
	}()
	peer, err := transport.New(ctx, transport.Lanes{Control: controlClient, Data: dataClient, Diagnostic: diagnosticClient}, b.session, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.MutationResult
	req := protocol.ConfigureDNSRequest{Meta: meta(b, "wire-dns"), Tun: device.Resource, Enabled: true, Endpoint: endpoint}
	if err := peer.Call(ctx, protocol.OperationConfigureDNS, req, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCommitted || result.CurrentResource.Generation != 2 || !backend.dnsOn {
		t.Fatalf("wire result=%+v dnsOn=%v", result, backend.dnsOn)
	}
	_ = peer.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("peer loss did not stop broker")
	}
}

func FuzzHostControlValidation(f *testing.F) {
	f.Add(true, string(protocol.ResourceDefaultTun), string(protocol.ResourceDNS), uint64(1))
	f.Fuzz(func(t *testing.T, enabled bool, tunType, endpointType string, generation uint64) {
		b, _ := testBroker(t)
		var tunID, endpointID protocol.ResourceID
		tunID[0], endpointID[0] = 1, 2
		req := protocol.ConfigureDNSRequest{
			Tun:      protocol.ResourceRef{ID: tunID, Type: protocol.ResourceType(tunType), Generation: generation},
			Endpoint: protocol.ResourceRef{ID: endpointID, Type: protocol.ResourceType(endpointType), Generation: generation},
			Enabled:  enabled,
		}
		_ = b.validateDNSRequest(req)
	})
}
