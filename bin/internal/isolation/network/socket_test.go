package network

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	client "github.com/asciimoth/almagest/bin/internal/isolation/runtime"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
)

type socketSystem struct {
	sysnet.System
	out, local gonnect.Network
}

func (s socketSystem) OutNet() gonnect.Network   { return s.out }
func (s socketSystem) LocalNet() gonnect.Network { return s.local }

type socketTestBackend struct {
	*fakeBackend
	sockets *SystemBackend
}

func (s socketTestBackend) OpenSocket(ctx context.Context, o protocol.SocketOptions) (OwnedTun, error) {
	return s.sockets.OpenSocket(ctx, o)
}
func (s socketTestBackend) ListInterfaces(ctx context.Context) (protocol.InterfaceSnapshot, error) {
	return s.sockets.ListInterfaces(ctx)
}
func (s socketTestBackend) ResolverSnapshot(ctx context.Context) (protocol.ResolverSnapshot, error) {
	return s.sockets.ResolverSnapshot(ctx)
}

func socketClient(t *testing.T) (*Broker, *client.Client) {
	t.Helper()
	b, f := testBroker(t)
	b.backend = socketTestBackend{f, &SystemBackend{system: socketSystem{out: &gonnect.NativeNetwork{}, local: &gonnect.NativeNetwork{}}}}
	return b, startClient(t, b)
}

func startClient(t *testing.T, b *Broker) *client.Client {
	t.Helper()
	a, z := net.Pipe()
	c, d := net.Pipe()
	e, g := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- b.Serve(ctx, transport.Lanes{Control: z, Data: d, Diagnostic: g}) }()
	runtime, err := client.New(ctx, transport.Lanes{Control: a, Data: c, Diagnostic: e}, b.session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runtime.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("broker did not stop")
		}
	})
	return runtime
}

func TestSocketRelay(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		t.Run(network, func(t *testing.T) {
			_, c := socketClient(t)
			n := c.Network(protocol.NetworkOutbound, nil)
			if n.IsNative() {
				t.Fatal("relay claims native access")
			}
			host := "127.0.0.1:0"
			if network == "tcp6" {
				host = "[::1]:0"
			}
			listener, err := n.ListenTCP(context.Background(), network, host)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			conn, err := n.DialTCP(context.Background(), network, "", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			accepted, err := listener.AcceptTCP()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = accepted.Close() }()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_ = accepted.SetDeadline(time.Now().Add(3 * time.Second))
			payload := bytes.Repeat([]byte("stream-order"), 10000)
			sent := make(chan error, 1)
			go func() {
				_, err := conn.Write(payload)
				if err == nil {
					err = conn.CloseWrite()
				}
				sent <- err
			}()
			got, err := io.ReadAll(accepted)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("read %d bytes: %v", len(got), err)
			}
			if err = <-sent; err != nil {
				t.Fatal(err)
			}
			if _, err = accepted.Write([]byte("reply")); err != nil {
				t.Fatal(err)
			}
			out := make([]byte, 5)
			if _, err = io.ReadFull(conn, out); err != nil || string(out) != "reply" {
				t.Fatalf("half close reply %q %v", out, err)
			}
			if err = listener.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = accepted.Write([]byte("after-listener-close")); err != nil {
				t.Fatal(err)
			}
			if err = conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = conn.Read(out); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("deadline: %v", err)
			}
		})
	}
}

