package networknodes

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

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
