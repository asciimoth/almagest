package socksnodes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"golang.getoutline.org/sdk/transport"
	ss "golang.getoutline.org/sdk/transport/shadowsocks"
)

type shadowsocksReplyPacket struct {
	gonnect.PacketConn
	addrs []net.Addr
	calls int
}

type shadowsocksNoDeadlineConn struct{ net.Conn }

func (*shadowsocksNoDeadlineConn) SetReadDeadline(time.Time) error { return gonnect.ErrUnsupported }

func TestShadowsocksTCPHandshakeTimesOutWithoutConnDeadline(t *testing.T) {
	key, err := ss.NewEncryptionKey(DefaultShadowsocksMethod, "timeout-test")
	if err != nil {
		t.Fatal(err)
	}
	client, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	done := make(chan struct{})
	go func() {
		relayShadowsocksTCPWithTimeout(context.Background(), &shadowsocksNoDeadlineConn{client}, &gonnect.RejectNetwork{}, key, nil, 20*time.Millisecond, time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unauthenticated connection did not time out")
	}
}

type shadowsocksSlowTCPProxy struct {
	gonnect.Network
	started chan struct{}
}

func (n *shadowsocksSlowTCPProxy) Dial(ctx context.Context, _, _ string) (net.Conn, error) {
	close(n.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestShadowsocksTCPDestinationDialTimesOut(t *testing.T) {
	key, err := ss.NewEncryptionKey(DefaultShadowsocksMethod, "dial-timeout-test")
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer client.Close() //nolint:errcheck
	proxy := &shadowsocksSlowTCPProxy{Network: &gonnect.RejectNetwork{}, started: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		relayShadowsocksTCPWithTimeout(context.Background(), server, proxy, key, nil, time.Second, 20*time.Millisecond)
		close(done)
	}()
	writer := ss.NewWriter(client, key)
	if _, err := writer.Write(socks.ParseAddr("127.0.0.1:80")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxy.started:
	case <-time.After(time.Second):
		t.Fatal("destination dial did not start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("destination dial did not stop at its timeout")
	}
}

func (c *shadowsocksReplyPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.calls >= len(c.addrs) {
		return 0, nil, io.EOF
	}
	addr := c.addrs[c.calls]
	c.calls++
	return copy(b, "reply"), addr, nil
}

func TestShadowsocksBoundPacketAcceptsResolvedDomainReply(t *testing.T) {
	remote, err := transport.MakeNetAddr("udp", "target.example:53")
	if err != nil {
		t.Fatal(err)
	}
	packet := &shadowsocksReplyPacket{addrs: []net.Addr{
		&net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 54},
		&net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 53},
	}}
	bound := &shadowsocksBoundPacket{PacketConn: packet, remote: remote}
	buf := make([]byte, 16)
	n, err := bound.Read(buf)
	if err != nil || string(buf[:n]) != "reply" || packet.calls != 2 {
		t.Fatalf("domain reply: size=%d err=%v calls=%d", n, err, packet.calls)
	}
}

type shadowsocksSourcePacket struct {
	gonnect.PacketConn
	calls int
}

func (c *shadowsocksSourcePacket) ReadFrom(b []byte) (int, net.Addr, error) {
	c.calls++
	if c.calls > 1 {
		return 0, nil, io.EOF
	}
	return copy(b, "answer"), &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 53}, nil
}

type shadowsocksCapturePacket struct {
	gonnect.PacketConn
	data []byte
}

type shadowsocksReadOnlyPacket struct {
	gonnect.PacketConn
	calls int
}

func (c *shadowsocksReadOnlyPacket) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, gonnect.ErrUnsupported
}

func (c *shadowsocksReadOnlyPacket) Read(b []byte) (int, error) {
	c.calls++
	if c.calls > 1 {
		return 0, io.EOF
	}
	return copy(b, "fallback"), nil
}

func (c *shadowsocksCapturePacket) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.data = append([]byte(nil), b...)
	return len(b), nil
}

