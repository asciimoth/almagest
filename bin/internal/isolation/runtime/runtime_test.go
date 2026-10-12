package runtime

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/tun"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name, input, network string
		want                 netip.AddrPort
		valid                bool
	}{
		{"empty", "", "tcp4", netip.AddrPort{}, true},
		{"ipv4", "192.0.2.1:443", "tcp4", netip.MustParseAddrPort("192.0.2.1:443"), true},
		{"ipv6", "[2001:db8::1]:53", "udp6", netip.MustParseAddrPort("[2001:db8::1]:53"), true},
		{"wildcard4", ":80", "tcp4", netip.MustParseAddrPort("0.0.0.0:80"), true},
		{"wildcard6", ":80", "tcp6", netip.MustParseAddrPort("[::]:80"), true},
		{"missing_port", "192.0.2.1", "tcp4", netip.AddrPort{}, false},
		{"name", "example.test:80", "tcp4", netip.AddrPort{}, false},
		{"large_port", "192.0.2.1:65536", "tcp4", netip.AddrPort{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEndpoint(tc.input, tc.network)
			if (err == nil) != tc.valid || err == nil && got != tc.want {
				t.Fatalf("got %v, %v; want %v, valid=%v", got, err, tc.want, tc.valid)
			}
		})
	}
}

func TestRuntimeErrorAndDeadlineConversion(t *testing.T) {
	for code, want := range map[string]error{
		"":              nil,
		"eof":           io.EOF,
		"closed":        net.ErrClosed,
		"deadline":      os.ErrDeadlineExceeded,
		"canceled":      context.Canceled,
		"short_buffer":  io.ErrShortBuffer,
		"short_write":   io.ErrShortWrite,
		"unsupported":   protocol.ErrCapabilityUnsupported,
		"invalid":       protocol.ErrInvalidContract,
		"permission":    os.ErrPermission,
		"made_up_error": protocol.ErrInvalidContract,
	} {
		got := decodeError(code)
		if want == nil {
			if got != nil {
				t.Fatalf("%q: got %v", code, got)
			}
		} else if !errors.Is(got, want) {
			t.Fatalf("%q: got %v, want %v", code, got, want)
		}
	}
	if remaining(time.Time{}) != nil {
		t.Fatal("zero deadline produced a duration")
	}
	if got := remaining(time.Now().Add(-time.Second)); got == nil || *got != 0 {
		t.Fatalf("expired deadline = %v", got)
	}
	if got := remaining(time.Now().Add(time.Second)); got == nil || *got <= 0 || *got > int64(time.Second) {
		t.Fatalf("future deadline = %v", got)
	}
}

