package networknodes

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"syscall"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const NodeTypeMockNetwork = "almagest/MockNetwork"

type MockNetworkClass struct{}

func (MockNetworkClass) ClassName() string        { return NodeTypeMockNetwork }
func (MockNetworkClass) ShortDescription() string { return "Virtual network mock" }
func (MockNetworkClass) LongDescription() string {
	return "Emulates network for test/debug purposes. Connections/Listeners created via MockNetwork are fully virtual and never reach real OS network stack."
}
func (MockNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (MockNetworkClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultMockNetConfig()
	config.read(cfg)
	network := NewMockLoopbackNetwork(config.allowAnyHost)
	return &mockNetNode{config: config, base: network}, nil
}

type mockNetConfig struct {
	// allowAnyHost keeps MockNetwork's historical all-address behavior. When
	// enabled, gonnect rewrites non-loopback host names and addresses to
	// loopback before binding or dialing.
	allowAnyHost bool
}

func defaultMockNetConfig() mockNetConfig {
	return mockNetConfig{allowAnyHost: true}
}

func (c *mockNetConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.allowAnyHost = configer.Bool(cfg, configer.Path{"allowAnyHost"}, c.allowAnyHost)
}

type mockNetNode struct {
	nodekit.SimpleNetworkProvider

	config mockNetConfig
	base   *MockLoopbackNetwork
}

func (n *mockNetNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.base == nil {
		n.base = NewMockLoopbackNetwork(n.config.allowAnyHost)
	}
	if err := n.InitSimpleNetworkProvider(w, nil, id, restored, n.base, "loopback"); err != nil {
		return err
	}
	if err := n.W.AddNodeResourceLocked(n.ID, n.base); err != nil {
		return err
	}
	n.sendMenuSnapshot(false)
	return nil
}

func (n *mockNetNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FormApplyMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
		return nil
	}
	if value, ok := formular.BoolValue(msg.Values["allowAnyHost"]); ok {
		n.config.allowAnyHost = value
		if n.base != nil {
			n.base.AllowAnyHost = value
		}
	}
	n.sendMenuSnapshot(true)
	return nil
}

func (n *mockNetNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"allowAnyHost": n.config.allowAnyHost,
	})
}

func (n *mockNetNode) sendMenuSnapshot(force bool) {
	sendNodeMenuSnapshot(n.W, n.ID, force, n.configBlock())
}

func (n *mockNetNode) configBlock() formular.Block {
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configCheckbox("allowAnyHost", "Allow any host", n.config.allowAnyHost),
	}}
}

// MockLoopbackNetwork normalizes mock UDP write errors at the source. The
// upstream SOCKS UDP relay treats host/network unreachable as transient via
// errors.Is, while gonnect's loopback "no route to host" error is textual.
// Keeping that normalization here lets downstream network payload wrappers stay
// ordinary detached wrappers that middleware can replace and close normally.
type MockLoopbackNetwork struct {
	*gonnect.LoopbackNetwork
}

// NewMockLoopbackNetwork installs MockNetwork's UDP error normalization while
// letting callers choose whether non-loopback host names and addresses should
// be rewritten to loopback. MockNetwork enables that all-host support by
// default, but tests and stricter debug setups can turn it off.
func NewMockLoopbackNetwork(allowAnyHost bool) *MockLoopbackNetwork {
	network := gonnect.NewLoopbackNetwork()
	network.AllowAnyHost = allowAnyHost
	return &MockLoopbackNetwork{LoopbackNetwork: network}
}

func (n *MockLoopbackNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	conn, err := n.LoopbackNetwork.PacketDial(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return wrapMockLoopbackPacketConn(conn), nil
}

func (n *MockLoopbackNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	conn, err := n.LoopbackNetwork.ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return wrapMockLoopbackPacketConn(conn), nil
}

func (n *MockLoopbackNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	conn, err := n.LoopbackNetwork.DialUDP(ctx, network, laddr, raddr)
	if err != nil {
		return nil, err
	}
	return mockLoopbackUDPConn{UDPConn: conn}, nil
}

func (n *MockLoopbackNetwork) ListenUDP(ctx context.Context, network, address string) (gonnect.UDPConn, error) {
	conn, err := n.LoopbackNetwork.ListenUDP(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return mockLoopbackUDPConn{UDPConn: conn}, nil
}

func (n *MockLoopbackNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.UDPConn, error) {
	conn, err := n.LoopbackNetwork.ListenUDPConfig(ctx, lc, network, address)
	if err != nil {
		return nil, err
	}
	return mockLoopbackUDPConn{UDPConn: conn}, nil
}

func wrapMockLoopbackPacketConn(conn gonnect.PacketConn) gonnect.PacketConn {
	if udp, ok := conn.(gonnect.UDPConn); ok {
		return mockLoopbackUDPConn{UDPConn: udp}
	}
	return mockLoopbackPacketConn{PacketConn: conn}
}

// mockLoopbackPacketConn covers non-UDPConn packet interfaces so every packet
// write path exposed by MockNetwork carries the same transient routing signal.
type mockLoopbackPacketConn struct {
	gonnect.PacketConn
}

func (c mockLoopbackPacketConn) GetWrapped() any {
	return c.PacketConn
}

func (c mockLoopbackPacketConn) Write(b []byte) (int, error) {
	n, err := c.PacketConn.Write(b)
	return n, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, addr)
	return n, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackPacketConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	if udp, ok := c.PacketConn.(interface {
		WriteToUDP([]byte, *net.UDPAddr) (int, error)
	}); ok {
		n, err := udp.WriteToUDP(b, addr)
		return n, normalizeMockLoopbackUDPWriteError(err)
	}
	return c.WriteTo(b, addr)
}

// mockLoopbackUDPConn wraps concrete UDP methods because callers may bypass
// PacketConn.WriteTo through UDP-specific write APIs.
type mockLoopbackUDPConn struct {
	gonnect.UDPConn
}

func (c mockLoopbackUDPConn) GetWrapped() any {
	return c.UDPConn
}

func (c mockLoopbackUDPConn) Write(b []byte) (int, error) {
	n, err := c.UDPConn.Write(b)
	return n, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackUDPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.UDPConn.WriteTo(b, addr)
	return n, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	n, err := c.UDPConn.WriteToUDP(b, addr)
	return n, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackUDPConn) WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error) {
	n, err := c.UDPConn.WriteToUDPAddrPort(b, addr)
	return n, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	n, oobn, err := c.UDPConn.WriteMsgUDP(b, oob, addr)
	return n, oobn, normalizeMockLoopbackUDPWriteError(err)
}

func (c mockLoopbackUDPConn) WriteMsgUDPAddrPort(b, oob []byte, addr netip.AddrPort) (int, int, error) {
	n, oobn, err := c.UDPConn.WriteMsgUDPAddrPort(b, oob, addr)
	return n, oobn, normalizeMockLoopbackUDPWriteError(err)
}

// normalizeMockLoopbackUDPWriteError adds a standard transient-routing sentinel
// to gonnect loopback errors without hiding the original failure text.
func normalizeMockLoopbackUDPWriteError(err error) error {
	if err == nil || errors.Is(err, syscall.EHOSTUNREACH) {
		return err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "write" && opErr.Err != nil && strings.Contains(opErr.Err.Error(), "no route to host") {
		// Preserve the original mock-network error for diagnostics while adding
		// the syscall sentinel expected by upstream transient-write handling.
		return errors.Join(syscall.EHOSTUNREACH, err)
	}
	return err
}
