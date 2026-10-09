package network

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
)

type dnsDefaultTun struct {
	resolver gdns.Interface
	err      error
}

func (*dnsDefaultTun) Read([][]byte, []int, int) (int, error) { return 0, nil }
func (*dnsDefaultTun) Write([][]byte, int) (int, error)       { return 0, nil }
func (*dnsDefaultTun) Close() error                           { return nil }
func (*dnsDefaultTun) MTU() (int, error)                      { return 1400, nil }
func (*dnsDefaultTun) Name() (string, error)                  { return "dns-test", nil }
func (*dnsDefaultTun) Events() <-chan tun.Event               { return nil }
func (*dnsDefaultTun) File() *os.File                         { return nil }
func (*dnsDefaultTun) BatchSize() int                         { return 1 }
func (*dnsDefaultTun) MWO() int                               { return 0 }
func (*dnsDefaultTun) MRO() int                               { return 0 }
func (*dnsDefaultTun) IsNative() bool                         { return false }
func (d *dnsDefaultTun) SetDNS(resolver gdns.Interface) error {
	d.resolver = resolver
	return d.err
}

type validIdentity struct{}

func (validIdentity) Verify(context.Context) error { return nil }

type validationSystem struct {
	sysnet.System
	report  sysnet.ValidationReport
	options sysnet.DefaultTunOpts
}

func (s *validationSystem) CheckTunOpts(sysnet.TunOpts) sysnet.ValidationReport { return s.report }
func (s *validationSystem) CheckDefaultTunOpts(opts sysnet.DefaultTunOpts) sysnet.ValidationReport {
	s.options = opts
	return s.report
}

func TestSystemBackendContract(t *testing.T) {
	system := &validationSystem{}
	backend := &SystemBackend{system: system}
	for _, tc := range []struct {
		name        string
		state       sysnet.CapabilityState
		cause, want error
	}{
		{"unknown", sysnet.CapabilityUnknown, nil, nil},
		{"unknown_probe", sysnet.CapabilityUnknown, errors.New("probe failed"), nil},
		{"invalid_unknown", sysnet.CapabilityUnknown, errors.Join(sysnet.ErrCapabilityUnknown, sysnet.ErrInvalidOptions), protocol.ErrInvalidContract},
		{"unavailable", sysnet.CapabilityUnavailable, nil, protocol.ErrCapabilityUnavailable},
		{"unsupported", sysnet.CapabilityUnsupported, nil, protocol.ErrCapabilityUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			system.report = sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{{State: tc.state, Err: tc.cause}}}
			_, err := backend.Check(context.Background(), protocol.CreateTunRequest{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	system.report = sysnet.ValidationReport{}
	source := netip.MustParsePrefix("192.0.2.1/32")
	destination := netip.MustParsePrefix("198.51.100.0/24")
	for _, mode := range []string{"include", "exclude"} {
		req := protocol.CreateTunRequest{Default: true, Strict: true, RuleMode: mode, DNSIP: source.Addr(), Rules: []protocol.Rule{{Type: "uid", Value: "1000"}}, Options: protocol.TunOptions{MTU: 1400, Addresses: []netip.Prefix{source}, SourceRoutes: []protocol.SourceRoute{{Source: source, Route: destination}}}}
		if _, err := backend.Check(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		got := system.options
		if !got.Strict || got.DnsIP != source.Addr().String() || len(got.SourceRoutes) != 1 || got.SourceRoutes[0].Source != source.Addr() || got.SourceRoutes[0].Destination != destination {
			t.Fatal(got)
		}
		if mode == "include" && (len(got.Include) != 1 || len(got.Exclude) != 0) {
			t.Fatal(got)
		}
		if mode == "exclude" && (len(got.Exclude) != 1 || len(got.Include) != 0) {
			t.Fatal(got)
		}
	}
	for _, state := range []sysnet.CapabilityState{sysnet.CapabilityUnknown, sysnet.CapabilityAvailable, sysnet.CapabilityUnsupported, sysnet.CapabilityUnavailable} {
		got := capability(sysnet.Capability{State: state, Reasons: []sysnet.CapabilityReason{sysnet.ReasonProbeFailed}, Limitations: []sysnet.LimitationID{"native_limit"}})
		if err := got.Validate(); err != nil {
			t.Fatal(err)
		}
		if got.AttemptAllowed != (state == sysnet.CapabilityUnknown || state == sysnet.CapabilityAvailable) || len(got.Reasons) != 1 || len(got.Limitations) != 1 {
			t.Fatal(got)
		}
	}
}

func TestSystemBackendManagedDNS(t *testing.T) {
	system := &validationSystem{}
	backend := &SystemBackend{system: system}
	device := &dnsDefaultTun{}
	owned := &systemTun{backend: backend, device: device, identity: validIdentity{}, defaultTun: true}
	endpoint := newFakeDNSEndpoint()
	if err := backend.ConfigureDNS(context.Background(), owned, endpoint, true); err != nil || device.resolver != endpoint.Resolver() {
		t.Fatalf("attach resolver=%p want=%p err=%v", device.resolver, endpoint.Resolver(), err)
	}
	if err := backend.ConfigureDNS(context.Background(), owned, nil, false); err != nil || device.resolver != nil {
		t.Fatalf("detach resolver=%p err=%v", device.resolver, err)
	}
	device.err = sysnet.ErrUnavailable
	if err := backend.ConfigureDNS(context.Background(), owned, endpoint, true); !errors.Is(err, protocol.ErrCapabilityUnavailable) {
		t.Fatalf("provider error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	device.err = nil
	if err := backend.ConfigureDNS(canceled, owned, endpoint, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
	if err := backend.ConfigureDNS(context.Background(), newFakeTun(), endpoint, true); !errors.Is(err, protocol.ErrResourceType) {
		t.Fatalf("foreign tun = %v", err)
	}
}