func TestShadowsocksServerUDPReplyReportsSource(t *testing.T) {
	key, err := ss.NewEncryptionKey("aes-256-gcm", "source-test")
	if err != nil {
		t.Fatal(err)
	}
	listener := &shadowsocksCapturePacket{}
	session := &shadowsocksUDPSession{conn: &shadowsocksSourcePacket{}, target: "target.example:53", client: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}}
	relayShadowsocksUDPReply(listener, session, key)
	if len(listener.data) == 0 {
		t.Fatal("server did not send UDP reply")
	}
	plain, err := ss.Unpack(nil, listener.data, key)
	if err != nil {
		t.Fatal(err)
	}
	addr := socks.SplitAddr(plain)
	if addr == nil || addr.String() != "192.0.2.1:53" || string(plain[len(addr):]) != "answer" {
		t.Fatalf("UDP reply address and data = %q", plain)
	}
}

func TestShadowsocksServerUDPReplySupportsReadOnlyPacket(t *testing.T) {
	key, err := ss.NewEncryptionKey("aes-256-gcm", "read-only-test")
	if err != nil {
		t.Fatal(err)
	}
	listener := &shadowsocksCapturePacket{}
	session := &shadowsocksUDPSession{conn: &shadowsocksReadOnlyPacket{}, target: "target.example:53", client: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}}
	relayShadowsocksUDPReply(listener, session, key)
	plain, err := ss.Unpack(nil, listener.data, key)
	if err != nil {
		t.Fatal(err)
	}
	addr := socks.SplitAddr(plain)
	if addr == nil || addr.String() != "target.example:53" || string(plain[len(addr):]) != "fallback" {
		t.Fatalf("fallback UDP reply address and data = %q", plain)
	}
}

type shadowsocksBlockingProxyNetwork struct {
	gonnect.Network
	started chan struct{}
	once    sync.Once
}

func (n *shadowsocksBlockingProxyNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	if address == "slow.example:53" {
		n.once.Do(func() { close(n.started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return n.Network.PacketDial(ctx, network, address)
}

func TestShadowsocksSlowUDPDialDoesNotBlockOtherClients(t *testing.T) {
	ctx := context.Background()
	network := gonnect.NewLoopbackNetwork()
	defer network.Close() //nolint:errcheck
	target, err := network.ListenPacket(ctx, "udp", "127.0.0.1:18599")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close() //nolint:errcheck
	go func() {
		buf := make([]byte, 128)
		for {
			n, addr, err := target.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = target.WriteTo(buf[:n], addr)
		}
	}()
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	cfg := shadowsocksConfig{address: "127.0.0.1:18399", method: DefaultShadowsocksMethod, password: "slow-dial-test", udp: true}
	key, err := makeShadowsocksKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &shadowsocksBlockingProxyNetwork{Network: network, started: make(chan struct{})}
	stop := make(chan struct{})
	defer func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- runShadowsocksServerOnce(ctx, w, 0, 1, stop, cfg, key, network, proxy, testWorkerSpawner{}, newShadowsocksReplayGuard())
	}()
	client, err := newShadowsocksNetwork(network, cfg.address, cfg.method, cfg.password, false, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		packet, dialErr := client.PacketDial(ctx, "udp", "slow.example:53")
		if dialErr == nil {
			_, dialErr = packet.Write([]byte("slow"))
			_ = packet.Close()
		}
		select {
		case <-proxy.started:
			goto blocked
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("slow dial did not start: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
blocked:
	fast, err := client.PacketDial(ctx, "udp", "127.0.0.1:18599")
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close() //nolint:errcheck
	_ = fast.SetDeadline(time.Now().Add(time.Second))
	if _, err := fast.Write([]byte("fast")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := fast.Read(buf)
	if err != nil || string(buf[:n]) != "fast" {
		t.Fatalf("fast UDP reply while slow dial is pending: data=%q err=%v", buf[:n], err)
	}
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop while a UDP dial was pending")
	}
}

func TestShadowsocksNetworkRejectsMissingUpstreamAndDirectMethods(t *testing.T) {
	if _, err := newShadowsocksNetwork(nil, DefaultShadowsocksAddress, DefaultShadowsocksMethod, "secret", true, true); err == nil {
		t.Fatal("missing upstream accepted")
	}
	upstream := &gonnect.RejectNetwork{}
	network, err := newShadowsocksNetwork(upstream, DefaultShadowsocksAddress, DefaultShadowsocksMethod, "secret", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if network.IsNative() {
		t.Fatal("proxy network reports native")
	}
	ctx := context.Background()
	if _, err := network.Dial(ctx, "tcp", "example.test:80"); err == nil {
		t.Fatal("TCP dial escaped reject upstream")
	}
	if _, err := network.PacketDial(ctx, "udp", "example.test:53"); err == nil {
		t.Fatal("UDP dial escaped reject upstream")
	}
	if _, err := network.Listen(ctx, "tcp", "127.0.0.1:1"); !errors.Is(err, gonnect.ErrUnsupported) {
		t.Fatalf("Listen error = %v", err)
	}
	if _, err := network.ListenTCP(ctx, "tcp", "127.0.0.1:1"); !errors.Is(err, gonnect.ErrUnsupported) {
		t.Fatalf("ListenTCP error = %v", err)
	}
	if _, err := network.DialUDP(ctx, "udp", "", "example.test:53"); err == nil {
		t.Fatalf("DialUDP error = %v", err)
	}
	trap := &shadowsocksLookupTrap{Network: upstream}
	noLookup, err := newShadowsocksNetwork(trap, DefaultShadowsocksAddress, DefaultShadowsocksMethod, "secret", true, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = noLookup.Dial(ctx, "tcp", "target.example:80")
	_, _ = noLookup.PacketDial(ctx, "udp", "target.example:53")
	if trap.called {
		t.Fatal("dial resolved a target hostname outside Shadowsocks")
	}
	if _, err := noLookup.LookupHost(ctx, "target.example"); err == nil || trap.called {
		t.Fatal("target hostname lookup escaped the Shadowsocks client")
	}
}

func TestShadowsocksClientProtocolSwitches(t *testing.T) {
	upstream := &gonnect.RejectNetwork{}
	tcpOnly, err := newShadowsocksNetwork(upstream, DefaultShadowsocksAddress, DefaultShadowsocksMethod, "secret", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcpOnly.PacketDial(context.Background(), "udp", "127.0.0.1:53"); !errors.Is(err, gonnect.ErrUnsupported) {
		t.Fatalf("disabled UDP dial: %v", err)
	}
	udpOnly, err := newShadowsocksNetwork(upstream, DefaultShadowsocksAddress, DefaultShadowsocksMethod, "secret", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := udpOnly.Dial(context.Background(), "tcp", "127.0.0.1:80"); !errors.Is(err, gonnect.ErrUnsupported) {
		t.Fatalf("disabled TCP dial: %v", err)
	}
}

type shadowsocksRejectProxyNetwork struct {
	*gonnect.RejectNetwork
	calls chan string
}

func (n *shadowsocksRejectProxyNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	select {
	case n.calls <- address:
	default:
	}
	return n.RejectNetwork.Dial(ctx, network, address)
}

func TestShadowsocksServerNeverUsesHostWhenProxyRejects(t *testing.T) {
	ctx := context.Background()
	listenNetwork := gonnect.NewLoopbackNetwork()
	defer listenNetwork.Close() //nolint:errcheck
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close() //nolint:errcheck
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	cfg := shadowsocksConfig{address: "127.0.0.1:18393", method: DefaultShadowsocksMethod, password: "reject-proxy-test", tcp: true}
	key, err := makeShadowsocksKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}()
	done := make(chan error, 1)
	proxy := &shadowsocksRejectProxyNetwork{RejectNetwork: &gonnect.RejectNetwork{}, calls: make(chan string, 1)}
	go func() {
		done <- runShadowsocksServerOnce(ctx, w, 0, 1, stop, cfg, key, listenNetwork, proxy, testWorkerSpawner{}, newShadowsocksReplayGuard())
	}()
	client, err := newShadowsocksNetwork(listenNetwork, cfg.address, cfg.method, cfg.password, true, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	for {
		conn, err = client.Dial(ctx, "tcp", target.Addr().String())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("must not reach host"))
	select {
	case address := <-proxy.calls:
		if address != target.Addr().String() {
			t.Fatalf("proxy target = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not use supplied proxy Network")
	}
	_ = conn.Close()
	if err := target.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if leaked, err := target.Accept(); err == nil {
		_ = leaked.Close()
		t.Fatal("server bypassed its rejecting proxy Network")
	}
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

type shadowsocksLookupTrap struct {
	gonnect.Network
	called bool
}

func (n *shadowsocksLookupTrap) LookupHost(context.Context, string) ([]string, error) {
	n.called = true
	return []string{"127.0.0.1"}, nil
}
func TestShadowsocksClientServerTCPAndUDP(t *testing.T) {
	ctx := context.Background()
	network := gonnect.NewLoopbackNetwork()
	defer network.Close() //nolint:errcheck
	tcpEcho, err := network.Listen(ctx, "tcp", "127.0.0.1:18585")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpEcho.Close() //nolint:errcheck
	go func() {
		for {
			conn, err := tcpEcho.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = conn.Close() }(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	udpEcho, err := network.ListenPacket(ctx, "udp", "127.0.0.1:18586")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close() //nolint:errcheck
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := udpEcho.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udpEcho.WriteTo(buf[:n], addr)
		}
	}()
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	cfg := shadowsocksConfig{address: "127.0.0.1:18388", method: DefaultShadowsocksMethod, password: "test-secret", tcp: true, udp: true}
	key, err := makeShadowsocksKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runShadowsocksServerOnce(ctx, w, 0, 1, stop, cfg, key, network, network, testWorkerSpawner{}, newShadowsocksReplayGuard())
	}()
	client, err := newShadowsocksNetwork(network, cfg.address, cfg.method, cfg.password, true, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := client.Dial(ctx, "tcp", "127.0.0.1:18585")
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, err = conn.Write([]byte("hello"))
			if err == nil {
				data := make([]byte, 5)
				_, err = io.ReadFull(conn, data)
				if err == nil && string(data) != "hello" {
					t.Errorf("TCP echo = %q", data)
				}
			}
			_ = conn.Close()
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("TCP exchange failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	packet, err := client.PacketDial(ctx, "udp", "127.0.0.1:18586")
	if err != nil {
		t.Fatal(err)
	}
	_ = packet.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.Write([]byte("datagram")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 100)
	n, err := packet.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:n]) != "datagram" {
		t.Fatalf("UDP echo = %q", data[:n])
	}
	_ = packet.Close()
	udp, err := client.DialUDP(ctx, "udp", "", "127.0.0.1:18586")
	if err != nil {
		t.Fatal(err)
	}
	_ = udp.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := udp.Write([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	n, err = udp.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:n]) != "typed" {
		t.Fatalf("typed UDP echo = %q", data[:n])
	}
	_ = udp.Close()
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
func makeShadowsocksKey(cfg shadowsocksConfig) (*ss.EncryptionKey, error) {
	return ss.NewEncryptionKey(cfg.method, cfg.password)
}

func TestShadowsocksNestedClientsAndServers(t *testing.T) {
	ctx := context.Background()
	network := gonnect.NewLoopbackNetwork()
	defer network.Close() //nolint:errcheck
	echo, err := network.Listen(ctx, "tcp", "127.0.0.1:18587")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close() //nolint:errcheck
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = conn.Close() }(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	udpEcho, err := network.ListenPacket(ctx, "udp", "127.0.0.1:18591")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close() //nolint:errcheck
	go func() {
		buf := make([]byte, 128)
		for {
			n, addr, err := udpEcho.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udpEcho.WriteTo(buf[:n], addr)
		}
	}()
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	configs := []shadowsocksConfig{
		{address: "127.0.0.1:18389", method: "aes-128-gcm", password: "first", tcp: true, udp: true},
		{address: "127.0.0.1:18390", method: "chacha20-ietf-poly1305", password: "second", tcp: true, udp: true},
	}
	stops := []chan struct{}{make(chan struct{}), make(chan struct{})}
	dones := []chan error{make(chan error, 1), make(chan error, 1)}
	for i, cfg := range configs {
		key, err := makeShadowsocksKey(cfg)
		if err != nil {
			t.Fatal(err)
		}
		go func(i int, cfg shadowsocksConfig) {
			dones[i] <- runShadowsocksServerOnce(ctx, w, 0, uint64(i+1), stops[i], cfg, key, network, network, testWorkerSpawner{}, newShadowsocksReplayGuard())
		}(i, cfg)
	}
	first, err := newShadowsocksNetwork(network, configs[0].address, configs[0].method, configs[0].password, true, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newShadowsocksNetwork(first, configs[1].address, configs[1].method, configs[1].password, true, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []*shadowsocksNetwork{first, second} {
		deadline := time.Now().Add(3 * time.Second)
		for {
			conn, err := client.Dial(ctx, "tcp", "127.0.0.1:18587")
			if err == nil {
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				_, err = conn.Write([]byte("nested"))
				if err == nil {
					buf := make([]byte, 6)
					_, err = io.ReadFull(conn, buf)
					if err == nil && string(buf) != "nested" {
						t.Fatalf("nested echo = %q", buf)
					}
				}
				_ = conn.Close()
				if err == nil {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("nested client failed: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	packet, err := second.PacketDial(ctx, "udp", "127.0.0.1:18591")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close() //nolint:errcheck
	_ = packet.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.Write([]byte("nested UDP")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, err := packet.Read(buf)
	if err != nil || string(buf[:n]) != "nested UDP" {
		t.Fatalf("nested UDP reply: data=%q err=%v", buf[:n], err)
	}
	for i := range stops {
		close(stops[i])
	}
	for i := range dones {
		select {
		case err := <-dones[i]:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("nested server did not stop")
		}
	}
}

type shadowsocksTestSalt struct{}

func (shadowsocksTestSalt) GetSalt(salt []byte) error {
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	return nil
}
func TestShadowsocksServerRejectsUDPReplay(t *testing.T) {
	ctx := context.Background()
	network := gonnect.NewLoopbackNetwork()
	defer network.Close() //nolint:errcheck
	target, err := network.ListenPacket(ctx, "udp", "127.0.0.1:18588")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close() //nolint:errcheck
	received := make(chan string, 2)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, _, err := target.ReadFrom(buf)
			if err != nil {
				return
			}
			received <- string(buf[:n])
		}
	}()
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	cfg := shadowsocksConfig{address: "127.0.0.1:18391", method: "aes-256-gcm", password: "replay-key", udp: true}
	key, err := makeShadowsocksKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runShadowsocksServerOnce(ctx, w, 0, 1, stop, cfg, key, network, network, testWorkerSpawner{}, newShadowsocksReplayGuard())
	}()
	addr := socks.ParseAddr("127.0.0.1:18588")
	plaintext := append(append([]byte(nil), addr...), []byte("once")...)
	encrypted, err := ss.PackSalt(make([]byte, key.SaltSize()+len(plaintext)+key.TagSize()), plaintext, key, shadowsocksTestSalt{})
	if err != nil {
		t.Fatal(err)
	}
	var conn gonnect.PacketConn
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err = network.PacketDial(ctx, "udp", cfg.address)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer conn.Close() //nolint:errcheck
	for {
		if _, err := conn.Write(encrypted); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("UDP server did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case got := <-received:
		if got != "once" {
			t.Fatalf("first packet = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first packet not relayed")
	}
	if _, err := conn.Write(encrypted); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		t.Fatalf("replayed packet reached target: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestShadowsocksServerRejectsTCPReplay(t *testing.T) {
	ctx := context.Background()
	network := gonnect.NewLoopbackNetwork()
	defer network.Close() //nolint:errcheck
	target, err := network.Listen(ctx, "tcp", "127.0.0.1:18589")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close() //nolint:errcheck
	accepted := make(chan struct{}, 2)
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	cfg := shadowsocksConfig{address: "127.0.0.1:18392", method: "aes-256-gcm", password: "replay-key", tcp: true}
	key, err := makeShadowsocksKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runShadowsocksServerOnce(ctx, w, 0, 1, stop, cfg, key, network, network, testWorkerSpawner{}, newShadowsocksReplayGuard())
	}()
	var output bytes.Buffer
	writer := ss.NewWriter(&output, key)
	writer.SetSaltGenerator(shadowsocksTestSalt{})
	request := append(append([]byte(nil), socks.ParseAddr("127.0.0.1:18589")...), byte('x'))
	if _, err := writer.Write(request); err != nil {
		t.Fatal(err)
	}
	ciphertext := append([]byte(nil), output.Bytes()...)
	send := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			conn, err := network.Dial(ctx, "tcp", cfg.address)
			if err == nil {
				_, err = conn.Write(ciphertext)
				_ = conn.Close()
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	send()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("first TCP request not relayed")
	}
	send()
	select {
	case <-accepted:
		t.Fatal("replayed TCP request reached target")
	case <-time.After(100 * time.Millisecond):
	}
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