func TestPacketRelay(t *testing.T) {
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			_, c := socketClient(t)
			n := c.Network(protocol.NetworkLocal, nil)
			host := "127.0.0.1:0"
			if network == "udp6" {
				host = "[::1]:0"
			}
			receiver, err := n.ListenUDP(context.Background(), network, host)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = receiver.Close() }()
			sender, err := n.DialUDP(context.Background(), network, "", receiver.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sender.Close() }()
			_ = sender.SetDeadline(time.Now().Add(time.Second))
			_ = receiver.SetDeadline(time.Now().Add(time.Second))
			for _, payload := range [][]byte{{}, []byte("packet"), bytes.Repeat([]byte{7}, 1200)} {
				if count, err := sender.Write(payload); err != nil || count != len(payload) {
					t.Fatalf("write %d %v", count, err)
				}
				out := make([]byte, 2048)
				count, from, err := receiver.ReadFromUDPAddrPort(out)
				if err != nil || !bytes.Equal(out[:count], payload) || !from.IsValid() {
					t.Fatalf("packet %d %v %v", count, from, err)
				}
				if _, err = receiver.WriteToUDPAddrPort(out[:count], from); err != nil {
					t.Fatal(err)
				}
				count, err = sender.Read(out)
				if err != nil || !bytes.Equal(out[:count], payload) {
					t.Fatalf("echo %d %v", count, err)
				}
			}
			if _, err = sender.Write([]byte("truncate")); err != nil {
				t.Fatal(err)
			}
			out := make([]byte, 2)
			count, _, flags, _, err := receiver.ReadMsgUDP(out, nil)
			if err != nil || count != 2 || flags == 0 {
				t.Fatalf("truncation %d %d %v", count, flags, err)
			}
			pending := make(chan error, 1)
			go func() { _, err := receiver.Read(make([]byte, 20)); pending <- err }()
			time.Sleep(10 * time.Millisecond)
			if err = receiver.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-pending:
				if !errors.Is(err, net.ErrClosed) {
					t.Fatalf("blocked read close: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("close blocked")
			}
		})
	}
}

func TestSocketAuthority(t *testing.T) {
	b, _ := socketClient(t)
	req := protocol.OpenSocketRequest{Meta: meta(b, "socket"), Options: protocol.SocketOptions{Purpose: protocol.NetworkOutbound, Kind: protocol.SocketListenUDP, Network: "udp4", Local: netip.MustParseAddrPort("127.0.0.1:0")}}
	result, err := b.OpenSocket(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := b.OpenSocket(context.Background(), req)
	if err != nil || replay.Resource != result.Resource {
		t.Fatal("receipt replay", err)
	}
	req.Options.Local = netip.MustParseAddrPort("127.0.0.1:1")
	if _, err = b.OpenSocket(context.Background(), req); !errors.Is(err, protocol.ErrOperationConflict) {
		t.Fatal(err)
	}
	forged := result.Resource
	forged.Generation++
	if _, err = b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "stale"), Resource: forged}); !errors.Is(err, protocol.ErrStaleGeneration) {
		t.Fatal(err)
	}
	if _, err = b.ReleaseGraphEpoch(context.Background(), protocol.ReleaseGraphEpochRequest{Meta: meta(b, "release"), GraphEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	closed := b.resources[result.Resource.ID].state.Closed
	b.mu.Unlock()
	if !closed {
		t.Fatal("epoch did not close socket")
	}
}

func TestSocketCancellationAndOffers(t *testing.T) {
	t.Run("pending_accept_deadline", func(t *testing.T) {
		_, c := socketClient(t)
		listener, err := c.Network(protocol.NetworkLocal, nil).ListenTCP(context.Background(), "tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			conn, e := listener.AcceptTCP()
			if conn != nil {
				_ = conn.Close()
			}
			done <- e
		}()
		time.Sleep(10 * time.Millisecond)
		if err = listener.SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-done:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("deadline did not wake pending accept")
		}
		if err = listener.SetDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		remote, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = remote.Close() }()
		conn, err := listener.AcceptTCP()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	})
	t.Run("pending_deadline_and_peer_loss", func(t *testing.T) {
		b, c := socketClient(t)
		conn, err := c.Network(protocol.NetworkOutbound, nil).ListenUDP(context.Background(), "udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := conn.Read(make([]byte, 10)); done <- err }()
		time.Sleep(10 * time.Millisecond)
		if err = conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-done:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("deadline did not wake pending read")
		}
		if err = conn.SetReadDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		go func() { _, err := conn.Read(make([]byte, 10)); done <- err }()
		_ = c.Close()
		select {
		case err = <-done:
			if err == nil {
				t.Fatal("peer loss succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("peer loss did not wake read")
		}
		until := time.Now().Add(time.Second)
		for {
			b.mu.Lock()
			live := 0
			for _, r := range b.resources {
				if !r.state.Closed {
					live++
				}
			}
			b.mu.Unlock()
			if live == 0 {
				break
			}
			if time.Now().After(until) {
				t.Fatal("peer loss leaked native socket")
			}
			time.Sleep(time.Millisecond)
		}
	})
	t.Run("accept_offer_revoke", func(t *testing.T) {
		b, _ := socketClient(t)
		opened, err := b.OpenSocket(context.Background(), protocol.OpenSocketRequest{Meta: meta(b, "listener"), Options: protocol.SocketOptions{Purpose: protocol.NetworkLocal, Kind: protocol.SocketListenTCP, Network: "tcp4", Local: netip.MustParseAddrPort("127.0.0.1:0")}})
		if err != nil {
			t.Fatal(err)
		}
		remote, err := net.DialTimeout("tcp4", opened.State.Local.String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = remote.Close() }()
		offered, err := b.Channel(context.Background(), protocol.ChannelRequest{Meta: meta(b, ""), Resource: opened.Resource, Kind: protocol.ChannelAccept})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.Channel(context.Background(), protocol.ChannelRequest{Meta: meta(b, ""), Resource: offered.Resource, Kind: protocol.ChannelRead, Request: 1, Size: 10}); !errors.Is(err, protocol.ErrPolicyDenied) {
			t.Fatal("unclaimed socket allowed I/O", err)
		}
		if _, err = b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "listener-close"), Resource: opened.Resource}); err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		closed := b.resources[offered.Resource.ID].state.Closed
		b.mu.Unlock()
		if !closed {
			t.Fatal("listener close leaked offer")
		}
		if _, err = b.Channel(context.Background(), protocol.ChannelRequest{Meta: meta(b, ""), Resource: offered.Resource, Kind: protocol.ChannelClaim}); err == nil {
			t.Fatal("claim revived closed socket")
		}
	})
}

