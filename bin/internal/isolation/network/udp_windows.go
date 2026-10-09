package network

import (
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect"
	"net"
)

func readUDPData(conn gonnect.UDPConn, p []byte) (int, net.Addr, *protocol.PacketMetadata, error) {
	n, src, err := conn.ReadFrom(p)
	return n, src, &protocol.PacketMetadata{}, err
}
