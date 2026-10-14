package network

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
)

// Compare the same native backend, launch identity, and stable socket with the
// production RPC adapter. No observed-packet registration precedes any lookup.
func nativeMatcherParity(t *testing.T, system sysnet.System, ruleType string) {
	b, _ := testBroker(t)
	backend, err := NewSystemBackend(system, CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	b.backend = backend
	c := startClient(t, b)
	rule := sysnet.Rule{Type: ruleType, Rule: strconv.Itoa(os.Getpid())}
	direct, err := system.BuildMatcher(rule)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.Close() }()
	proxied, err := c.BuildMatcher(rule)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proxied.Close() }()
	compare := func(t *testing.T, flow sockowner.FlowTuple) {
		t.Helper()
		want, wantErr := direct.Match(flow)
		got, gotErr := proxied.Match(flow)
		if got != want || transport.ErrorCode(gotErr) != transport.ErrorCode(nativeError(wantErr)) {
			t.Fatalf("native=%v/%v RPC=%v/%v flow=%+v", want, wantErr, got, gotErr, flow)
		}
	}
	for _, family := range []string{"4", "6"} {
		host := "127.0.0.1"
		if family == "6" {
			host = "::1"
		}
		t.Run("tcp"+family, func(t *testing.T) {
			listener, err := c.Network(protocol.NetworkLocal, nil).ListenTCP(context.Background(), "tcp"+family, net.JoinHostPort(host, "0"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			for range 4 { // Short connections use new ephemeral ports.
				conn, err := net.Dial("tcp"+family, listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				accepted, err := listener.Accept()
				if err != nil {
					_ = conn.Close()
					t.Fatal(err)
				}
				flow, err := sockowner.IncomingConnPeerFlow(accepted)
				if err != nil {
					t.Fatal(err)
				}
				compare(t, *flow)
				want, err := sysnet.MatchConn(accepted, direct)
				if err != nil || !want {
					t.Fatalf("native live TCP owner: %v %v", want, err)
				}
				got, err := sysnet.MatchConn(accepted, proxied)
				if err != nil || !got {
					t.Fatalf("RPC MatchConn: %v %v", got, err)
				}
				_ = conn.Close()
				_ = accepted.Close()
				compare(t, *flow)
			}
		})
		t.Run("udp"+family, func(t *testing.T) {
			for _, bind := range []string{host, ""} {
				packet, err := net.ListenPacket("udp"+family, net.JoinHostPort(bind, "0"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = packet.Close() }()
				flow := sockowner.FlowTuple{Proto: "udp", LocalIP: net.ParseIP(host), LocalPort: uint16(packet.LocalAddr().(*net.UDPAddr).Port), RemoteIP: net.ParseIP(host), RemotePort: 54321}
				compare(t, flow)
				// Simulate a queued outgoing TUN packet, then change socket state.
				if family == "4" {
					data := make([]byte, 28)
					data[0], data[9] = 0x45, 17
					binary.BigEndian.PutUint16(data[2:4], uint16(len(data)))
					copy(data[12:16], flow.LocalIP.To4())
					copy(data[16:20], flow.RemoteIP.To4())
					binary.BigEndian.PutUint16(data[20:22], flow.LocalPort)
					binary.BigEndian.PutUint16(data[22:24], flow.RemotePort)
					binary.BigEndian.PutUint16(data[24:26], 8)
					queued, err := sockowner.FlowTupleFromOutgoingIPPacket(data)
					if err != nil {
						t.Fatal(err)
					}
					compare(t, queued)
					_ = packet.Close()
					compare(t, queued)
				}
			}
		})
	}
	// Retain a small, repeatable component measurement. Separate-process
	// performance is an optimization concern, not a functional matcher contract.
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = packet.Close() }()
	flow := sockowner.FlowTuple{Proto: "udp", LocalIP: net.IPv4(127, 0, 0, 1), LocalPort: uint16(packet.LocalAddr().(*net.UDPAddr).Port), RemoteIP: net.IPv4(127, 0, 0, 1), RemotePort: 53}
	var nativeTimes, rpcTimes []time.Duration
	var nativeTotal, rpcTotal time.Duration
	for range 256 {
		start := time.Now()
		_, err := direct.Match(flow)
		nativeTimes = append(nativeTimes, time.Since(start))
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		_, err = proxied.Match(flow)
		rpcTimes = append(rpcTimes, time.Since(start))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range nativeTimes {
		nativeTotal += nativeTimes[i]
		rpcTotal += rpcTimes[i]
	}
	slices.Sort(nativeTimes)
	slices.Sort(rpcTimes)
	t.Logf("UDP component latency (256 samples): native median=%s p99=%s; RPC median=%s p99=%s; added median=%s p99=%s", nativeTimes[128], nativeTimes[253], rpcTimes[128], rpcTimes[253], rpcTimes[128]-nativeTimes[128], rpcTimes[253]-nativeTimes[253])
	t.Logf("Sequential per-packet lookup ceiling: native=%.0f/s RPC=%.0f/s loss=%.1f%%", 256/nativeTotal.Seconds(), 256/rpcTotal.Seconds(), 100*(1-float64(nativeTotal)/float64(rpcTotal)))
}
