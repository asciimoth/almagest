package network

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect"
)

func TestSocketValidation(t *testing.T) {
	validDial := protocol.SocketOptions{
		Purpose: protocol.NetworkOutbound,
		Kind:    protocol.SocketDialTCP,
		Network: "tcp4",
		Remote:  netip.MustParseAddrPort("192.0.2.1:443"),
	}
	validListen := protocol.SocketOptions{
		Purpose: protocol.NetworkLocal,
		Kind:    protocol.SocketListenUDP,
		Network: "udp6",
		Local:   netip.MustParseAddrPort("[::1]:0"),
	}
	validMulticast := protocol.SocketOptions{
		Purpose: protocol.NetworkLocal,
		Kind:    protocol.SocketMulticastUDP,
		Network: "udp6",
		Local:   netip.MustParseAddrPort("[::]:5353"),
		Multicast: protocol.MulticastOptions{
			ReuseAddr:    true,
			ControlFlags: uint32(gonnect.ControlDst | gonnect.ControlInterface),
		},
	}
	validNetworkResolver := protocol.SocketOptions{
		Purpose: protocol.NetworkLocal,
		Kind:    protocol.SocketNetworkResolver,
	}
	for _, options := range []protocol.SocketOptions{validDial, validListen, validMulticast, validNetworkResolver} {
		if err := validateSocket(options); err != nil {
			t.Fatalf("valid socket rejected: %+v: %v", options, err)
		}
	}

	tests := []struct {
		name   string
		base   protocol.SocketOptions
		change func(*protocol.SocketOptions)
		want   error
	}{
		{"purpose", validDial, func(o *protocol.SocketOptions) { o.Purpose = "host" }, protocol.ErrInvalidContract},
		{"tcp_network", validDial, func(o *protocol.SocketOptions) { o.Network = "udp4" }, protocol.ErrInvalidContract},
		{"udp_network", validListen, func(o *protocol.SocketOptions) { o.Network = "tcp6" }, protocol.ErrInvalidContract},
		{"unknown_kind", validListen, func(o *protocol.SocketOptions) { o.Kind = "raw" }, protocol.ErrUnsupportedOperation},
		{"dial_without_remote", validDial, func(o *protocol.SocketOptions) { o.Remote = netip.AddrPort{} }, protocol.ErrInvalidContract},
		{"dial_zero_port", validDial, func(o *protocol.SocketOptions) { o.Remote = netip.MustParseAddrPort("192.0.2.1:0") }, protocol.ErrInvalidContract},
		{"listen_with_remote", validListen, func(o *protocol.SocketOptions) { o.Remote = netip.MustParseAddrPort("[::1]:53") }, protocol.ErrInvalidContract},
		{"wrong_address_family", validDial, func(o *protocol.SocketOptions) { o.Remote = netip.MustParseAddrPort("[::1]:443") }, protocol.ErrInvalidContract},
		{"long_zone", validMulticast, func(o *protocol.SocketOptions) {
			o.Local = netip.AddrPortFrom(netip.MustParseAddr("fe80::1").WithZone(strings.Repeat("z", 65)), 5353)
		}, protocol.ErrLimitExceeded},
		{"read_buffer", validDial, func(o *protocol.SocketOptions) { o.ReadBuffer = protocol.MaxSessionData + 1 }, protocol.ErrLimitExceeded},
		{"write_buffer", validDial, func(o *protocol.SocketOptions) { o.WriteBuffer = protocol.MaxSessionData + 1 }, protocol.ErrLimitExceeded},
		{"negative_keepalive", validDial, func(o *protocol.SocketOptions) { o.KeepAlive = -1 }, protocol.ErrLimitExceeded},
		{"long_keepalive", validDial, func(o *protocol.SocketOptions) { o.KeepAlive = 24*time.Hour + 1 }, protocol.ErrLimitExceeded},
		{"absolute_deadline", validDial, func(o *protocol.SocketOptions) { o.Deadline = time.Now() }, protocol.ErrInvalidContract},
		{"unknown_control_flag", validMulticast, func(o *protocol.SocketOptions) { o.Multicast.ControlFlags = ^uint32(0) }, protocol.ErrInvalidContract},
		{"multicast_options_on_tcp", validDial, func(o *protocol.SocketOptions) { o.Multicast.ReuseAddr = true }, protocol.ErrInvalidContract},
		{"network_resolver_options", validNetworkResolver, func(o *protocol.SocketOptions) { o.Network = "udp" }, protocol.ErrInvalidContract},
		{"network_resolver_purpose", validNetworkResolver, func(o *protocol.SocketOptions) { o.Purpose = "host" }, protocol.ErrInvalidContract},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			options := tc.base
			tc.change(&options)
			if err := validateSocket(options); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSocketEndpointConversion(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr net.Addr
		want netip.AddrPort
	}{
		{"nil", nil, netip.AddrPort{}},
		{"tcp", &net.TCPAddr{IP: net.IP{192, 0, 2, 2}, Port: 80}, netip.MustParseAddrPort("192.0.2.2:80")},
		{"udp", &net.UDPAddr{IP: net.ParseIP("2001:db8::2"), Port: 53}, netip.MustParseAddrPort("[2001:db8::2]:53")},
		{"ip_zone", &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "test0"}, netip.AddrPortFrom(netip.MustParseAddr("fe80::1").WithZone("test0"), 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := endpoint(tc.addr); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
