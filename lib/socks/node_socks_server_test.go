package socksnodes

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
)

func TestBuildSocksGoServerRoutesClientAndProxyNetworks(t *testing.T) {
	listenCalls := make([]string, 0, 1)
	proxyCalls := make([]string, 0, 5)
	listen := &socksRouteNetwork{name: "listen", calls: &listenCalls}
	proxy := &socksRouteNetwork{name: "proxy", calls: &proxyCalls}

	server := buildSocksGoServer(logging.NewDebugLogger(nil, logging.DebugLogSource{Name: "test"}), socksServerWorkerConfig{
		config:        defaultSocksServerConfig(),
		listenNetwork: listen,
		proxyNetwork:  proxy,
	})

	ctx := context.Background()
	ctrl := &socksRouteConn{local: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1080}}

	if _, err := server.AssocListener(ctx, ctrl); err != nil {
		t.Fatalf("AssocListener error = %v, want nil", err)
	}
	if _, err := server.Dialer(ctx, "tcp", "example.test:80"); err != nil {
		t.Fatalf("Dialer error = %v, want nil", err)
	}
	if _, err := server.PacketDialer(ctx, "udp", "example.test:53"); err != nil {
		t.Fatalf("PacketDialer error = %v, want nil", err)
	}
	if _, err := server.Listener(ctx, "tcp", "127.0.0.1:0"); err != nil {
		t.Fatalf("Listener error = %v, want nil", err)
	}
	if _, err := server.PacketListener(ctx, "udp", "127.0.0.1:0"); err != nil {
		t.Fatalf("PacketListener error = %v, want nil", err)
	}
	if _, err := server.Resolver.LookupHost(ctx, "example.test"); err != nil {
		t.Fatalf("Resolver.LookupHost error = %v, want nil", err)
	}

	testkit.AssertStringSlice(t, listenCalls, []string{"listen.ListenPacket udp 127.0.0.1:0"})
	testkit.AssertStringSlice(t, proxyCalls, []string{
		"proxy.Dial tcp example.test:80",
		"proxy.PacketDial udp example.test:53",
		"proxy.Listen tcp 127.0.0.1:0",
		"proxy.ListenPacket udp 127.0.0.1:0",
		"proxy.LookupHost example.test",
	})
}

func TestSocksUDPAssocUsesAssignedRelayPort(t *testing.T) {
	listenCalls := make([]string, 0, 1)
	listen := &socksRouteNetwork{name: "listen", calls: &listenCalls}
	ctrl := &socksRouteConn{local: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1080}}

	if _, err := listenSocksUDPAssoc(context.Background(), listen, ctrl); err != nil {
		t.Fatalf("listenSocksUDPAssoc error = %v, want nil", err)
	}

	// Standard SOCKS5 UDP ASSOCIATE returns the server-assigned relay port
	// in the reply; the request has no field for a client-chosen relay port.
	testkit.AssertStringSlice(t, listenCalls, []string{
		"listen.ListenPacket udp 127.0.0.1:0",
	})
}

func TestPrepareSocksHandshakeDeadlineSetsInitialDeadline(t *testing.T) {
	server := &socksgo.Server{HandshakeTimeout: time.Second}
	conn := &socksRouteConn{}

	before := time.Now()
	prepareSocksHandshakeDeadline(server, conn)
	after := time.Now()

	if conn.deadline.Before(before) || conn.deadline.After(after.Add(server.HandshakeTimeout)) {
		t.Fatalf("deadline = %v, want within handshake timeout after %v", conn.deadline, before)
	}
}

