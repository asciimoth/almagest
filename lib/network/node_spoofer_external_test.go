package networknodes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestSpooferNodeValidationReportsStatus(t *testing.T) {
	w := newSpooferNodeTestWorkspace(t)
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	spooferID, err := w.AddNodeByClass(NodeTypeSpoofer, "spoofer")
	if err != nil {
		t.Fatalf("AddNodeByClass Spoofer: %v", err)
	}
	w.SubscribeNodeMenu(spooferID, sub)

	w.SendNodeFormularMsg(spooferID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(spooferID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "ttl"},
		Value:       300,
	})
	testkit.AssertMenuItemStatus(t, state, spooferID, "ttl", formular.StatusError)

	w.SendNodeFormularMsg(spooferID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(spooferID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "ttl"},
		Value:       64,
	})
	testkit.AssertMenuItemStatus(t, state, spooferID, "ttl", formular.StatusOK)

	w.SendNodeFormularMsg(spooferID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(spooferID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "congestionControlAlg"},
		Value:       "",
	})
	testkit.AssertMenuItemStatus(t, state, spooferID, "congestionControlAlg", formular.StatusOK)
}

func TestSpooferPrepareTCPNilSpawnerReturnsError(t *testing.T) {
	handler, err := prepareSpooferTCP(
		context.Background(), nil, NewMockLoopbackNetwork(false), spooferForwardTestEndpoint(), nil,
	)
	if handler != nil {
		_ = handler.Close()
		t.Fatal("prepared handler is non-nil with no worker spawner")
	}
	if err == nil || !strings.Contains(err.Error(), "spawner") {
		t.Fatalf("prepare error = %v, want worker spawner error", err)
	}
}

func TestSpooferForwardUDPNilSpawnerClosesConn(t *testing.T) {
	conn := &spooferForwardTestPacketConn{}

	forwardSpooferUDP(context.Background(), nil, NewMockLoopbackNetwork(false), conn, spooferForwardTestEndpoint(), nil)

	if !conn.closed {
		t.Fatal("packet conn was not closed")
	}
}

func TestSpooferForwardUDPOpensUnconnectedSocket(t *testing.T) {
	network := &spooferForwardRecordingNetwork{LoopbackNetwork: gonnect.NewLoopbackNetwork()}
	t.Cleanup(func() { _ = network.Close() })
	conn := &spooferForwardTestPacketConn{}

	forwardSpooferUDP(
		context.Background(), spooferForwardStopSpawner{}, network, conn, spooferForwardTestEndpoint(), nil,
	)

	if network.listenPacketCalls != 1 {
		t.Fatalf("ListenPacket calls = %d, want 1", network.listenPacketCalls)
	}
	if network.packetDialCalls != 0 {
		t.Fatalf("PacketDial calls = %d, want 0", network.packetDialCalls)
	}
	if network.listenPacketNetwork != "udp4" || network.listenPacketAddress != "0.0.0.0:0" {
		t.Fatalf(
			"ListenPacket(%q, %q), want udp4 0.0.0.0:0",
			network.listenPacketNetwork, network.listenPacketAddress,
		)
	}
	if !conn.closed {
		t.Fatal("packet conn was not closed after spawner failure")
	}
}

