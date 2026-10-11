package network

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	client "github.com/asciimoth/almagest/bin/internal/isolation/runtime"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
)

type systemTestDNS struct {
	requests chan gdns.Request
	done     chan struct{}
	once     sync.Once
}

func newSystemTestDNS() *systemTestDNS {
	d := &systemTestDNS{requests: make(chan gdns.Request), done: make(chan struct{})}
	go func() {
		for {
			select {
			case <-d.done:
				return
			case request := <-d.requests:
				response := &gdns.Message{ID: request.Message.ID, Response: true, Questions: append([]gdns.Question(nil), request.Message.Questions...)}
				request.Reply <- gdns.Response{Message: response}
			}
		}
	}()
	return d
}

func (d *systemTestDNS) Requests() chan<- gdns.Request { return d.requests }
func (d *systemTestDNS) Close() error {
	d.once.Do(func() { close(d.done) })
	return nil
}

func systemRuntimeFixture(t *testing.T) (*Broker, *fakeBackend, *client.System) {
	t.Helper()
	b, backend := testBroker(t)
	backend.caps = &protocol.CapabilitySnapshot{SchemaVersion: sysnet.CapabilitySchemaVersion, Revision: 8, Operations: []protocol.OperationCapability{{
		Operation: protocol.OperationCreateTun, NativeOperation: string(sysnet.OpCreate), Target: string(sysnet.TargetTun), Family: string(sysnet.FamilyNone),
		Capability: protocol.Capability{State: protocol.CapabilitySupported, AttemptAllowed: true},
	}}}
	backend.create = func(_ context.Context, request protocol.CreateTunRequest) (OwnedTun, error) {
		device := newFakeTun()
		device.state.Name = request.Options.Name
		device.state.MTU = request.Options.MTU
		device.state.Addresses = append([]netip.Prefix(nil), request.Options.Addresses...)
		device.state.Routes = append([]netip.Prefix(nil), request.Options.Routes...)
		device.state.SystemCapabilityRevision = 8
		device.state.CapabilityRevision = 3
		for _, operation := range []protocol.Operation{protocol.OperationSetTunMTU, protocol.OperationSetTunAddresses, protocol.OperationSetTunRoutes, protocol.OperationSetTunName} {
			device.state.Capabilities = append(device.state.Capabilities, protocol.OperationCapability{Operation: operation, Capability: protocol.Capability{State: protocol.CapabilitySupported, AttemptAllowed: true}})
		}
		backend.mu.Lock()
		backend.devices = append(backend.devices, device)
		backend.mu.Unlock()
		return device, nil
	}
	system := startClient(t, b).System()
	t.Cleanup(func() { _ = system.Close() })
	return b, backend, system
}

func TestRuntimeSystemTunParityAndStableGenerations(t *testing.T) {
	_, _, system := systemRuntimeFixture(t)
	report := system.Capabilities()
	if err := sysnet.ValidateCapabilityReport(report); err != nil || report.Revision != 8 {
		t.Fatalf("capabilities=%+v %v", report, err)
	}
	if err := system.CheckTunOpts(sysnet.TunOpts{MTU: -1}).Err(); !errors.Is(err, sysnet.ErrInvalidOptions) {
		t.Fatalf("negative MTU validation=%v", err)
	}
	device, err := system.BuildTun(sysnet.TunOpts{Name: "before", MTU: 1400, TunAddrs: []string{"192.0.2.1/24"}, TunRoutes: []string{"198.51.100.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	stable := device
	for _, update := range []func() error{
		func() error { return system.SetTunName(device, "after") },
		func() error { return system.SetTunMTU(device, 1500) },
		func() error { return system.SetTunAddrs(device, []string{"192.0.2.2/32"}) },
		func() error { return system.AddTunAddr(device, "2001:db8::2/128") },
		func() error { return system.SetTunRoutes(device, []string{"203.0.113.0/24"}) },
		func() error { return system.AddTunRoute(device, "2001:db8:1::/64") },
	} {
		if err := update(); err != nil {
			t.Fatal(err)
		}
		if device != stable {
			t.Fatal("public TUN identity changed after an update")
		}
	}
	if name, err := device.Name(); err != nil || name != "after" {
		t.Fatalf("name=%q %v", name, err)
	}
	if mtu, err := device.MTU(); err != nil || mtu != 1500 {
		t.Fatalf("MTU=%d %v", mtu, err)
	}
	if addrs, err := system.GetTunAddrs(device); err != nil || len(addrs) != 2 || addrs[0] != "192.0.2.2/32" || addrs[1] != "2001:db8::2/128" {
		t.Fatalf("addresses=%v %v", addrs, err)
	}
	if routes, err := system.GetTunRoutes(device); err != nil || len(routes) != 2 {
		t.Fatalf("routes=%v %v", routes, err)
	}
	if warnings := system.TunWarnings(device); len(warnings) != 1 || warnings[0] != "native_warning" {
		t.Fatalf("warnings=%v", warnings)
	}
	if report, err := system.CapabilitiesForTun(device); err != nil || len(report.Operations) != 4 {
		t.Fatalf("TUN capabilities=%+v %v", report, err)
	}
	if err := system.SetTunMTU(&client.Tun{}, 1400); !errors.Is(err, sysnet.ErrUnknownTun) {
		t.Fatalf("foreign TUN=%v", err)
	}
}

func TestRuntimeSystemDefaultTunReplacementAndApplicationDNS(t *testing.T) {
	_, backend, system := systemRuntimeFixture(t)
	first, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{Name: "first", MTU: 1400})
	if err != nil {
		t.Fatal(err)
	}
	second, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{Name: "second", MTU: 1450})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("default TUN replacement changed its public identity")
	}
	if name, _ := second.Name(); name != "second" {
		t.Fatalf("replacement name=%q", name)
	}

	queryResult := make(chan error, 1)
	backend.dnsHook = func(ctx context.Context, _ OwnedTun, endpoint OwnedDNSEndpoint, enabled bool) error {
		backend.mu.Lock()
		backend.dnsOn = enabled
		backend.mu.Unlock()
		if !enabled {
			return nil
		}
		query := &gdns.Message{ID: 42, Questions: []gdns.Question{{Name: "isolated.test", Type: gdns.TypeA, Class: gdns.ClassIN}}}
		response, err := gdns.Query(ctx, endpoint.Resolver(), query)
		if err == nil && (response == nil || !response.Response || response.ID != query.ID) {
			err = protocol.ErrInvalidContract
		}
		queryResult <- err
		return err
	}
	upstream := newSystemTestDNS()
	defer func() { _ = upstream.Close() }()
	if err := second.SetDNS(upstream); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-queryResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("application DNS relay did not answer")
	}
	backend.mu.Lock()
	dnsOn := backend.dnsOn
	backend.mu.Unlock()
	if !dnsOn {
		t.Fatal("native backend did not receive application DNS")
	}
	if err := second.SetDNS(nil); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	dnsOn = backend.dnsOn
	backend.mu.Unlock()
	if dnsOn {
		t.Fatal("native backend DNS remained attached")
	}
}

