// Package protocol defines the versioned daemon-isolation control contracts.
//
// These types describe control operations. They do not select or describe a
// packet data channel. In particular, they do not expose file paths, argv,
// native handles, syscalls, ioctls, registry operations, driver calls, or
// generic paths. A typed matcher rule can still contain a path value when the
// reviewed sysnet rule contract requires it.
package protocol

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	// Version is the first isolation control-contract version.
	Version uint32 = 1

	MaxControlFrameBytes      = 64 * 1024
	MaxControlDepth           = 32
	MaxConcurrentRequests     = 128
	MaxConfigChunkBytes       = 64 * 1024
	MaxConfigBytes            = 8 * 1024 * 1024
	MaxOperationIDBytes       = 128
	MaxDiagnosticMessageBytes = 4 * 1024
	MaxDiagnosticSourceBytes  = 128
	MaxResourceWarnings       = 128
	MaxResourceWarningBytes   = 1024
	MaxRuleCompletionResults  = 128
	MaxInterfaceSnapshotItems = 4096
	MaxAddressOrRouteSetItems = 4096
	MaxLiveResources          = 4096
	MaxOperationReceipts      = 8192
	MaxCommandSlots           = 32
	MaxCommandSlotBytes       = 32 * 1024
	MaxCommandTotalSlotBytes  = 48 * 1024
	MaxCommandOutputBytes     = 1024 * 1024
)

var (
	ErrInvalidContract       = errors.New("invalid isolation control contract")
	ErrUnsupportedVersion    = errors.New("unsupported isolation protocol version")
	ErrUnsupportedOperation  = errors.New("unsupported isolation operation")
	ErrCapabilityUnsupported = errors.New("isolation capability is unsupported")
	ErrCapabilityUnavailable = errors.New("isolation capability is unavailable")
	ErrPolicyDenied          = errors.New("isolation policy denied operation")
	ErrLimitExceeded         = errors.New("isolation control limit exceeded")
	ErrForeignResource       = errors.New("isolation resource belongs to another session")
	ErrResourceType          = errors.New("isolation resource has the wrong type")
	ErrStaleGeneration       = errors.New("isolation resource generation is stale")
	ErrStaleGraphEpoch       = errors.New("isolation graph epoch is stale")
	ErrOperationConflict     = errors.New("isolation operation conflicts with retained receipt")
	ErrOperationUnknown      = errors.New("isolation operation result is unknown")
	ErrRecoveryRequired      = errors.New("isolation operation requires recovery")
)

// Role is assigned by the trusted launcher. A peer cannot select its role.
type Role string

const (
	RoleRuntime       Role = "runtime"
	RoleNetworkBroker Role = "network_broker"
	RoleConfigWriter  Role = "config_writer"
	RoleCommandHelper Role = "command_helper"
)

// ResourceType identifies one broker-owned object class.
type ResourceType string

const (
	ResourceAllocation ResourceType = "allocation"
	ResourceTun        ResourceType = "tun"
	ResourceDefaultTun ResourceType = "default_tun"
	ResourceSocket     ResourceType = "socket"
	ResourceListener   ResourceType = "listener"
	ResourceMatcher    ResourceType = "matcher"
	ResourceDNS        ResourceType = "dns_attachment"
)

// Session identifies one channel-bound runtime instance. Epoch changes when a
// new session uses the same launch. PolicyDigest covers trusted normalized
// policy and is a consistency check, not runtime-supplied authority.
type Session struct {
	ID           SessionID
	Epoch        uint64
	Version      uint32
	PolicyDigest [32]byte
}

// RequestMeta is present on every control request. OperationID is required for
// a mutation and is empty for a read-only request.
type RequestMeta struct {
	RequestID    uint64
	SessionID    SessionID
	SessionEpoch uint64
	GraphEpoch   uint64
	OperationID  string
}

// ResourceRef is an opaque broker-issued reference. The broker checks every
// field before it looks up or operates on the native object.
type ResourceRef struct {
	ID         ResourceID
	SessionID  SessionID
	Type       ResourceType
	Generation uint64
	GraphEpoch uint64
}

// CapabilityState preserves the backend distinction between an unsupported
// operation, a supported operation that is unavailable now, and an operation
// whose availability is not known.
type CapabilityState string

const (
	CapabilityUnknown     CapabilityState = "unknown"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnavailable CapabilityState = "unavailable"
	CapabilitySupported   CapabilityState = "supported"
)

