package network

import (
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/gonnect"
	"net"
	"net/netip"
)

func readUDPData(conn gonnect.UDPConn, p []byte) (int, net.Addr, *protocol.PacketMetadata, error) {
	raw := make([]byte, 4096)
	n, on, _, src, err := conn.ReadMsgUDP(p, raw)
	if err != nil {
		return n, src, &protocol.PacketMetadata{}, err
	}
	meta, e := relay.ParseAncillary(raw[:on], false)
	if e != nil {
		return 0, src, nil, e
	}
	if meta.Destination != nil {
		ep := *meta.Destination
		ep = netip.AddrPortFrom(ep.Addr(), endpoint(conn.LocalAddr()).Port())
		meta.Destination = &ep
	}
	return n, src, meta, nil
}