func TestCreateDNSEndpointReceiptsAuthorityAndCleanup(t *testing.T) {
	b, _ := testBroker(t)
	req := protocol.CreateDNSEndpointRequest{Meta: meta(b, "dns-endpoint")}
	created, err := b.CreateDNSEndpoint(context.Background(), req)
	if err != nil || created.Resource.Type != protocol.ResourceDNS || created.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("create=%+v %v", created, err)
	}
	replayed, err := b.CreateDNSEndpoint(context.Background(), req)
	if err != nil || replayed.Resource != created.Resource {
		t.Fatalf("replay=%+v %v", replayed, err)
	}
	conflict := req
	conflict.Meta.GraphEpoch++
	if _, err := b.CreateDNSEndpoint(context.Background(), conflict); !errors.Is(err, protocol.ErrOperationConflict) && !errors.Is(err, protocol.ErrStaleGraphEpoch) {
		t.Fatalf("conflict=%v", err)
	}
	foreign := req
	foreign.Meta.OperationID = "foreign"
	foreign.Meta.SessionID[0]++
	if _, err := b.CreateDNSEndpoint(context.Background(), foreign); !errors.Is(err, protocol.ErrForeignResource) {
		t.Fatalf("foreign=%v", err)
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	closed := b.resources[created.Resource.ID].state.Closed
	b.mu.Unlock()
	if !closed {
		t.Fatal("shutdown leaked application DNS endpoint")
	}
}

func TestRuntimeSystemRejectsMalformedNativeValues(t *testing.T) {
	_, _, system := systemRuntimeFixture(t)
	for _, value := range []sysnet.TunOpts{{MTU: -1}, {MTU: 1400, TunAddrs: []string{"host.invalid"}}, {MTU: 1400, TunRoutes: []string{"129.0.0.0/1/2"}}} {
		if _, err := system.BuildTun(value); !errors.Is(err, sysnet.ErrInvalidOptions) {
			t.Fatalf("BuildTun(%+v)=%v", value, err)
		}
	}
	for _, value := range []sysnet.DefaultTunOpts{{MTU: -1}, {MTU: 1400, DnsIP: "host.invalid"}, {MTU: 1400, Include: []sysnet.Rule{{Type: "uid"}}, Exclude: []sysnet.Rule{{Type: "gid"}}}} {
		if _, err := system.BuildDefaultTun(value); !errors.Is(err, sysnet.ErrInvalidOptions) {
			t.Fatalf("BuildDefaultTun(%+v)=%v", value, err)
		}
	}
	if _, err := netip.ParsePrefix("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSystemSerializesAdditiveUpdates(t *testing.T) {
	_, _, system := systemRuntimeFixture(t)
	device, err := system.BuildTun(sysnet.TunOpts{Name: "concurrent", MTU: 1400})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 1; i <= 16; i++ {
		address := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}).String() + "/32"
		group.Go(func() {
			if err := system.AddTunAddr(device, address); err != nil {
				t.Errorf("AddTunAddr(%s): %v", address, err)
			}
		})
	}
	group.Wait()
	addresses, err := system.GetTunAddrs(device)
	if err != nil || len(addresses) != 16 {
		t.Fatalf("concurrent addresses=%v %v", addresses, err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if err := system.SetTunMTU(device, 1500); !errors.Is(err, sysnet.ErrUnknownTun) {
		t.Fatalf("update after close=%v", err)
	}
	select {
	case _, open := <-device.Events():
		if open {
			t.Fatal("closed stable TUN still emits events")
		}
	case <-time.After(time.Second):
		t.Fatal("stable TUN event stream did not close")
	}
}

func TestRuntimeSystemBuildEngineComposition(t *testing.T) {
	_, _, system := systemRuntimeFixture(t)
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(8),
		system, nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if engine.AllocIP() == nil || engine.AllocSubnet() == nil {
		t.Fatal("engine did not receive the isolated allocators")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := system.BuildTun(sysnet.TunOpts{MTU: 1400}); !errors.Is(err, sysnet.ErrUnavailable) {
		t.Fatalf("engine close did not close isolated system: %v", err)
	}
}