// Capability is a transport-independent capability record. AttemptAllowed is
// especially important for unknown capabilities: callers can retain valid
// attempt-on-unknown behavior instead of treating unknown as denied.
type Capability struct {
	State          CapabilityState
	Reasons        []string
	Detail         string
	Limitations    []string
	AttemptAllowed bool
}

// CapabilitySnapshot is the complete effective report after backend probes,
// optional policy restrictions, and isolation-adapter limitations.
type CapabilitySnapshot struct {
	SchemaVersion      uint32
	Revision           uint64
	Operations         []OperationCapability
	DefaultTunProfiles []DefaultTunProfile
	Rules              []RuleCapability
	Ownership          []OwnershipCapability
}

type OperationCapability struct {
	Operation Operation
	// NativeOperation preserves exact backend scopes such as create_named.
	NativeOperation string
	Target          string
	Family          string
	Capability
}

type DefaultTunProfile struct {
	Family     string
	Mode       string
	Strict     bool
	Capability Capability
	Rules      []RuleBinding
}

type RuleBinding struct {
	Type       string
	Capability Capability
}

type MatcherCapability struct {
	Family     string
	Protocol   string
	Quality    string
	Capability Capability
}

type RuleCapability struct {
	Type        string
	Description string
	ValueKind   string
	SemanticsID string
	Validation  Capability
	Completion  Capability
	Matchers    []MatcherCapability
}

type OwnerFieldCapability struct {
	Field      string
	Capability Capability
}

type OwnershipCapability struct {
	Family     string
	Protocol   string
	Quality    string
	Capability Capability
	Fields     []OwnerFieldCapability
}

// Operation is a closed list of isolation control operations. An unknown value
// is rejected. It does not grant access to a similarly named native API.
type Operation string

const (
	OperationHandshake          Operation = "handshake"
	OperationCapabilities       Operation = "capabilities"
	OperationStatus             Operation = "status"
	OperationCheckTun           Operation = "check_tun"
	OperationCheckDefaultTun    Operation = "check_default_tun"
	OperationCheckRule          Operation = "check_rule"
	OperationCompleteRule       Operation = "complete_rule"
	OperationReserveIP          Operation = "reserve_ip"
	OperationReserveSubnet      Operation = "reserve_subnet"
	OperationReleaseAllocation  Operation = "release_allocation"
	OperationCreateTun          Operation = "create_tun"
	OperationCreateDefaultTun   Operation = "create_default_tun"
	OperationSetTunMTU          Operation = "set_tun_mtu"
	OperationSetTunAddresses    Operation = "set_tun_addresses"
	OperationSetTunRoutes       Operation = "set_tun_routes"
	OperationSetTunName         Operation = "set_tun_name"
	OperationGetTunState        Operation = "get_tun_state"
	OperationConfigureDNS       Operation = "configure_dns"
	OperationOpenSocket         Operation = "open_socket"
	OperationChannel            Operation = "data_channel"
	OperationListInterfaces     Operation = "list_interfaces"
	OperationResolverSnapshot   Operation = "resolver_snapshot"
	OperationCreateMatcher      Operation = "create_matcher"
	OperationMatch              Operation = "match"
	OperationCloseResource      Operation = "close_resource"
	OperationReleaseGraphEpoch  Operation = "release_graph_epoch"
	OperationReadConfig         Operation = "read_config"
	OperationReplaceConfig      Operation = "replace_config"
	OperationGetOperationResult Operation = "get_operation_result"
	OperationRunNodeCommand     Operation = "run_node_command"
	OperationRunCallback        Operation = "run_callback"
)

// NetworkControl is the typed control-plane surface owned by N. Returned TUN,
// socket, and listener data channels are deliberately not part of version 1.
type NetworkControl interface {
	Handshake(context.Context, HandshakeRequest) (HandshakeResult, error)
	Capabilities(context.Context, ReadRequest) (CapabilitySnapshot, error)
	Status(context.Context, ReadRequest) (StatusResult, error)
	GetResult(context.Context, OperationResultRequest) (ResourceResult, error)
	CheckTun(context.Context, CheckTunRequest) (ValidationResult, error)
	CheckDefaultTun(context.Context, CheckDefaultTunRequest) (ValidationResult, error)
	CheckRule(context.Context, CheckRuleRequest) (ValidationResult, error)
	CompleteRule(context.Context, CompleteRuleRequest) (RuleCompletionResult, error)
	ReserveAddress(context.Context, ReserveAddressRequest) (ResourceResult, error)
	CreateTun(context.Context, CreateTunRequest) (ResourceResult, error)
	UpdateTun(context.Context, UpdateTunRequest) (MutationResult, error)
	ConfigureDNS(context.Context, ConfigureDNSRequest) (MutationResult, error)
	OpenSocket(context.Context, OpenSocketRequest) (ResourceResult, error)
	ListInterfaces(context.Context, ReadRequest) (InterfaceSnapshot, error)
	ResolverSnapshot(context.Context, ReadRequest) (ResolverSnapshot, error)
	CreateMatcher(context.Context, CreateMatcherRequest) (ResourceResult, error)
	Match(context.Context, MatchRequest) (MatchResult, error)
	GetResourceState(context.Context, ResourceRequest) (ResourceState, error)
	CloseResource(context.Context, ResourceRequest) (MutationResult, error)
	ReleaseGraphEpoch(context.Context, ReleaseGraphEpochRequest) (MutationResult, error)
}

