package network

import (
	"net"
	"net/netip"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/gonnect"
)

func (s *nativeSocket) SetReadDeadline(t time.Time) error {
	switch {
	case s.tcp != nil:
		return s.tcp.SetReadDeadline(t)
	case s.udp != nil:
		return s.udp.SetReadDeadline(t)
	case s.multicast != nil:
		return s.multicast.SetReadDeadline(t)
	case s.listener != nil:
		return s.listener.SetDeadline(t)
	}
	return protocol.ErrResourceType
}
func (s *nativeSocket) SetWriteDeadline(t time.Time) error {
	switch {
	case s.tcp != nil:
		return s.tcp.SetWriteDeadline(t)
	case s.udp != nil:
		return s.udp.SetWriteDeadline(t)
	case s.multicast != nil:
		return s.multicast.SetWriteDeadline(t)
	}
	return protocol.ErrResourceType
}
func (s *nativeSocket) ReadData(p []byte) (int, *protocol.PacketMetadata, error) {
	if s.tcp != nil {
		n, e := s.tcp.Read(p)
		return n, nil, e
	}
	var n int
	var src net.Addr
	var err error
	meta := &protocol.PacketMetadata{}
	if s.multicast != nil {
		var cm gonnect.ControlMessage
		n, cm, src, err = s.multicast.ReadFromControl(p)
		if cm.Dst != nil {
			ep := endpoint(cm.Dst)
			if ep.IsValid() {
				meta.Destination = &ep
			}
		}
		if cm.IfIndex != 0 {
			index := uint32(cm.IfIndex)
			meta.InterfaceIndex = &index
		}
	} else if s.udp != nil {
		n, src, meta, err = readUDPData(s.udp, p)
	} else {
		return 0, nil, protocol.ErrResourceType
	}
	if n < 0 && err != nil {
		n = 0
	}
	if meta == nil {
		return n, nil, err
	}
	if src != nil {
		ep := endpoint(src)
		meta.Source = &ep
	}
	if n >= 0 {
		meta.OriginalLength = uint32(n)
	}
	return n, meta, err
}
func (s *nativeSocket) WriteData(p []byte, meta *protocol.PacketMetadata) (int, error) {
	if s.tcp != nil {
		if meta != nil {
			return 0, protocol.ErrInvalidContract
		}
		return s.tcp.Write(p)
	}
	if meta == nil {
		return 0, protocol.ErrCapabilityUnsupported
	}
	if meta.DNS != nil {
		return 0, protocol.ErrInvalidContract
	}
	if s.multicast != nil {
		if meta.Source != nil || meta.HopLimit != nil || meta.TrafficClass != nil {
			return 0, protocol.ErrCapabilityUnsupported
		}
		if meta.Destination == nil {
			return 0, protocol.ErrInvalidContract
		}
		cm := gonnect.ControlMessage{}
		if meta.InterfaceIndex != nil {
			cm.IfIndex = int(*meta.InterfaceIndex)
		}
		return s.multicast.WriteToControl(p, cm, net.UDPAddrFromAddrPort(*meta.Destination))
	}
	if s.udp == nil {
		return 0, protocol.ErrResourceType
	}
	if meta.Source != nil || meta.InterfaceIndex != nil || meta.HopLimit != nil || meta.TrafficClass != nil {
		if err := s.validatePacketSource(meta); err != nil {
			return 0, err
		}
		raw, err := relay.MarshalAncillary(meta, endpoint(s.udp.LocalAddr()).Addr().Is6(), true)
		if err != nil {
			return 0, err
		}
		var dst *net.UDPAddr
		if meta.Destination != nil {
			dst = net.UDPAddrFromAddrPort(*meta.Destination)
		}
		n, _, err := s.udp.WriteMsgUDP(p, raw, dst)
		return n, err
	}
	if meta.Destination == nil {
		return s.udp.Write(p)
	}
	return s.udp.WriteToUDPAddrPort(p, *meta.Destination)
}

