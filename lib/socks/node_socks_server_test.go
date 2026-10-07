package socksnodes

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
)

func TestSocksgoEmptyDefaultHostPreservesWildcardFamily(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, network, address, want string
	}{
		{name: "UDP IPv4", network: "udp4", address: "0.0.0.0:25120", want: "0.0.0.0:25120"},
		{name: "UDP IPv6", network: "udp6", address: "[::]:25120", want: "[::]:25120"},
		{name: "TCP IPv4", network: "tcp4", address: "0.0.0.0:25120", want: "0.0.0.0:25120"},
		{name: "TCP IPv6", network: "tcp6", address: "[::]:25120", want: "[::]:25120"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			before := protocol.AddrFromHostPort(test.address, test.network)
			after := before.WithDefaultHost("")
			if after.Type != before.Type {
				t.Fatalf("address type = %v, want preserved type %v", after.Type, before.Type)
			}
			if got := after.Network(); got != test.network {
				t.Fatalf("network = %q, want %q", got, test.network)
			}
			if got := after.ToHostPort(); got != test.want {
				t.Fatalf("address = %q, want %q", got, test.want)
			}
		})
	}
}

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

func TestAcceptSocksConnectionCancellationClosesBlockedHandshake(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() {
		if err := clientConn.Close(); err != nil {
			t.Errorf("close client connection: %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- acceptSocksConnection(ctx, &socksgo.Server{
			HandshakeTimeout: time.Minute,
		}, serverConn)
	}()

	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled SOCKS handshake returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled SOCKS handshake remained blocked")
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
				"username":       "bob",
				"password":       "fallback",
				"passwordSecret": "bob-password",
			},
		},
	})
	if !ok {
		t.Fatal("parseSocksServerUsers ok = false, want true")
	}
	if len(users) != 1 {
		t.Fatalf("users = %#v, want one user", users)
	}
	if users[0] != (socksServerUser{id: "user-2", username: "bob", password: "fallback", passwordSecret: "bob-password"}) {
		t.Fatalf("users[0] = %#v, want bob", users[0])
	}
}

func TestSocksServerConfigParsingAndValidationEdges(t *testing.T) {
	for _, config := range []socksServerConfig{
		{socksPort: -1}, {socksPort: 65536}, {wsPort: -1}, {wsPort: 65536},
		{socksHost: "127.0.0.1", socksPort: 1080, wsHost: "127.0.0.1", wsPort: 1080},
	} {
		if err := config.validate(); err == nil {
			t.Fatalf("validate accepted %+v", config)
		}
	}
	if err := (socksServerConfig{}).validate(); err != nil {
		t.Fatalf("disabled config error = %v", err)
	}
	if (socksServerConfig{}).hasListener() || !(socksServerConfig{socksPort: 1}).hasListener() {
		t.Fatal("hasListener returned an incorrect result")
	}

	current := defaultSocksServerConfig()
	fields := []struct {
		id    string
		value any
	}{
		{id: "socksHost", value: " 127.0.0.1 "}, {id: "socksPort", value: 1081},
		{id: "wsHost", value: " localhost "}, {id: "wsPort", value: 8080},
		{id: "wsPath", value: " socket "},
	}
	for _, field := range fields {
		next, ok := socksServerConfigWithFieldValue(current, formular.FieldValidateMessage{
			Field: formular.FieldRef{FieldID: field.id}, Value: field.value,
		})
		if !ok {
			t.Fatalf("field %q was not recognized", field.id)
		}
		current = next
	}
	if _, ok := socksServerConfigWithFieldValue(current, formular.FieldValidateMessage{Field: formular.FieldRef{FieldID: "unknown"}}); ok {
		t.Fatal("unknown field was recognized")
	}
	if current.wsPath != "/socket" || current.socksHost != "127.0.0.1" || current.wsHost != "localhost" {
		t.Fatalf("field values = %+v", current)
	}
	if got := socksServerConfigLabel(socksServerConfig{}); got != "disabled" {
		t.Fatalf("disabled label = %q", got)
	}
	if got := socksServerConfigLabel(socksServerConfig{wsPort: 80}); got != "ws "+net.JoinHostPort(DefaultSocksServerHost, "80") {
		t.Fatalf("WebSocket label = %q", got)
	}

	rawUsers := []formular.ArrayElementValue{
		{ID: "same", Template: "user", Values: map[string]any{"username": " alice ", "password": "one"}},
		{ID: "same", Template: "user", Values: map[string]any{"username": "bob", "passwordSecret": " secret "}},
		{Template: "user", Values: map[string]any{"username": ""}},
		{Template: "ignored", Values: map[string]any{"username": "ignored"}},
	}
	users, ok := parseSocksServerUsers(rawUsers)
	if !ok || len(users) != 2 || users[0].id != "same" || users[1].id == "same" {
		t.Fatalf("parsed users = %#v, %t", users, ok)
	}
	if _, ok := parseSocksServerUsers("invalid"); ok {
		t.Fatal("invalid users value was accepted")
	}

	config := configer.NewMemory(map[string]any{
		"users": []any{
			map[string]any{"username": " alice ", "password": "one"},
			"invalid",
			map[string]any{"id": "bob", "username": "bob", "passwordSecret": " secret "},
		},
		"commands": map[string]bool{"cmdConnect": false},
	})
	users, ok = readSocksServerUsers(config)
	if !ok || len(users) != 2 || users[0].id != "user-1" {
		t.Fatalf("read users = %#v, %t", users, ok)
	}
	commands, ok := readSocksServerCommands(config)
	if !ok || commands[protocol.CmdConnect] {
		t.Fatalf("read commands = %#v, %t", commands, ok)
	}
	if got := socksAuthSummary(protocol.AuthInfo{Code: protocol.PassAuthCode, Info: map[string]any{"user": "alice"}}); got != "user:alice" {
		t.Fatalf("user auth summary = %q", got)
	}
	if got := socksAuthSummary(protocol.AuthInfo{Code: protocol.PassAuthCode}); got != "userpass" {
		t.Fatalf("generic auth summary = %q", got)
	}
	if got := socksAuthSummary(protocol.AuthInfo{Code: 99}); got != "99" {
		t.Fatalf("unknown auth summary = %q", got)
	}
}