func TestSpooferForwardUDPAcceptsReplyFromDifferentSourcePort(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen target UDP: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	reply, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen reply UDP: %v", err)
	}
	t.Cleanup(func() { _ = reply.Close() })
	if target.LocalAddr().(*net.UDPAddr).Port == reply.LocalAddr().(*net.UDPAddr).Port {
		t.Fatal("target and reply UDP ports unexpectedly match")
	}

	client := newSpooferForwardScriptedPacketConn([]byte("request"))
	spawner := &spooferForwardGoSpawner{}
	network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	endpoint := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{127, 0, 0, 1}),
		LocalPort:    uint16(target.LocalAddr().(*net.UDPAddr).Port),
	}

	serverDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, source, err := target.ReadFromUDP(buf)
		if err == nil && string(buf[:n]) != "request" {
			err = fmt.Errorf("request payload = %q", buf[:n])
		}
		if err == nil {
			_, err = reply.WriteToUDP([]byte("response"), source)
		}
		serverDone <- err
	}()

	forwardSpooferUDPWithIdleTimeout(
		context.Background(), spawner, network, client, endpoint, nil, 2*time.Second,
	)
	t.Cleanup(func() {
		_ = client.Close()
		spawner.wait(t)
	})

	select {
	case got := <-client.responses:
		if string(got) != "response" {
			t.Fatalf("response payload = %q, want response", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Spoofer did not forward a UDP reply from a different source port")
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("UDP server: %v", err)
	}
}

func TestSpooferForwardUDPClosesIdleFlow(t *testing.T) {
	client := newSpooferForwardBlockingPacketConn()
	upstream := newSpooferForwardBlockingPacketConn()
	network := &spooferForwardRecordingNetwork{
		LoopbackNetwork:  gonnect.NewLoopbackNetwork(),
		listenPacketConn: upstream,
	}
	t.Cleanup(func() { _ = network.Close() })
	spawner := &spooferForwardGoSpawner{}

	forwardSpooferUDPWithIdleTimeout(
		context.Background(), spawner, network, client, spooferForwardTestEndpoint(), nil, 20*time.Millisecond,
	)

	select {
	case <-client.closedCh:
	case <-time.After(time.Second):
		t.Fatal("idle intercepted UDP connection was not closed")
	}
	select {
	case <-upstream.closedCh:
	case <-time.After(time.Second):
		t.Fatal("idle upstream UDP connection was not closed")
	}
	spawner.wait(t)
}

func TestSpooferPrepareTCPDialsConfiguredTarget(t *testing.T) {
	network := &spooferForwardRecordingNetwork{LoopbackNetwork: gonnect.NewLoopbackNetwork()}
	t.Cleanup(func() { _ = network.Close() })

	handler, err := prepareSpooferTCP(
		context.Background(), spooferForwardStopSpawner{}, network, spooferForwardTestEndpoint(), nil,
	)
	if err != nil {
		t.Fatalf("prepareSpooferTCP: %v", err)
	}
	if network.dialCalls != 1 {
		t.Fatalf("Dial calls = %d, want 1", network.dialCalls)
	}
	if network.dialNetwork != "tcp" || network.dialAddress != "127.0.0.1:80" {
		t.Fatalf("Dial(%q, %q), want tcp 127.0.0.1:80", network.dialNetwork, network.dialAddress)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("close prepared handler: %v", err)
	}
	assertSpooferPipeClosed(t, network.dialPeer)
}

func TestPreparedSpooferTCPClosesBothSidesWhenWorkerStartFails(t *testing.T) {
	network := &spooferForwardRecordingNetwork{LoopbackNetwork: gonnect.NewLoopbackNetwork()}
	t.Cleanup(func() { _ = network.Close() })
	reported := make(chan error, 1)
	handler, err := prepareSpooferTCP(
		context.Background(), spooferForwardStopSpawner{}, network, spooferForwardTestEndpoint(), func(err error) {
			reported <- err
		},
	)
	if err != nil {
		t.Fatalf("prepareSpooferTCP: %v", err)
	}
	client, clientPeer := net.Pipe()
	defer func() { _ = clientPeer.Close() }()
	handler.HandleTCP(client)

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "upstream worker") {
			t.Fatalf("reported error = %v, want upstream worker failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker start failure was not reported")
	}
	assertSpooferPipeClosed(t, clientPeer)
	assertSpooferPipeClosed(t, network.dialPeer)
}

func TestSpooferForwardDiagnosticsIncludeTargetFamilyAndNetwork(t *testing.T) {
	logFactory := logging.NewDebugLogFactory(16)
	network := NewMockLoopbackNetwork(false)
	node := &spooferNode{}
	node.Logger = logFactory.NodeLogger(17, NodeTypeSpoofer)
	err := errors.New("controlled dial failure")
	for range spooferForwardLogBurst + 2 {
		node.reportSpooferForwardError(
			context.Background(), "tcp", spooferForwardTestIPv6Endpoint(), 42, network, err,
		)
	}
	entries := logFactory.Drain()
	if len(entries) != spooferForwardLogBurst {
		t.Fatalf("diagnostic count = %d, want rate-limited burst %d", len(entries), spooferForwardLogBurst)
	}
	if entries[0].Level != logging.DebugLogLevelDebug {
		t.Fatalf("diagnostic level = %q, want %q", entries[0].Level, logging.DebugLogLevelDebug)
	}
	message := entries[0].Message
	for _, want := range []string{
		"Spoofer tcp forwarding failed",
		"target=[2001:db8::80]:443",
		"family=ipv6",
		"networkNode=42",
		"network=*networknodes.MockLoopbackNetwork",
		"controlled dial failure",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic %q does not contain %q", message, want)
		}
	}
}

