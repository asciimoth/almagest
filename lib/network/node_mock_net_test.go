package networknodes

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func TestMockLoopbackUDPWriteAdapters(t *testing.T) {
	network := NewMockLoopbackNetwork(true)
	listener, err := network.ListenUDP(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	address, err := net.ResolveUDPAddr("udp4", listener.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	addressPort := netip.MustParseAddrPort(address.String())

	if wrapped, ok := listener.(interface{ GetWrapped() any }); !ok || wrapped.GetWrapped() == nil {
		t.Fatalf("ListenUDP result = %T, want wrapped connection", listener)
	}
	writes := []func() error{
		func() error { _, err := listener.WriteTo([]byte("a"), address); return err },
		func() error { _, err := listener.WriteToUDP([]byte("b"), address); return err },
		func() error { _, err := listener.WriteToUDPAddrPort([]byte("c"), addressPort); return err },
		func() error { _, _, err := listener.WriteMsgUDP([]byte("d"), nil, address); return err },
		func() error { _, _, err := listener.WriteMsgUDPAddrPort([]byte("e"), nil, addressPort); return err },
	}
	for index, write := range writes {
		if err := write(); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}

	connected, err := network.DialUDP(context.Background(), "udp4", "127.0.0.1:0", address.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connected.Close() }()
	if _, err := connected.Write([]byte("f")); err != nil {
		t.Fatal(err)
	}

	packet := mockLoopbackPacketConn{PacketConn: connected}
	if packet.GetWrapped() == nil {
		t.Fatal("packet wrapper has no wrapped connection")
	}
	if _, err := packet.Write([]byte("g")); err != nil {
		t.Fatal(err)
	}
	packet = mockLoopbackPacketConn{PacketConn: listener}
	if _, err := packet.WriteTo([]byte("h"), address); err != nil {
		t.Fatal(err)
	}
	if _, err := packet.WriteToUDP([]byte("i"), address); err != nil {
		t.Fatal(err)
	}
}

func TestMockLoopbackUDPNoRouteWriteIsHostUnreachable(t *testing.T) {
	// Upstream SOCKS UDP handling keys transient no-route writes off syscall
	// sentinels, so MockNetwork's textual loopback error must expose one.
	network := NewMockLoopbackNetwork(true)
	conn, err := network.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket error = %v, want nil", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = conn.WriteTo([]byte("ping"), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9})
	if !errors.Is(err, syscall.EHOSTUNREACH) {
		t.Fatalf("WriteTo error = %v, want errors.Is EHOSTUNREACH", err)
	}
}

func TestMockLoopbackNetworkAllowAnyHostToggle(t *testing.T) {
	tests := []struct {
		name         string
		allowAnyHost bool
		wantListen   bool
	}{
		{name: "enabled", allowAnyHost: true, wantListen: true},
		{name: "disabled", allowAnyHost: false, wantListen: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := NewMockLoopbackNetwork(tt.allowAnyHost)
			listener, err := network.ListenTCP(context.Background(), "tcp", "example.com:0")
			if err == nil {
				defer func() { _ = listener.Close() }()
			}
			if got := err == nil; got != tt.wantListen {
				t.Fatalf("ListenTCP accepted non-loopback host = %v, want %v (err=%v)", got, tt.wantListen, err)
			}
		})
	}
}

func TestMockNetworkConfigDefaultsToAllowAnyHostAndCanDisable(t *testing.T) {
	node, err := MockNetworkClass{}.NewNode(configer.NewMemory(nil))
	if err != nil {
		t.Fatalf("NewNode default config: %v", err)
	}
	defaultNode := node.(*mockNetNode)
	if !defaultNode.config.allowAnyHost || !defaultNode.base.AllowAnyHost {
		t.Fatalf("default allowAnyHost config=%v base=%v, want true/true", defaultNode.config.allowAnyHost, defaultNode.base.AllowAnyHost)
	}

	cfg := configer.NewMemory(map[string]any{"allowAnyHost": false})
	node, err = MockNetworkClass{}.NewNode(cfg)
	if err != nil {
		t.Fatalf("NewNode disabled config: %v", err)
	}
	disabledNode := node.(*mockNetNode)
	if disabledNode.config.allowAnyHost || disabledNode.base.AllowAnyHost {
		t.Fatalf("disabled allowAnyHost config=%v base=%v, want false/false", disabledNode.config.allowAnyHost, disabledNode.base.AllowAnyHost)
	}
}

func TestMockNetworkFormAndSaveAllowAnyHost(t *testing.T) {
	node, err := MockNetworkClass{}.NewNode(configer.NewMemory(nil))
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	mock := node.(*mockNetNode)
	mock.ID = 77

	if err := mock.OnFormularMsg(formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(mock.ID)},
		BlockID:     "config",
		Values:      map[string]any{"allowAnyHost": false},
	}); err != nil {
		t.Fatalf("OnFormularMsg: %v", err)
	}
	if mock.config.allowAnyHost || mock.base.AllowAnyHost {
		t.Fatalf("form allowAnyHost config=%v base=%v, want false/false", mock.config.allowAnyHost, mock.base.AllowAnyHost)
	}

	cfg := configer.NewMemory(nil)
	if err := mock.OnSave(cfg); err != nil {
		t.Fatalf("OnSave: %v", err)
	}
	got, err := cfg.Get(configer.Path{"allowAnyHost"})
	if err != nil {
		t.Fatalf("saved allowAnyHost missing: %v", err)
	}
	if got != false {
		t.Fatalf("saved allowAnyHost = %#v, want false", got)
	}
}