func TestSocksServerAuthAllowsDuplicateUsernamePasswords(t *testing.T) {
	auth := socksServerAuth([]socksServerUser{
		{username: "alice", password: "one"},
		{username: "alice", password: "two"},
		{username: "bob", password: "one"},
	})
	handler, ok := auth.Get(protocol.PassAuthCode).(*protocol.PassAuthHandler)
	if !ok {
		t.Fatalf("auth handler = %T, want *protocol.PassAuthHandler", auth.Get(protocol.PassAuthCode))
	}

	for _, tc := range []struct {
		user string
		pass string
		want bool
	}{
		{user: "alice", pass: "one", want: true},
		{user: "alice", pass: "two", want: true},
		{user: "alice", pass: "wrong", want: false},
		{user: "bob", pass: "one", want: true},
		{user: "bob", pass: "two", want: false},
		{user: "carol", pass: "one", want: false},
	} {
		if got := handler.VerifyFn(tc.user, tc.pass); got != tc.want {
			t.Fatalf("VerifyFn(%q, %q) = %v, want %v", tc.user, tc.pass, got, tc.want)
		}
	}
}

func TestParseSocksServerUsersKeepsAppliedRemoval(t *testing.T) {
	users, ok := parseSocksServerUsers([]formular.ArrayElementValue{
		{
			ID:       "user-2",
			Template: "user",
			Values: map[string]any{
				"username": "bob",
				"password": "secret",
			},
		},
	})
	if !ok {
		t.Fatal("parseSocksServerUsers ok = false, want true")
	}
	if len(users) != 1 {
		t.Fatalf("users = %#v, want one user", users)
	}
	if users[0] != (socksServerUser{id: "user-2", username: "bob", password: "secret"}) {
		t.Fatalf("users[0] = %#v, want bob", users[0])
	}
}

func TestSocksServerEnsureWorkerStartedKeepsRunningWorker(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	worker, err := nodekit.NewRestartableWorker(testWorkerSpawner{}, func(stop <-chan struct{}) {
		close(started)
		<-stop
		close(stopped)
	}, "test socks worker")
	if err != nil {
		t.Fatalf("NewRestartableWorker error = %v, want nil", err)
	}
	<-started
	t.Cleanup(func() {
		select {
		case <-stopped:
		default:
			worker.Stop()
			<-stopped
		}
	})

	node := &socksServerNode{worker: worker, restartID: 7}
	node.ensureWorkerStarted("ready")

	if node.worker != worker {
		t.Fatal("ensureWorkerStarted replaced a running worker")
	}
	if node.restartID != 7 {
		t.Fatalf("restartID = %d, want 7", node.restartID)
	}
	select {
	case <-stopped:
		t.Fatal("ensureWorkerStarted stopped a running worker")
	default:
	}
}

func TestSocksServerAcceptNetworkPayloadReplacesStaleCurrentSameSource(t *testing.T) {
	// A closed same-source current wrapper should not make the SOCKS server
	// discard a fresh re-emit from Select or another wrapper-preserving node.
	base := gonnect.NewLoopbackNetwork()
	current := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	closeTestNetwork(current)
	replacement := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	defer closeTestNetwork(replacement)

	next, changed := acceptSocksServerNetworkPayload(nil, 0, pasta.Event{
		Link:    1,
		Payload: NetworkPayload{Network: replacement},
	}, current)
	if !changed {
		t.Fatal("acceptSocksServerNetworkPayload() changed = false, want true for stale current")
	}
	if !SameNetworkInstance(next, replacement) {
		t.Fatal("acceptSocksServerNetworkPayload() did not return replacement")
	}
	if !NetworkIsReady(replacement) {
		t.Fatal("replacement network is not ready")
	}
}

type socksRouteNetwork struct {
	name  string
	calls *[]string
}

var _ NetworkCloser = (*socksRouteNetwork)(nil)

func (n *socksRouteNetwork) record(call string) {
	*n.calls = append(*n.calls, n.name+"."+call)
}