func TestSpooferForwardDiagnosticsIgnoreCanceledOldRuntime(t *testing.T) {
	logFactory := logging.NewDebugLogFactory(4)
	node := &spooferNode{}
	node.Logger = logFactory.NodeLogger(17, NodeTypeSpoofer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	node.reportSpooferForwardError(
		ctx, "tcp", spooferForwardTestEndpoint(), 42, NewMockLoopbackNetwork(false),
		fmt.Errorf("prepare: %w", context.Canceled),
	)
	if entries := logFactory.Drain(); len(entries) != 0 {
		t.Fatalf("canceled old runtime produced diagnostics: %#v", entries)
	}
}

func assertSpooferPipeClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if conn == nil {
		t.Fatal("pipe peer is nil")
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read([]byte{0}); err != io.EOF {
		t.Fatalf("peer read error = %v, want EOF", err)
	}
}

func newSpooferNodeTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	runtime := spooferTestRuntime{ctx: context.Background(), sys: &sysnetdebug.System{}}
	if err := w.AddNodeClass(SpooferClass{Runtime: runtime}); err != nil {
		t.Fatalf("AddNodeClass Spoofer: %v", err)
	}
	return w
}

type spooferTestRuntime struct {
	ctx context.Context
	sys sysnet.System
}

func (r spooferTestRuntime) Context() context.Context { return r.ctx }
func (spooferTestRuntime) Pool() bufpool.Pool         { return nil }
func (r spooferTestRuntime) System() sysnet.System    { return r.sys }
func (spooferTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (spooferTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (spooferTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (spooferTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (spooferTestRuntime) WorkerSpawner(uint64) WorkerSpawner {
	return nil
}
func (spooferTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

type spooferForwardStopSpawner struct{}

func (spooferForwardStopSpawner) Spawn(func(), string) (uint64, error) {
	return 0, errors.New("stop spoofer test")
}

func (spooferForwardStopSpawner) SpawnWg(func(), *sync.WaitGroup, string) (uint64, error) {
	return 0, errors.New("stop spoofer test")
}

type spooferForwardRecordingNetwork struct {
	*gonnect.LoopbackNetwork

	dialCalls           int
	dialNetwork         string
	dialAddress         string
	dialPeer            net.Conn
	packetDialCalls     int
	packetDialNetwork   string
	packetDialAddress   string
	listenPacketCalls   int
	listenPacketNetwork string
	listenPacketAddress string
	listenPacketConn    gonnect.PacketConn
}

func (n *spooferForwardRecordingNetwork) Dial(_ context.Context, network, address string) (net.Conn, error) {
	n.dialCalls++
	n.dialNetwork = network
	n.dialAddress = address
	conn, peer := net.Pipe()
	n.dialPeer = peer
	return conn, nil
}

func (n *spooferForwardRecordingNetwork) PacketDial(_ context.Context, network, address string) (gonnect.PacketConn, error) {
	n.packetDialCalls++
	n.packetDialNetwork = network
	n.packetDialAddress = address
	return &spooferForwardTestPacketConn{}, nil
}

func (n *spooferForwardRecordingNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	n.listenPacketCalls++
	n.listenPacketNetwork = network
	n.listenPacketAddress = address
	if n.listenPacketConn != nil {
		return n.listenPacketConn, nil
	}
	return &spooferForwardTestPacketConn{}, nil
}

type spooferForwardGoSpawner struct {
	wg sync.WaitGroup
}

func (s *spooferForwardGoSpawner) Spawn(fn func(), _ string) (uint64, error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
	return 1, nil
}

func (s *spooferForwardGoSpawner) SpawnWg(fn func(), wg *sync.WaitGroup, name string) (uint64, error) {
	wg.Add(1)
	return s.Spawn(func() {
		defer wg.Done()
		fn()
	}, name)
}

func (s *spooferForwardGoSpawner) wait(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Spoofer UDP workers did not stop")
	}
}

type spooferForwardScriptedPacketConn struct {
	requests  chan []byte
	responses chan []byte
	closedCh  chan struct{}
	closeOnce sync.Once
}

func newSpooferForwardScriptedPacketConn(request []byte) *spooferForwardScriptedPacketConn {
	c := &spooferForwardScriptedPacketConn{
		requests: make(chan []byte, 1), responses: make(chan []byte, 1), closedCh: make(chan struct{}),
	}
	c.requests <- append([]byte(nil), request...)
	return c
}

func (c *spooferForwardScriptedPacketConn) Read(p []byte) (int, error) {
	n, _, err := c.ReadFrom(p)
	return n, err
}

func (c *spooferForwardScriptedPacketConn) Write(p []byte) (int, error) {
	select {
	case c.responses <- append([]byte(nil), p...):
		return len(p), nil
	case <-c.closedCh:
		return 0, net.ErrClosed
	}
}

func (c *spooferForwardScriptedPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closedCh) })
	return nil
}

func (*spooferForwardScriptedPacketConn) LocalAddr() net.Addr  { return nil }
func (*spooferForwardScriptedPacketConn) RemoteAddr() net.Addr { return nil }
func (*spooferForwardScriptedPacketConn) SetDeadline(time.Time) error {
	return nil
}
func (*spooferForwardScriptedPacketConn) SetReadDeadline(time.Time) error {
	return nil
}
func (*spooferForwardScriptedPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *spooferForwardScriptedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case request := <-c.requests:
		return copy(p, request), nil, nil
	case <-c.closedCh:
		return 0, nil, net.ErrClosed
	}
}