func TestSocksServerConfigAlternateStorageShapes(t *testing.T) {
	rawUsers := []any{
		map[string]any{"id": "alice", "template": "user", "values": map[string]any{"username": " alice ", "password": "one"}},
		"invalid",
		map[string]any{"id": "ignored", "template": "other", "values": map[string]any{"username": "ignored"}},
	}
	users, ok := parseSocksServerUsers(rawUsers)
	if !ok || len(users) != 1 || users[0].username != "alice" {
		t.Fatalf("parsed []any users = %#v, %t", users, ok)
	}

	config := configer.NewMemory(map[string]any{
		"users": []map[string]any{
			{"username": " alice ", "password": "one"},
			{"username": "   "},
		},
		"commands": map[string]any{"cmdConnect": false, "cmdBind": "invalid"},
	})
	users, ok = readSocksServerUsers(config)
	if !ok || len(users) != 1 || users[0].id != "user-1" {
		t.Fatalf("read []map users = %#v, %t", users, ok)
	}
	commands, ok := readSocksServerCommands(config)
	if !ok || commands[protocol.CmdConnect] || !commands[protocol.CmdBind] {
		t.Fatalf("read map[string]any commands = %#v, %t", commands, ok)
	}
	for _, raw := range []any{"invalid", []any{}} {
		cfg := configer.NewMemory(map[string]any{"commands": raw})
		if _, ok := readSocksServerCommands(cfg); ok {
			t.Fatalf("accepted command config %#v", raw)
		}
	}
	if normalizeSocksServerPath(" ") != "" || normalizeSocksServerPath("socket") != "/socket" || normalizeSocksServerPath(" /socket ") != "/socket" {
		t.Fatal("normalizeSocksServerPath returned an unexpected value")
	}
	if hostOrDefault(" ") != DefaultSocksServerHost || hostOrDefault(" localhost ") != "localhost" {
		t.Fatal("hostOrDefault returned an unexpected value")
	}
	if validPort(-1) != 0 || validPort(65536) != 0 || validPort(65535) != 65535 {
		t.Fatal("validPort returned an unexpected value")
	}
}

func TestSocksServerUserMenuOffersPasswordSecretSelector(t *testing.T) {
	template := socksServerUserTemplate()
	if len(template.Items) != 3 {
		t.Fatalf("user template items = %d, want 3", len(template.Items))
	}
	item := template.Items[2]
	if item.ID != "passwordSecret" || item.Field == nil || item.Selector != nodekit.SecretNameSelector {
		t.Fatalf("password secret menu item = %#v, want secret selector", item)
	}
}

func TestResolveSocksServerPasswordConfigKeepsSecretsRuntimeOnly(t *testing.T) {
	persisted := socksServerConfig{users: []socksServerUser{
		{id: "alice", username: "alice", password: "inline-alice", passwordSecret: "alice-secret"},
		{id: "bob", username: "bob", password: "inline-bob", passwordSecret: "missing-secret"},
	}}
	runtime := resolveSocksServerPasswordConfig(persisted, func(name string) (string, bool) {
		if name == "alice-secret" {
			return "secret-alice", true
		}
		return "", false
	})

	if runtime.users[0].password != "secret-alice" {
		t.Fatalf("resolved Alice password = %q, want secret value", runtime.users[0].password)
	}
	if runtime.users[1].password != "inline-bob" {
		t.Fatalf("resolved Bob password = %q, want inline fallback", runtime.users[1].password)
	}
	if persisted.users[0].password != "inline-alice" {
		t.Fatalf("persisted Alice password = %q, want unchanged inline value", persisted.users[0].password)
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