type HandshakeRequest struct {
	Meta           RequestMeta
	Version        uint32
	ExpectedPolicy [32]byte
}

type HandshakeResult struct {
	Session Session
	Limits  Limits
}

type Limits struct {
	ControlFrameBytes  uint32
	ControlDepth       uint16
	ConcurrentRequests uint16
	ArrayItems         uint32
	StringBytes        uint32
}

type ReadRequest struct{ Meta RequestMeta }

type StatusResult struct {
	RecoveryRequired bool
	LiveResources    uint32
	Warnings         []string
}

// TunOptions contains normalized, typed control values only. Backend-specific
// native handles and arbitrary options are not accepted.
type TunOptions struct {
	Name         string
	MTU          uint32
	Addresses    []netip.Prefix
	Routes       []netip.Prefix
	SourceRoutes []SourceRoute
}

type SourceRoute struct {
	Source netip.Prefix
	Route  netip.Prefix
}

type CheckTunRequest struct {
	Meta    RequestMeta
	Options TunOptions
}

type CheckDefaultTunRequest struct {
	Meta    RequestMeta
	Options TunOptions
	Strict  bool
	Rules   []Rule
	// RuleMode is empty/full, include, or exclude. Rules require a mode.
	RuleMode string
	DNSIP    netip.Addr
}

type Rule struct {
	Type  string
	Value string
}

type CheckRuleRequest struct {
	Meta    RequestMeta
	Context string
	Rule    Rule
}

type CompleteRuleRequest struct {
	Meta    RequestMeta
	Context string
	Rule    Rule
	Limit   uint16
}

type ValidationResult struct {
	Normalized bool
	Warnings   []string
}

type RuleCompletionResult struct{ Rules []Rule }

type AddressKind string

const (
	AddressIP     AddressKind = "ip"
	AddressSubnet AddressKind = "subnet"
)

type ReserveAddressRequest struct {
	Meta   RequestMeta
	Kind   AddressKind
	Family string
}

type CreateTunRequest struct {
	Meta     RequestMeta
	Default  bool
	Options  TunOptions
	Strict   bool
	Rules    []Rule
	RuleMode string
	DNSIP    netip.Addr
}

type TunUpdateKind string

const (
	TunUpdateMTU       TunUpdateKind = "mtu"
	TunUpdateAddresses TunUpdateKind = "addresses"
	TunUpdateRoutes    TunUpdateKind = "routes"
	TunUpdateName      TunUpdateKind = "name"
)

type UpdateTunRequest struct {
	Meta      RequestMeta
	Resource  ResourceRef
	Kind      TunUpdateKind
	MTU       uint32
	Addresses []netip.Prefix
	Routes    []netip.Prefix
	Name      string
}

type ConfigureDNSRequest struct {
	Meta     RequestMeta
	Tun      ResourceRef
	Enabled  bool
	Endpoint ResourceRef
}

type NetworkPurpose string

const (
	NetworkOutbound NetworkPurpose = "outbound"
	NetworkLocal    NetworkPurpose = "local"
)

type SocketKind string

const (
	SocketDialTCP      SocketKind = "dial_tcp"
	SocketListenTCP    SocketKind = "listen_tcp"
	SocketDialUDP      SocketKind = "dial_udp"
	SocketListenUDP    SocketKind = "listen_udp"
	SocketPacketUDP    SocketKind = "packet_udp"
	SocketMulticastUDP SocketKind = "multicast_udp"
)

type SocketOptions struct {
	Purpose     NetworkPurpose
	Kind        SocketKind
	Local       netip.AddrPort
	Remote      netip.AddrPort
	Network     string
	Deadline    time.Time
	ReadBuffer  uint32
	WriteBuffer uint32
	KeepAlive   time.Duration
	NoDelay     bool
	Multicast   MulticastOptions
}