func TestSocketControlUnderLoad(t *testing.T) {
	b, c := socketClient(t)
	network := c.Network(protocol.NetworkOutbound, nil)
	var connections []gonnect.UDPConn
	for range 32 {
		conn, err := network.ListenUDP(context.Background(), "udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	done := make(chan error, len(connections))
	for _, conn := range connections {
		go func() { _, err := conn.Read(make([]byte, 32)); done <- err }()
	}
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	status, err := c.Status(context.Background())
	if err != nil || status.LiveResources != uint32(len(connections)) {
		t.Fatal(status, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("status blocked by data: %v", elapsed)
	}
	for _, conn := range connections {
		if err = conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for range connections {
		select {
		case err = <-done:
			if err == nil {
				t.Fatal("closed read succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("close did not drain reads")
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.resources {
		if !r.state.Closed {
			t.Fatal("live resource after close")
		}
	}
}

func (s socketTestBackend) ListNetworkInterfaces(ctx context.Context, purpose protocol.NetworkPurpose) (protocol.InterfaceSnapshot, error) {
	return s.sockets.ListNetworkInterfaces(ctx, purpose)
}

func exerciseTunRelay(t *testing.T, b *Broker) {
	t.Helper()
	c := startClient(t, b)
	device, err := c.CreateTun(context.Background(), protocol.CreateTunRequest{Options: protocol.TunOptions{MTU: 1400, Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")}, Routes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}})
	if err != nil {
		b.mu.Lock()
		var details []error
		for _, receipt := range b.receipts {
			details = append(details, receipt.err)
		}
		b.mu.Unlock()
		t.Fatalf("TUN create: %v; native receipts: %v", err, details)
	}
	defer func() { _ = device.Close() }()
	if device.File() != nil || device.IsNative() {
		t.Fatal("native TUN escaped broker")
	}
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socket.Close() }()
	_ = socket.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = socket.WriteToUDP([]byte("outbound"), &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 12345}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1600)
	sizes := make([]int, 1)
	matched := false
	for range 16 {
		count, readErr := device.Read([][]byte{buffer}, sizes, 7)
		if readErr != nil || count != 1 {
			t.Fatalf("TUN read %d %v", count, readErr)
		}
		// The kernel can emit IPv6 neighbour discovery when the link comes up.
		if bytes.HasSuffix(buffer[7:7+sizes[0]], []byte("outbound")) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatal("outbound UDP was not delivered")
	}
	payload := []byte("inbound")
	packet := make([]byte, 28+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:], []byte{192, 0, 2, 2, 192, 0, 2, 1})
	sum := uint32(0)
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[i:]))
	}
	for sum > 65535 {
		sum = (sum & 65535) + (sum >> 16)
	}
	binary.BigEndian.PutUint16(packet[10:], ^uint16(sum))
	binary.BigEndian.PutUint16(packet[20:], 12345)
	binary.BigEndian.PutUint16(packet[22:], uint16(socket.LocalAddr().(*net.UDPAddr).Port))
	binary.BigEndian.PutUint16(packet[24:], uint16(8+len(payload)))
	copy(packet[28:], payload)
	if n, err := device.Write([][]byte{append(make([]byte, 5), packet...)}, 5); err != nil || n != 1 {
		t.Fatalf("TUN write %d %v", n, err)
	}
	n, _, err := socket.ReadFromUDP(buffer)
	if err != nil || string(buffer[:n]) != "inbound" {
		t.Fatalf("native read %q %v", buffer[:n], err)
	}
}

func TestSocketResolverAndInterfaces(t *testing.T) {
	b, c := socketClient(t)
	n := c.Network(protocol.NetworkOutbound, nil)
	interfaces, err := n.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatalf("interfaces %d %v", len(interfaces), err)
	}
	for _, iface := range interfaces {
		byName, err := n.InterfacesByName(iface.Name())
		if err != nil || len(byName) == 0 {
			t.Fatal(err)
		}
		byIndex, err := n.InterfacesByIndex(iface.Index())
		if err != nil || len(byIndex) == 0 {
			t.Fatal(err)
		}
	}
	if _, err = n.InterfaceAddrs(); err != nil {
		t.Fatal(err)
	}
	if _, err = n.InterfaceMulticastAddrs(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = n.LookupHost(ctx, "unconfigured.invalid"); err == nil {
		t.Fatal("missing resolver unexpectedly resolved")
	}
	native, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = native.Close() }()
	responseDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		count, from, err := native.ReadFromUDP(buffer)
		if err != nil {
			responseDone <- err
			return
		}
		query, err := gdns.Unpack(buffer[:count])
		if err != nil {
			responseDone <- err
			return
		}
		query.Response = true
		query.Answers = []gdns.Resource{{Name: query.Questions[0].Name, Type: gdns.TypeA, Class: gdns.ClassIN, TTL: 1, Data: []byte{192, 0, 2, 123}}}
		wire, err := gdns.Pack(query)
		if err == nil {
			_, err = native.WriteToUDP(wire, from)
		}
		responseDone <- err
	}()
	backend := b.backend.(socketTestBackend)
	backend.sockets.SetResolverSnapshotSource(func(context.Context) (protocol.ResolverSnapshot, error) {
		return protocol.ResolverSnapshot{Revision: 1, Servers: []netip.AddrPort{endpoint(native.LocalAddr())}}, nil
	})
	dns, err := c.SnapshotDNS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dns.Close() }()
	resolved := c.Network(protocol.NetworkOutbound, gdns.NewResolver(dns))
	addresses, err := resolved.LookupIP(ctx, "ip4", "relay.invalid")
	if err != nil || len(addresses) != 1 || addresses[0].String() != "192.0.2.123" {
		t.Fatalf("DNS %v %v", addresses, err)
	}
	if err = <-responseDone; err != nil {
		t.Fatal(err)
	}
}

type interfaceTestNetwork struct{ gonnect.Network }

func (interfaceTestNetwork) Interfaces() ([]gonnect.NetworkInterface, error) {
	return []gonnect.NetworkInterface{&gonnect.LiteralInterface{IDVal: "unknown-mtu", IndexVal: 1, NameVal: "loopback", MTUVal: -1}}, nil
}
func TestSocketUnknownInterfaceMTU(t *testing.T) {
	backend := &SystemBackend{system: socketSystem{out: interfaceTestNetwork{}}}
	snapshot, err := backend.ListInterfaces(context.Background())
	if err != nil || len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].MTU != -1 {
		t.Fatalf("unknown MTU lost: %+v %v", snapshot, err)
	}
}
