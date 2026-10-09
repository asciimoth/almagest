package runtime

import (
	"net"
	"net/netip"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/gonnect"
)

type UDPConn struct{ *Conn }

func (c *UDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, e := c.ReadFromUDP(p)
	return n, a, e
}
func (c *UDPConn) ReadFromUDP(p []byte) (int, *net.UDPAddr, error) {
	n, a, e := c.ReadFromUDPAddrPort(p)
	if !a.IsValid() {
		return n, nil, e
	}
	return n, net.UDPAddrFromAddrPort(a), e
}
func (c *UDPConn) ReadFromUDPAddrPort(p []byte) (int, netip.AddrPort, error) {
	n, m, e := c.readPacket(p)
	var a netip.AddrPort
	if m != nil && m.Source != nil {
		a = *m.Source
	}
	return n, a, e
}
func (c *UDPConn) WriteTo(p []byte, a net.Addr) (int, error) {
	if a == nil {
		return c.Write(p)
	}
	ep, e := netip.ParseAddrPort(a.String())
	if e != nil {
		return 0, e
	}
	return c.WriteToUDPAddrPort(p, ep)
}
func (c *UDPConn) WriteToUDP(p []byte, a *net.UDPAddr) (int, error) {
	if a == nil {
		return c.Write(p)
	}
	return c.WriteToUDPAddrPort(p, a.AddrPort())
}
func (c *UDPConn) WriteToUDPAddrPort(p []byte, a netip.AddrPort) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeRecord(p, &protocol.PacketMetadata{Destination: &a, OriginalLength: uint32(len(p))})
}
func (c *UDPConn) ReadMsgUDP(p, oob []byte) (int, int, int, *net.UDPAddr, error) {
	n, on, flags, a, e := c.ReadMsgUDPAddrPort(p, oob)
	if !a.IsValid() {
		return n, on, flags, nil, e
	}
	return n, on, flags, net.UDPAddrFromAddrPort(a), e
}
func (c *UDPConn) ReadMsgUDPAddrPort(p, oob []byte) (int, int, int, netip.AddrPort, error) {
	n, m, err := c.readPacket(p)
	var source netip.AddrPort
	flags := 0
	if m == nil {
		return n, 0, flags, source, err
	}
	if m.Source != nil {
		source = *m.Source
	}
	if m.Truncated {
		flags |= messageTruncated
	}
	raw, encodeErr := relay.MarshalAncillary(m, source.Addr().Is6(), false)
	if encodeErr != nil {
		return n, 0, flags, source, encodeErr
	}
	on := copy(oob, raw)
	if on < len(raw) {
		flags |= controlTruncated
	}
	return n, on, flags, source, err
}
func (c *UDPConn) WriteMsgUDP(p, oob []byte, a *net.UDPAddr) (int, int, error) {
	var ep netip.AddrPort
	if a != nil {
		ep = a.AddrPort()
	}
	return c.WriteMsgUDPAddrPort(p, oob, ep)
}
func (c *UDPConn) WriteMsgUDPAddrPort(p, oob []byte, a netip.AddrPort) (int, int, error) {
	meta, err := relay.ParseAncillary(oob, true)
	if err != nil {
		return 0, 0, err
	}
	meta.OriginalLength = uint32(len(p))
	if a.IsValid() {
		meta.Destination = &a
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	n, err := c.writeRecord(p, meta)
	on := 0
	if err == nil {
		on = len(oob)
	}
	return n, on, err
}
func (c *UDPConn) JoinGroup(iface gonnect.NetworkInterface, group net.Addr) error {
	return c.group("join_group", iface, group)
}
func (c *UDPConn) LeaveGroup(iface gonnect.NetworkInterface, group net.Addr) error {
	return c.group("leave_group", iface, group)
}
func (c *UDPConn) group(kind string, iface gonnect.NetworkInterface, group net.Addr) error {
	if group == nil {
		return protocol.ErrInvalidContract
	}
	var addr netip.Addr
	if ep, e := netip.ParseAddrPort(group.String()); e == nil {
		addr = ep.Addr()
	} else {
		addr, e = netip.ParseAddr(group.String())
		if e != nil {
			return e
		}
	}
	o := protocol.SocketSetting{Kind: kind, Group: addr}
	if iface != nil {
		o.InterfaceIndex = uint32(iface.Index())
	}
	return c.setting(o)
}
func (c *UDPConn) SetControlMessage(flags gonnect.ControlFlags, on bool) error {
	return c.setting(protocol.SocketSetting{Kind: "control_message", Value: int32(flags), Enabled: on})
}
func (c *UDPConn) ReadFromControl(p []byte) (int, gonnect.ControlMessage, net.Addr, error) {
	n, m, e := c.readPacket(p)
	var cm gonnect.ControlMessage
	var from net.Addr
	if m != nil {
		if m.Source != nil {
			from = net.UDPAddrFromAddrPort(*m.Source)
		}
		if m.Destination != nil {
			cm.Dst = &net.IPAddr{IP: net.IP(m.Destination.Addr().AsSlice()), Zone: m.Destination.Addr().Zone()}
		}
		if m.InterfaceIndex != nil {
			cm.IfIndex = int(*m.InterfaceIndex)
		}
	}
	return n, cm, from, e
}
func (c *UDPConn) WriteToControl(p []byte, cm gonnect.ControlMessage, dst net.Addr) (int, error) {
	if cm.IfName != "" || cm.Dst != nil {
		return 0, protocol.ErrCapabilityUnsupported
	}
	if dst == nil {
		return 0, protocol.ErrInvalidContract
	}
	ep, e := netip.ParseAddrPort(dst.String())
	if e != nil {
		return 0, e
	}
	m := &protocol.PacketMetadata{Destination: &ep, OriginalLength: uint32(len(p))}
	if cm.IfIndex != 0 {
		i := uint32(cm.IfIndex)
		m.InterfaceIndex = &i
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeRecord(p, m)
}
