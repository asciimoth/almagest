package networknodes

import (
	"context"
	"errors"
	"io"
	"net"
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

func TestSpooferForwardTCPNilSpawnerClosesConn(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()

	forwardSpooferTCP(context.Background(), nil, NewMockLoopbackNetwork(false), client, spooferForwardTestEndpoint())

	_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := peer.Read([]byte{0}); err != io.EOF {
		t.Fatalf("peer read error = %v, want EOF", err)
	}
}

func TestSpooferForwardUDPNilSpawnerClosesConn(t *testing.T) {
	conn := &spooferForwardTestPacketConn{}

	forwardSpooferUDP(context.Background(), nil, NewMockLoopbackNetwork(false), conn, spooferForwardTestEndpoint())

	if !conn.closed {
		t.Fatal("packet conn was not closed")
	}
}

func TestSpooferForwardUDPDialsConfiguredTarget(t *testing.T) {
	network := &spooferForwardRecordingNetwork{LoopbackNetwork: gonnect.NewLoopbackNetwork()}
	t.Cleanup(func() { _ = network.Close() })
	conn := &spooferForwardTestPacketConn{}

	forwardSpooferUDP(context.Background(), spooferForwardStopSpawner{}, network, conn, spooferForwardTestEndpoint())

	if network.listenPacketCalls != 0 {
		t.Fatalf("ListenPacket calls = %d, want 0", network.listenPacketCalls)
	}
	if network.packetDialCalls != 1 {
		t.Fatalf("PacketDial calls = %d, want 1", network.packetDialCalls)
	}
	if network.packetDialNetwork != "udp" || network.packetDialAddress != "127.0.0.1:80" {
		t.Fatalf("PacketDial(%q, %q), want udp 127.0.0.1:80", network.packetDialNetwork, network.packetDialAddress)
	}
	if !conn.closed {
		t.Fatal("packet conn was not closed after spawner failure")
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

	packetDialCalls   int
	packetDialNetwork string
	packetDialAddress string
	listenPacketCalls int
}

func (n *spooferForwardRecordingNetwork) PacketDial(_ context.Context, network, address string) (gonnect.PacketConn, error) {
	n.packetDialCalls++
	n.packetDialNetwork = network
	n.packetDialAddress = address
	return &spooferForwardTestPacketConn{}, nil
}

func (n *spooferForwardRecordingNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	n.listenPacketCalls++
	return n.LoopbackNetwork.ListenPacket(ctx, network, address)
}

func spooferForwardTestEndpoint() stack.TransportEndpointID {
	return stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{127, 0, 0, 1}),
		LocalPort:    80,
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