func TestTunAdapterStateAndEvents(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	client := &Client{session: session, tuns: make(map[protocol.ResourceID]*Tun)}
	client.epoch.Store(1)
	ref := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: session.ID, Type: protocol.ResourceTun, Generation: 1, GraphEpoch: 1}
	adapter, err := client.Tun(ref, protocol.ResourceState{Name: "relay0", MTU: 1400, Revision: 1, Up: true})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.finish()
	if adapter.IsNative() || adapter.File() != nil || adapter.BatchSize() != 1 || adapter.MWO() != 0 || adapter.MRO() != 0 {
		t.Fatal("adapter claims native access or exposed unexpected handle/batch properties")
	}
	if name, _ := adapter.Name(); name != "relay0" {
		t.Fatalf("name = %q", name)
	}
	if mtu, _ := adapter.MTU(); mtu != 1400 {
		t.Fatalf("mtu = %d", mtu)
	}
	if event := <-adapter.Events(); event != tun.EventUp {
		t.Fatalf("initial event = %v", event)
	}

	adapter.event(transport.Event{Resource: ref, Revision: 1, Up: false})
	select {
	case event := <-adapter.Events():
		t.Fatalf("stale event delivered: %v", event)
	default:
	}
	adapter.event(transport.Event{Resource: ref, Revision: 2, MTUChanged: true})
	if event := <-adapter.Events(); event != tun.EventDown|tun.EventMTUUpdate {
		t.Fatalf("state event = %v", event)
	}
	if count, err := adapter.Read(nil, nil, 0); count != 0 || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("invalid read = %d, %v", count, err)
	}
	if count, err := adapter.Write([][]byte{{1}}, 1); count != 0 || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("invalid write = %d, %v", count, err)
	}

	replacement := ref
	replacement.Generation = 2
	newAdapter, err := client.Tun(replacement, protocol.ResourceState{Name: "relay0", MTU: 1400, Revision: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer newAdapter.finish()
	if equivalent, err := client.Tun(replacement, protocol.ResourceState{Name: "relay0", MTU: 1400, Revision: 3}); err != nil || equivalent != newAdapter {
		t.Fatalf("equivalent generation = %p, %v; want existing %p", equivalent, err, newAdapter)
	}
	if _, open := <-adapter.Events(); open {
		t.Fatal("replacement did not terminate old adapter")
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if client.tuns[ref.ID] != newAdapter {
		t.Fatal("closing the stale adapter removed its replacement")
	}
	if _, err = client.Tun(ref, protocol.ResourceState{MTU: 1400}); !errors.Is(err, protocol.ErrOperationConflict) {
		t.Fatalf("old generation accepted: %v", err)
	}
}

func TestTunDeadlinesRemainLocal(t *testing.T) {
	device := &Tun{Conn: &Conn{}}
	readDeadline := time.Now().Add(time.Second)
	writeDeadline := readDeadline.Add(time.Second)
	if err := device.SetReadDeadline(readDeadline); err != nil {
		t.Fatal(err)
	}
	if err := device.SetWriteDeadline(writeDeadline); err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if !device.readDeadline.Equal(readDeadline) || !device.writeDeadline.Equal(writeDeadline) {
		t.Fatalf("deadlines = %v, %v", device.readDeadline, device.writeDeadline)
	}
}

func TestSystemTunWaitsForGenerationReplacement(t *testing.T) {
	resourceID := protocol.ResourceID{1}
	old := &Tun{Conn: &Conn{ref: protocol.ResourceRef{ID: resourceID, Generation: 1}}}
	next := &Tun{Conn: &Conn{ref: protocol.ResourceRef{ID: resourceID, Generation: 2}}}
	device := &SystemTun{current: old, replacing: true, replaced: make(chan struct{})}
	done := make(chan *Tun, 1)
	go func() { done <- device.replacementAfter(old) }()
	select {
	case <-done:
		t.Fatal("replacement wait returned before installation")
	case <-time.After(10 * time.Millisecond):
	}
	device.mu.Lock()
	device.current = next
	device.replacing = false
	close(device.replaced)
	device.mu.Unlock()
	select {
	case got := <-done:
		if got != next {
			t.Fatalf("replacement = %p, want %p", got, next)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement wait did not finish")
	}
}

func TestSystemTunEndsReadAcrossSourceReplacement(t *testing.T) {
	old := &Tun{Conn: &Conn{ref: protocol.ResourceRef{ID: protocol.ResourceID{1}, Generation: 1}}}
	next := &Tun{Conn: &Conn{ref: protocol.ResourceRef{ID: protocol.ResourceID{2}, Generation: 1}}}
	device := &SystemTun{current: old, sourceGen: 1}
	if !device.beginReplacement() {
		t.Fatal("begin replacement failed")
	}
	done := make(chan *Tun, 1)
	go func() { done <- device.replacementAfter(old) }()

	device.install(next, nil)
	device.endReplacement()
	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("replacement = %p, want nil for a new source", got)
		}
	case <-time.After(time.Second):
		t.Fatal("source replacement wait did not finish")
	}
	if got := device.SourceGeneration(); got != 2 {
		t.Fatalf("source generation = %d, want 2", got)
	}
}

func TestTunAdapterRejectsInvalidState(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	client := &Client{session: session, tuns: make(map[protocol.ResourceID]*Tun)}
	client.epoch.Store(1)
	base := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: session.ID, Type: protocol.ResourceTun, Generation: 1, GraphEpoch: 1}
	for _, tc := range []struct {
		name  string
		ref   protocol.ResourceRef
		state protocol.ResourceState
		want  error
	}{
		{"type", func() protocol.ResourceRef { r := base; r.Type = protocol.ResourceSocket; return r }(), protocol.ResourceState{MTU: 1400}, protocol.ErrResourceType},
		{"foreign", func() protocol.ResourceRef { r := base; r.SessionID[0]++; return r }(), protocol.ResourceState{MTU: 1400}, protocol.ErrForeignResource},
		{"zero_mtu", base, protocol.ResourceState{}, protocol.ErrCapabilityUnsupported},
		{"large_mtu", base, protocol.ResourceState{MTU: protocol.MaxPacketRecord + 1}, protocol.ErrCapabilityUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.Tun(tc.ref, tc.state); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConfigureDNSRejectsInvalidReferences(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	client := &Client{session: session}
	client.epoch.Store(1)
	defaultRef := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: session.ID, Type: protocol.ResourceDefaultTun, Generation: 1, GraphEpoch: 1}
	device := &Tun{Conn: &Conn{client: client, ref: defaultRef}}
	dns := protocol.ResourceRef{ID: protocol.ResourceID{3}, SessionID: session.ID, Type: protocol.ResourceDNS, Generation: 1, GraphEpoch: 1}

	if _, err := client.ConfigureDNS(context.Background(), nil, dns, true); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("nil TUN: %v", err)
	}
	regularRef := defaultRef
	regularRef.Type = protocol.ResourceTun
	regular := &Tun{Conn: &Conn{client: client, ref: regularRef}}
	if _, err := client.ConfigureDNS(context.Background(), regular, dns, true); !errors.Is(err, protocol.ErrResourceType) {
		t.Fatalf("regular TUN: %v", err)
	}
	foreign := dns
	foreign.SessionID[0]++
	if _, err := client.ConfigureDNS(context.Background(), device, foreign, true); !errors.Is(err, protocol.ErrForeignResource) {
		t.Fatalf("foreign DNS endpoint: %v", err)
	}
	if _, err := client.ConfigureDNS(context.Background(), device, dns, false); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("endpoint on detach: %v", err)
	}
	zeroGeneration := defaultRef
	zeroGeneration.Generation = 0
	if _, err := client.ConfigureDNS(context.Background(), &Tun{Conn: &Conn{client: client, ref: zeroGeneration}}, dns, true); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("zero TUN generation: %v", err)
	}
	exhausted := defaultRef
	exhausted.Generation = math.MaxUint64
	if _, err := client.ConfigureDNS(context.Background(), &Tun{Conn: &Conn{client: client, ref: exhausted}}, dns, true); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("exhausted TUN generation: %v", err)
	}
	zeroDNS := dns
	zeroDNS.Generation = 0
	if _, err := client.ConfigureDNS(context.Background(), device, zeroDNS, true); !errors.Is(err, protocol.ErrStaleGeneration) {
		t.Fatalf("zero DNS generation: %v", err)
	}
	newDNS := dns
	newDNS.Generation = 2
	if _, err := client.ConfigureDNS(context.Background(), device, newDNS, true); !errors.Is(err, protocol.ErrStaleGeneration) {
		t.Fatalf("changed DNS generation: %v", err)
	}
}

func FuzzParseEndpoint(f *testing.F) {
	for _, seed := range []string{"", ":0", "127.0.0.1:80", "[::1]:443", "example.test:53", "[fe80::1%test0]:1"} {
		f.Add(seed, "udp6")
	}
	f.Fuzz(func(t *testing.T, input, network string) {
		endpoint, err := parseEndpoint(input, network)
		if err == nil && input != "" && !endpoint.IsValid() {
			t.Fatalf("successful parse produced invalid endpoint: %q", input)
		}
	})
}