func (s *nativeSocket) setting(o protocol.SocketSetting) error {
	if s.tcp != nil {
		switch o.Kind {
		case "keepalive":
			return s.tcp.SetKeepAlive(o.Enabled)
		case "keepalive_period":
			if o.Idle < 0 || o.Idle > int64(24*time.Hour) {
				return protocol.ErrInvalidContract
			}
			return s.tcp.SetKeepAlivePeriod(time.Duration(o.Idle))
		case "keepalive_config":
			if o.Idle < 0 || o.Interval < 0 || o.Idle > int64(24*time.Hour) || o.Interval > int64(24*time.Hour) || o.Count < 0 || o.Count > 1024 {
				return protocol.ErrInvalidContract
			}
			return s.tcp.SetKeepAliveConfig(net.KeepAliveConfig{Enable: o.Enabled, Idle: time.Duration(o.Idle), Interval: time.Duration(o.Interval), Count: int(o.Count)})
		case "linger":
			if o.Value < -1 || o.Value > 86400 {
				return protocol.ErrInvalidContract
			}
			return s.tcp.SetLinger(int(o.Value))
		case "no_delay":
			return s.tcp.SetNoDelay(o.Enabled)
		}
	}
	if o.Kind == "read_buffer" || o.Kind == "write_buffer" {
		if o.Value <= 0 || o.Value > protocol.MaxSessionData {
			return protocol.ErrInvalidContract
		}
		var target any = s.tcp
		if s.udp != nil {
			target = s.udp
		}
		if s.multicast != nil {
			target = s.multicast
		}
		if o.Kind == "read_buffer" {
			if v, ok := target.(interface{ SetReadBuffer(int) error }); ok {
				return v.SetReadBuffer(int(o.Value))
			}
		} else {
			if v, ok := target.(interface{ SetWriteBuffer(int) error }); ok {
				return v.SetWriteBuffer(int(o.Value))
			}
		}
	}
	if s.multicast != nil {
		switch o.Kind {
		case "control_message":
			if o.Value < 0 || o.Value > int32(gonnect.ControlDst|gonnect.ControlInterface) {
				return protocol.ErrInvalidContract
			}
			return s.multicast.SetControlMessage(gonnect.ControlFlags(o.Value), o.Enabled)
		case "join_group", "leave_group":
			if !o.Group.IsMulticast() {
				return protocol.ErrInvalidContract
			}
			var iface gonnect.NetworkInterface
			if o.InterfaceIndex != 0 {
				ifaces, err := s.network.InterfacesByIndex(int(o.InterfaceIndex))
				if err != nil {
					return err
				}
				if len(ifaces) != 1 {
					return protocol.ErrCapabilityUnavailable
				}
				iface = ifaces[0]
			}
			group := net.UDPAddrFromAddrPort(netip.AddrPortFrom(o.Group, 0))
			if o.Kind == "join_group" {
				return s.multicast.JoinGroup(iface, group)
			}
			return s.multicast.LeaveGroup(iface, group)
		}
	}
	return protocol.ErrCapabilityUnsupported
}

func (s *nativeSocket) validatePacketSource(meta *protocol.PacketMetadata) error {
	if meta.Source != nil && meta.Source.Port() != 0 && meta.Source.Port() != endpoint(s.udp.LocalAddr()).Port() {
		return protocol.ErrInvalidContract
	}
	if meta.Source == nil && meta.InterfaceIndex == nil {
		return nil
	}
	interfaces, err := s.network.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range interfaces {
		if meta.InterfaceIndex != nil && *meta.InterfaceIndex != 0 && uint32(iface.Index()) != *meta.InterfaceIndex {
			continue
		}
		if meta.Source == nil || meta.Source.Addr().IsUnspecified() {
			return nil
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return err
		}
		for _, address := range addresses {
			p, err := netip.ParsePrefix(address.String())
			if err == nil && p.Addr() == meta.Source.Addr() {
				return nil
			}
		}
	}
	return protocol.ErrPolicyDenied
}
