package protocol

import "net/netip"

const (
	DataVersion     = 1
	MaxDataFrame    = 80 * 1024
	MaxDataMetadata = 8 * 1024
	MaxDataPayload  = 64 * 1024
	MaxDataRecords  = 16
	MaxStreamRecord = 32 * 1024
	MaxPacketRecord = 65535
	MaxSessionData  = 8 * 1024 * 1024
	// MaxNativePacketBatchBytes bounds broker-owned native TUN read scratch.
	// A read can use fewer packets than the device's advertised batch size.
	MaxNativePacketBatchBytes = MaxSessionData / 2
)

// PacketMetadata contains typed ancillary fields. Native control bytes are
// never accepted on the wire. Nil endpoints are used for TUN packets.
type PacketMetadata struct {
	Source         *netip.AddrPort `json:"source"`
	Destination    *netip.AddrPort `json:"destination"`
	InterfaceIndex *uint32         `json:"interfaceIndex"`
	HopLimit       *uint8          `json:"hopLimit"`
	TrafficClass   *uint8          `json:"trafficClass"`
	OriginalLength uint32          `json:"originalLength"`
	Truncated      bool            `json:"truncated"`
	// BatchRemaining is the number of packets from the same native TUN read
	// that remain queued at the broker. It is zero for sockets and DNS.
	BatchRemaining uint32 `json:"batchRemaining"`
	// DNS is present only on a ResourceDNS application-resolver relay. Native
	// socket ancillary data and TUN packets must not use it.
	DNS *DNSMetadata `json:"dns"`
}

type DNSMetadata struct {
	Correlation uint64 `json:"correlation"`
}

type DataRecord struct {
	Request uint64          `json:"request"`
	Length  uint32          `json:"length"`
	Packet  *PacketMetadata `json:"packet"`
}

type DataMetadata struct {
	Version      uint32       `json:"version"`
	Session      string       `json:"session"`
	SessionEpoch uint64       `json:"sessionEpoch"`
	Resource     string       `json:"resource"`
	Generation   uint64       `json:"generation"`
	GraphEpoch   uint64       `json:"graphEpoch"`
	Sequence     uint64       `json:"sequence"`
	Records      []DataRecord `json:"records"`
}

// ChannelRequest is a closed data-control union. Remaining is a duration in
// nanoseconds: nil means no deadline, zero means expired. Request identifies
// one read or write; it is distinct from the control transport request ID.
// PrepareWrite grants exactly one record of Size bytes. Read grants the same
// receive capacity to the broker. Neither grant confers resource authority.
type ChannelRequest struct {
	Meta      RequestMeta
	Resource  ResourceRef
	Kind      ChannelKind
	Request   uint64
	Size      uint32
	Remaining *int64
	Options   SocketSetting
}

type ChannelKind string

const (
	ChannelNegotiate        ChannelKind = "negotiate"
	ChannelRead             ChannelKind = "read"
	ChannelPrepareWrite     ChannelKind = "prepare_write"
	ChannelWrite            ChannelKind = "write"
	ChannelCloseRead        ChannelKind = "close_read"
	ChannelCloseWrite       ChannelKind = "close_write"
	ChannelSetReadDeadline  ChannelKind = "read_deadline"
	ChannelSetWriteDeadline ChannelKind = "write_deadline"
	ChannelSocketSetting    ChannelKind = "socket_setting"
	ChannelAccept           ChannelKind = "accept"
	ChannelClaim            ChannelKind = "claim"
	ChannelReject           ChannelKind = "reject"
)

// SocketSetting admits only named options; it is not a setsockopt interface.
type SocketSetting struct {
	Kind           string
	Enabled        bool
	Value          int32
	Idle           int64
	Interval       int64
	Count          int32
	InterfaceIndex uint32
	Group          netip.Addr
}

type ChannelResult struct {
	Count    uint32
	Code     string
	Resource ResourceRef
	State    ResourceState
}