func (n *socksRouteNetwork) Close() error { return nil }
func (n *socksRouteNetwork) IsNative() bool {
	return false
}
func (n *socksRouteNetwork) Dial(_ context.Context, network, address string) (net.Conn, error) {
	n.record("Dial " + network + " " + address)
	return &socksRouteConn{}, nil
}
func (n *socksRouteNetwork) Listen(_ context.Context, network, address string) (net.Listener, error) {
	n.record("Listen " + network + " " + address)
	return &socksRouteListener{}, nil
}
func (n *socksRouteNetwork) PacketDial(_ context.Context, network, address string) (gonnect.PacketConn, error) {
	n.record("PacketDial " + network + " " + address)
	return &socksRoutePacketConn{}, nil
}
func (n *socksRouteNetwork) ListenPacket(_ context.Context, network, address string) (gonnect.PacketConn, error) {
	n.record("ListenPacket " + network + " " + address)
	return &socksRoutePacketConn{}, nil
}
func (n *socksRouteNetwork) DialTCP(context.Context, string, string, string) (gonnect.TCPConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) ListenTCP(context.Context, string, string) (gonnect.TCPListener, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) DialUDP(context.Context, string, string, string) (gonnect.UDPConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) ListenUDP(context.Context, string, string) (gonnect.UDPConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) ListenPacketConfig(context.Context, *gonnect.ListenConfig, string, string) (gonnect.PacketConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) ListenUDPConfig(context.Context, *gonnect.ListenConfig, string, string) (gonnect.UDPConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) ListenMulticastUDP(context.Context, string, string, gonnect.MulticastOptions) (gonnect.MulticastPacketConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) InterfaceAddrs() ([]net.Addr, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) InterfaceMulticastAddrs() ([]net.Addr, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) Interfaces() ([]gonnect.NetworkInterface, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) InterfacesByIndex(int) ([]gonnect.NetworkInterface, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) InterfacesByName(string) ([]gonnect.NetworkInterface, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *socksRouteNetwork) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return nil, nil
}
func (n *socksRouteNetwork) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, nil
}
func (n *socksRouteNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, nil
}
func (n *socksRouteNetwork) LookupHost(_ context.Context, host string) ([]string, error) {
	n.record("LookupHost " + host)
	return nil, nil
}
func (n *socksRouteNetwork) LookupAddr(context.Context, string) ([]string, error) { return nil, nil }
func (n *socksRouteNetwork) LookupCNAME(context.Context, string) (string, error)  { return "", nil }
func (n *socksRouteNetwork) LookupPort(context.Context, string, string) (int, error) {
	return 0, nil
}
func (n *socksRouteNetwork) LookupNS(context.Context, string) ([]*net.NS, error) {
	return nil, nil
}
func (n *socksRouteNetwork) LookupMX(context.Context, string) ([]*net.MX, error) {
	return nil, nil
}
func (n *socksRouteNetwork) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, nil
}
func (n *socksRouteNetwork) LookupTXT(context.Context, string) ([]string, error) { return nil, nil }

type socksRouteConn struct {
	local    net.Addr
	remote   net.Addr
	deadline time.Time
}

func (c *socksRouteConn) Read([]byte) (int, error)         { return 0, gonnect.ErrUnsupported }
func (c *socksRouteConn) Write([]byte) (int, error)        { return 0, gonnect.ErrUnsupported }
func (c *socksRouteConn) Close() error                     { return nil }
func (c *socksRouteConn) LocalAddr() net.Addr              { return c.local }
func (c *socksRouteConn) RemoteAddr() net.Addr             { return c.remote }
func (c *socksRouteConn) SetDeadline(t time.Time) error    { c.deadline = t; return nil }
func (c *socksRouteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *socksRouteConn) SetWriteDeadline(time.Time) error { return nil }

type socksRoutePacketConn struct {
	socksRouteConn
}

func (c *socksRoutePacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, gonnect.ErrUnsupported
}
func (c *socksRoutePacketConn) WriteTo([]byte, net.Addr) (int, error) {
	return 0, gonnect.ErrUnsupported
}

type socksRouteListener struct{}

func (l *socksRouteListener) Accept() (net.Conn, error) { return nil, gonnect.ErrUnsupported }
func (l *socksRouteListener) Close() error              { return nil }
func (l *socksRouteListener) Addr() net.Addr            { return &net.TCPAddr{} }
