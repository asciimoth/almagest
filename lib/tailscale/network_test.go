package tailscalenodes

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/asciimoth/gonnect"
)

func TestTailscaleUDPConnUnmapsIPv4MappedSource(t *testing.T) {
	want := netip.MustParseAddrPort("192.0.2.10:41641")
	base := &mappedSourceUDPConn{
		addr: netip.MustParseAddrPort("[::ffff:192.0.2.10]:41641"),
	}
	conn := &tailscaleUDPConn{UDPConn: base}

	_, got, err := conn.ReadFromUDPAddrPort(nil)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if got != want || got.Addr().Is6() {
		t.Fatalf("normalized source = %v, want %v", got, want)
	}
}

func TestTailscaleNetworkWrapsUDPConnectionsAndErrors(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	network := newTailscaleNetwork(base)

	listener, err := network.ListenUDP(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := listener.(*tailscaleUDPConn); !ok {
		t.Fatalf("ListenUDP returned %T", listener)
	}
	_ = listener.Close()

	listener, err = network.ListenUDPConfig(context.Background(), nil, "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := listener.(*tailscaleUDPConn); !ok {
		t.Fatalf("ListenUDPConfig returned %T", listener)
	}
	_ = listener.Close()

	if got, err := wrapTailscaleUDPConn(nil, nil); got != nil || err != nil {
		t.Fatalf("wrap nil = %v, %v", got, err)
	}
	wantErr := errors.New("dial failed")
	if got, err := wrapTailscaleUDPConn(nil, wantErr); got != nil || !errors.Is(err, wantErr) {
		t.Fatalf("wrap error = %v, %v", got, err)
	}
	if _, err := network.DialUDP(context.Background(), "bad-network", "", ""); err == nil {
		t.Fatal("DialUDP accepted an invalid network")
	}
}

func TestTailscaleUDPConnReadAdaptersUnmapIPv4(t *testing.T) {
	mapped := netip.MustParseAddrPort("[::ffff:192.0.2.10]:41641")
	base := &mappedReadUDPConn{mappedSourceUDPConn: mappedSourceUDPConn{addr: mapped}}
	conn := &tailscaleUDPConn{UDPConn: base}

	if _, addr, err := conn.ReadFrom(nil); err != nil || addr.(*net.UDPAddr).IP.To4() == nil {
		t.Fatalf("ReadFrom = %#v, %v", addr, err)
	}
	if _, addr, err := conn.ReadFromUDP(nil); err != nil || addr.IP.To4() == nil {
		t.Fatalf("ReadFromUDP = %#v, %v", addr, err)
	}
	if _, _, _, addr, err := conn.ReadMsgUDP(nil, nil); err != nil || addr.IP.To4() == nil {
		t.Fatalf("ReadMsgUDP = %#v, %v", addr, err)
	}
	if _, _, _, addr, err := conn.ReadMsgUDPAddrPort(nil, nil); err != nil || addr.Addr().Is6() {
		t.Fatalf("ReadMsgUDPAddrPort = %#v, %v", addr, err)
	}
	if got := unmapAddrPort(netip.AddrPort{}); got.IsValid() {
		t.Fatalf("unmap invalid = %v", got)
	}
	if got := unmapUDPAddr(nil); got != nil {
		t.Fatalf("unmap nil = %#v", got)
	}
	v6 := &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1}
	if got := unmapUDPAddr(v6); got != v6 {
		t.Fatal("native IPv6 address was copied")
	}
}

func TestUnmapUDPAddrPreservesMetadataAndDoesNotAliasIP(t *testing.T) {
	original := &net.UDPAddr{
		IP:   net.ParseIP("::ffff:192.0.2.10"),
		Port: 41641,
		Zone: "test-zone",
	}
	got := unmapUDPAddr(original)
	if got == original || !got.IP.Equal(net.IPv4(192, 0, 2, 10)) || got.Port != original.Port || got.Zone != original.Zone {
		t.Fatalf("unmapped UDP address = %#v", got)
	}
	got.IP[0] = 198
	if original.IP.To4()[0] != 192 {
		t.Fatal("unmapped IP aliases the source address")
	}
}

type mappedSourceUDPConn struct {
	gonnect.UDPConn
	addr netip.AddrPort
}

type mappedReadUDPConn struct {
	mappedSourceUDPConn
}

func (c *mappedReadUDPConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, net.UDPAddrFromAddrPort(c.addr), nil
}

func (c *mappedReadUDPConn) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	return 0, net.UDPAddrFromAddrPort(c.addr), nil
}

func (c *mappedReadUDPConn) ReadMsgUDP([]byte, []byte) (int, int, int, *net.UDPAddr, error) {
	return 0, 0, 0, net.UDPAddrFromAddrPort(c.addr), nil
}

func (c *mappedReadUDPConn) ReadMsgUDPAddrPort([]byte, []byte) (int, int, int, netip.AddrPort, error) {
	return 0, 0, 0, c.addr, nil
}

func (c *mappedSourceUDPConn) ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, c.addr, nil
}