func (c *spooferForwardScriptedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Write(p)
}

type spooferForwardBlockingPacketConn struct {
	mu           sync.Mutex
	readDeadline time.Time
	deadlineCh   chan struct{}
	closedCh     chan struct{}
	closeOnce    sync.Once
}

func newSpooferForwardBlockingPacketConn() *spooferForwardBlockingPacketConn {
	return &spooferForwardBlockingPacketConn{deadlineCh: make(chan struct{}, 1), closedCh: make(chan struct{})}
}

func (c *spooferForwardBlockingPacketConn) Read(p []byte) (int, error) {
	n, _, err := c.ReadFrom(p)
	return n, err
}
func (*spooferForwardBlockingPacketConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *spooferForwardBlockingPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closedCh) })
	return nil
}
func (*spooferForwardBlockingPacketConn) LocalAddr() net.Addr  { return nil }
func (*spooferForwardBlockingPacketConn) RemoteAddr() net.Addr { return nil }
func (c *spooferForwardBlockingPacketConn) SetDeadline(deadline time.Time) error {
	return c.SetReadDeadline(deadline)
}
func (c *spooferForwardBlockingPacketConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	select {
	case c.deadlineCh <- struct{}{}:
	default:
	}
	return nil
}
func (*spooferForwardBlockingPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *spooferForwardBlockingPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline := c.readDeadline
		c.mu.Unlock()
		if deadline.IsZero() {
			select {
			case <-c.deadlineCh:
				continue
			case <-c.closedCh:
				return 0, nil, net.ErrClosed
			}
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-timer.C:
			return 0, nil, &net.OpError{Op: "read", Net: "udp", Err: context.DeadlineExceeded}
		case <-c.deadlineCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-c.closedCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return 0, nil, net.ErrClosed
		}
	}
}

func (c *spooferForwardBlockingPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Write(p)
}

func spooferForwardTestEndpoint() stack.TransportEndpointID {
	return stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{127, 0, 0, 1}),
		LocalPort:    80,
	}
}

func spooferForwardTestIPv6Endpoint() stack.TransportEndpointID {
	return stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x80}),
		LocalPort:    443,
	}
}

type spooferForwardTestPacketConn struct {
	closed bool
}

var _ gonnect.PacketConn = (*spooferForwardTestPacketConn)(nil)

func (c *spooferForwardTestPacketConn) Read([]byte) (int, error) {
	return 0, net.ErrClosed
}

func (c *spooferForwardTestPacketConn) Write([]byte) (int, error) {
	return 0, net.ErrClosed
}

func (c *spooferForwardTestPacketConn) Close() error {
	c.closed = true
	return nil
}

func (c *spooferForwardTestPacketConn) LocalAddr() net.Addr {
	return nil
}

func (c *spooferForwardTestPacketConn) RemoteAddr() net.Addr {
	return nil
}

func (c *spooferForwardTestPacketConn) SetDeadline(time.Time) error {
	return nil
}

func (c *spooferForwardTestPacketConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *spooferForwardTestPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *spooferForwardTestPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, net.ErrClosed
}

func (c *spooferForwardTestPacketConn) WriteTo([]byte, net.Addr) (int, error) {
	return 0, net.ErrClosed
}
