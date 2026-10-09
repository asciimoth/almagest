package network

import (
	"context"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/gonnect"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
)

func TestLinuxTunRelay(t *testing.T) {
	if os.Getenv("ALMAGEST_ISOLATION_NATIVE_TEST") != "1" {
		t.Skip("requires disposable Docker network namespace")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("requires Docker")
	}
	system, err := linuxsysnet.NewSystem(linuxsysnet.Config{Features: linuxsysnet.FeatureConfig{Tun: true, DynTun: true, TunNames: true}, TUNFactory: RelayTUNFactory{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = system.Close() }()
	backend, err := NewSystemBackend(system, CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := testBroker(t)
	b.backend = backend
	exerciseTunRelay(t, b)
}

func TestLinuxUDPMetadataRelay(t *testing.T) {
	_, c := socketClient(t)
	network := c.Network(protocol.NetworkOutbound, nil)
	receiver, err := network.ListenUDP(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receiver.Close() }()
	sender, err := network.ListenUDP(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	source := netip.MustParseAddrPort("127.0.0.1:0")
	raw, err := relay.MarshalAncillary(&protocol.PacketMetadata{Source: &source}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	dst := receiver.LocalAddr().(*net.UDPAddr)
	n, on, err := sender.WriteMsgUDP([]byte("metadata"), raw, dst)
	if err != nil || n != 8 || on != len(raw) {
		t.Fatalf("send %d %d %v", n, on, err)
	}
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 20)
	n, from, err := receiver.ReadFromUDP(buffer)
	if err != nil || string(buffer[:n]) != "metadata" || !from.IP.Equal(source.Addr().AsSlice()) {
		t.Fatalf("receive %d %v %v", n, from, err)
	}
}

func TestLinuxMulticastRelay(t *testing.T) {
	_, c := socketClient(t)
	n := c.Network(protocol.NetworkOutbound, nil)
	interfaces, err := n.InterfacesByName("lo")
	if err != nil || len(interfaces) != 1 {
		t.Fatalf("loopback interface %v", err)
	}
	receiver, err := n.ListenMulticastUDP(context.Background(), "udp6", "[::]:0", gonnect.MulticastOptions{ReuseAddr: true, ControlFlags: gonnect.ControlDst | gonnect.ControlInterface})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receiver.Close() }()
	group := &net.UDPAddr{IP: net.ParseIP("ff02::114"), Port: receiver.LocalAddr().(*net.UDPAddr).Port}
	if err = receiver.JoinGroup(interfaces[0], group); err != nil {
		t.Fatal(err)
	}
	sender, err := n.ListenMulticastUDP(context.Background(), "udp6", "[::1]:0", gonnect.MulticastOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	if _, err = sender.WriteToControl([]byte("multicast"), gonnect.ControlMessage{IfIndex: interfaces[0].Index()}, &net.UDPAddr{IP: net.IPv6loopback, Port: group.Port}); err != nil {
		t.Fatal(err)
	}
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 32)
	count, cm, from, err := receiver.ReadFromControl(buffer)
	if err != nil || string(buffer[:count]) != "multicast" || cm.IfIndex != interfaces[0].Index() || cm.Dst == nil || from == nil {
		t.Fatalf("multicast %d %+v %v %v", count, cm, from, err)
	}
	if err = receiver.LeaveGroup(interfaces[0], group); err != nil {
		t.Fatal(err)
	}
}