type MulticastOptions struct {
	ReuseAddr    bool
	ReusePort    bool
	RecvAnyIf    bool
	ControlFlags uint32
}

type OpenSocketRequest struct {
	Meta    RequestMeta
	Options SocketOptions
}

type InterfaceRequest struct {
	Meta    RequestMeta
	Purpose NetworkPurpose
}

type Interface struct {
	ID              string
	HardwareAddress []byte
	Index           uint32
	Name            string
	MTU             int64
	Flags           uint32
	Addresses       []netip.Prefix
	Multicast       []netip.Addr
}

type InterfaceSnapshot struct{ Interfaces []Interface }

type ResolverSnapshot struct {
	Revision uint64
	Servers  []netip.AddrPort
	Search   []string
}

type CreateMatcherRequest struct {
	Meta RequestMeta
	Rule Rule
}

type FiveTuple struct {
	Protocol string
	Local    netip.AddrPort
	Remote   netip.AddrPort
}

type MatchRequest struct {
	Meta    RequestMeta
	Matcher ResourceRef
	Flow    FiveTuple
}

type MatchResult struct {
	Matched bool
	Quality string
}

type ResourceRequest struct {
	Meta     RequestMeta
	Resource ResourceRef
}

type ReleaseGraphEpochRequest struct {
	Meta       RequestMeta
	GraphEpoch uint64
}

type ResourceResult struct {
	MutationResult
	Resource ResourceRef
	State    ResourceState
}

type ResourceState struct {
	Local              netip.AddrPort
	Remote             netip.AddrPort
	Capabilities       []OperationCapability
	CapabilityRevision uint64
	Revision           uint64
	Up                 bool
	Closed             bool
	RecoveryRequired   bool
	Name               string
	MTU                uint32
	Addresses          []netip.Prefix
	Routes             []netip.Prefix
	Warnings           []string
}

type Outcome string

const (
	OutcomeRejected    Outcome = "rejected"
	OutcomeCommitted   Outcome = "committed"
	OutcomeCompensated Outcome = "compensated"
	OutcomeUncertain   Outcome = "uncertain"
)

// MutationResult is also a retained operation receipt. A canceled call can
// still return committed, compensated, or uncertain after native work starts.
type MutationResult struct {
	OperationID string
	Outcome     Outcome
	Code        string
	Detail      string
	// CurrentResource carries the new generation after a committed update.
	CurrentResource ResourceRef
}

// ConfigWriter owns one bootstrap-selected regular-config target. No request
// can select a path or protected input.
type ConfigWriter interface {
	ReadCurrent(context.Context, ReadConfigRequest) (ConfigDocument, error)
	Replace(context.Context, ReplaceConfigRequest) (ConfigResult, error)
	GetResult(context.Context, OperationResultRequest) (ConfigResult, error)
}

type ReadConfigRequest struct{ Meta RequestMeta }

type ConfigDocument struct {
	Data     []byte
	Revision string
	// Transfer identifies Data sent on the config-transfer lane. It is set in
	// wire replies. Data is used only by direct component calls.
	Transfer uint64
}

type ReplaceConfigRequest struct {
	Meta             RequestMeta
	ExpectedRevision string
	// Transfer identifies opaque bytes sent on the config-transfer lane. It is
	// required on the wire. Data is used only by direct component calls.
	Transfer uint64
	Data     []byte
}

type OperationResultRequest struct {
	Meta        RequestMeta
	OperationID string
}

type ConfigResult struct {
	MutationResult
	Revision string
}

// ConfigRevision returns the content revision used by the config-writer
// contract. Config bytes stay opaque to the writer.
func ConfigRevision(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// CommandControl keeps runtime node commands separate from N-originated
// callbacks. Both identify protected profiles; neither contains argv.
type CommandControl interface {
	RunNodeCommand(context.Context, NodeCommandRequest) (CommandResult, error)
	RunCallback(context.Context, CallbackRequest) (CommandResult, error)
	GetResult(context.Context, OperationResultRequest) (CommandResult, error)
}

type NodeCommandRequest struct {
	Meta      RequestMeta
	CommandID string
	Slots     map[string]string
}

type CallbackRequest struct {
	Meta       RequestMeta
	CallbackID string
	Resource   ResourceRef
	Event      string
}

type CommandResult struct {
	MutationResult
	ExitCode int32
}
