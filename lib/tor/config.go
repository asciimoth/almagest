package tornodes

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/pasta/pasta"
	tor "github.com/asciimoth/tor-driver"
)

const torDriverConfigurationField = "configuration"

// TorDriverConfig is the complete node-owned Tor configuration. It maps the
// typed tor-driver API without exposing raw torrc text or executable paths.
type TorDriverConfig struct {
	Enabled              bool                        `json:"enabled"`
	Driver               TorDriverDaemonConfig       `json:"driver"`
	Networks             []TorDriverNetworkConfig    `json:"networks"`
	Services             []TorDriverServiceConfig    `json:"services,omitempty"`
	ClientAuthorizations []TorDriverClientAuthConfig `json:"clientAuthorizations,omitempty"`
}

// TorDriverDaemonConfig maps node-controlled Tor network policy. Host process
// policy, process timeouts, and logging policy are not workspace configuration.
type TorDriverDaemonConfig struct {
	MaxProxyConnections int               `json:"maxProxyConnections,omitempty"`
	OutboundFailures    string            `json:"outboundFailures,omitempty"`
	BandwidthRate       uint64            `json:"bandwidthRate,omitempty"`
	BandwidthBurst      uint64            `json:"bandwidthBurst,omitempty"`
	ConnectionPadding   string            `json:"connectionPadding,omitempty"`
	CircuitPadding      string            `json:"circuitPadding,omitempty"`
	ReachableORPorts    []uint16          `json:"reachableORPorts,omitempty"`
	MaxPendingCircuits  uint16            `json:"maxPendingCircuits,omitempty"`
	NumCPUs             uint16            `json:"numCPUs,omitempty"`
	ClientIP            string            `json:"clientIP,omitempty"`
	OnionTraffic        string            `json:"onionTraffic,omitempty"`
	EntryNodes          []string          `json:"entryNodes,omitempty"`
	ExitNodes           []string          `json:"exitNodes,omitempty"`
	ExcludeNodes        []string          `json:"excludeNodes,omitempty"`
	ExcludeExitNodes    []string          `json:"excludeExitNodes,omitempty"`
	StrictNodes         bool              `json:"strictNodes,omitempty"`
	UseBridges          bool              `json:"useBridges,omitempty"`
	EnableObfs4         bool              `json:"enableObfs4,omitempty"`
	Bridges             []TorDriverBridge `json:"bridges,omitempty"`
}

// TorDriverBridge is one plain or obfs4 bridge.
type TorDriverBridge struct {
	Transport        string `json:"transport,omitempty"`
	Address          string `json:"address"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	Obfs4Certificate string `json:"obfs4Certificate,omitempty"`
	IAT              string `json:"iat,omitempty"`
}

// TorDriverNetworkConfig creates one independently isolated client Network
// and one left-directed output port.
type TorDriverNetworkConfig struct {
	Name                      string `json:"name"`
	Circuits                  string `json:"circuits,omitempty"`
	IsolateDestinationAddress bool   `json:"isolateDestinationAddress,omitempty"`
	IsolateDestinationPort    bool   `json:"isolateDestinationPort,omitempty"`
}

// TorDriverServiceConfig creates one v3 onion service and one left-directed
// listen-only Network port. KeySecret selects the Almagest secret that stores
// the expanded Ed25519 service key. An empty selector creates a new secret.
type TorDriverServiceConfig struct {
	Name              string   `json:"name"`
	Ports             []uint16 `json:"ports"`
	MaxStreams        uint16   `json:"maxStreams,omitempty"`
	MaxStreamsPolicy  string   `json:"maxStreamsPolicy,omitempty"`
	KeySecret         string   `json:"keySecret,omitempty"`
	AuthorizedClients []string `json:"authorizedClients,omitempty"`
}

// TorDriverClientAuthConfig installs one client-side v3 authorization key.
// PrivateKeySecret must contain tor-driver's base64 X25519 private-key format.
// An empty selector creates and saves a new node-scoped secret.
type TorDriverClientAuthConfig struct {
	Address          string `json:"address"`
	PrivateKeySecret string `json:"privateKeySecret,omitempty"`
	Name             string `json:"name,omitempty"`
}

func defaultTorDriverConfig() TorDriverConfig {
	return TorDriverConfig{
		Enabled:  true,
		Networks: []TorDriverNetworkConfig{{Name: "default"}},
	}
}

func (c *TorDriverConfig) normalize() {
	c.Driver.normalize()
	for i := range c.Networks {
		c.Networks[i].Name = strings.TrimSpace(c.Networks[i].Name)
		c.Networks[i].Circuits = strings.TrimSpace(c.Networks[i].Circuits)
	}
	for i := range c.Services {
		c.Services[i].Name = strings.TrimSpace(c.Services[i].Name)
		c.Services[i].MaxStreamsPolicy = strings.TrimSpace(c.Services[i].MaxStreamsPolicy)
		c.Services[i].KeySecret = strings.TrimSpace(c.Services[i].KeySecret)
		for j := range c.Services[i].AuthorizedClients {
			c.Services[i].AuthorizedClients[j] = strings.TrimSpace(c.Services[i].AuthorizedClients[j])
		}
	}
	for i := range c.ClientAuthorizations {
		c.ClientAuthorizations[i].Address = strings.TrimSpace(c.ClientAuthorizations[i].Address)
		c.ClientAuthorizations[i].PrivateKeySecret = strings.TrimSpace(c.ClientAuthorizations[i].PrivateKeySecret)
		c.ClientAuthorizations[i].Name = strings.TrimSpace(c.ClientAuthorizations[i].Name)
	}
}

func (c *TorDriverDaemonConfig) normalize() {
	c.OutboundFailures = strings.TrimSpace(c.OutboundFailures)
	c.ConnectionPadding = strings.TrimSpace(c.ConnectionPadding)
	c.CircuitPadding = strings.TrimSpace(c.CircuitPadding)
	c.ClientIP = strings.TrimSpace(c.ClientIP)
	c.OnionTraffic = strings.TrimSpace(c.OnionTraffic)
	for i := range c.Bridges {
		c.Bridges[i].Transport = strings.TrimSpace(c.Bridges[i].Transport)
		c.Bridges[i].Address = strings.TrimSpace(c.Bridges[i].Address)
		c.Bridges[i].Fingerprint = strings.TrimSpace(c.Bridges[i].Fingerprint)
		c.Bridges[i].Obfs4Certificate = strings.TrimSpace(c.Bridges[i].Obfs4Certificate)
		c.Bridges[i].IAT = strings.TrimSpace(c.Bridges[i].IAT)
	}
}

func validateTorDriverConfig(c TorDriverConfig) error {
	c.normalize()
	if err := validateTorDriverDaemonConfig(c.Driver); err != nil {
		return err
	}
	seen := make(map[string]string, len(c.Networks)+len(c.Services))
	for i, network := range c.Networks {
		if err := validateTorOutputName(network.Name, "network", i, seen); err != nil {
			return err
		}
		if network.Circuits != "" && network.Circuits != "session" && network.Circuits != "connection" {
			return fmt.Errorf("network %q: circuits must be session or connection", network.Name)
		}
		if network.Circuits == "connection" && (network.IsolateDestinationAddress || network.IsolateDestinationPort) {
			return fmt.Errorf("network %q: destination isolation requires session circuits", network.Name)
		}
	}
	for i, service := range c.Services {
		if err := validateTorOutputName(service.Name, "service", i, seen); err != nil {
			return err
		}
		if len(service.Ports) == 0 {
			return fmt.Errorf("service %q: at least one port is required", service.Name)
		}
		if len(service.Ports) > 128 {
			return fmt.Errorf("service %q: at most 128 ports are allowed", service.Name)
		}
		ports := append([]uint16(nil), service.Ports...)
		slices.Sort(ports)
		for j, port := range ports {
			if port == 0 {
				return fmt.Errorf("service %q: port must be nonzero", service.Name)
			}
			if j > 0 && ports[j-1] == port {
				return fmt.Errorf("service %q: duplicate port %d", service.Name, port)
			}
		}
		if service.MaxStreamsPolicy != "" && service.MaxStreamsPolicy != "keep" && service.MaxStreamsPolicy != "close" {
			return fmt.Errorf("service %q: maxStreamsPolicy must be keep or close", service.Name)
		}
		if service.MaxStreamsPolicy == "close" && service.MaxStreams == 0 {
			return fmt.Errorf("service %q: close maxStreamsPolicy requires a nonzero maxStreams value", service.Name)
		}
		seenClients := make(map[string]struct{}, len(service.AuthorizedClients))
		for _, key := range service.AuthorizedClients {
			parsed, err := tor.ParseClientAuthorizationPublicKey([]byte(key))
			if err != nil {
				return fmt.Errorf("service %q: authorized client: %w", service.Name, err)
			}
			encoded, err := parsed.MarshalText()
			if err != nil {
				return fmt.Errorf("service %q: authorized client: %w", service.Name, err)
			}
			if _, exists := seenClients[string(encoded)]; exists {
				return fmt.Errorf("service %q: duplicate authorized client", service.Name)
			}
			seenClients[string(encoded)] = struct{}{}
		}
	}
	seenAuth := make(map[string]struct{}, len(c.ClientAuthorizations))
	for i, authorization := range c.ClientAuthorizations {
		address := strings.ToLower(strings.TrimSuffix(authorization.Address, ".onion"))
		if !validTorServiceID(address) {
			return fmt.Errorf("client authorization %d: invalid v3 onion address", i)
		}
		if !validTorClientName(authorization.Name) {
			return fmt.Errorf("client authorization %d: name must contain at most 16 letters, digits, '+', '-', or '_'", i)
		}
		if _, exists := seenAuth[address]; exists {
			return fmt.Errorf("client authorization %d: duplicate onion address", i)
		}
		seenAuth[address] = struct{}{}
	}
	return nil
}

func validateTorOutputName(name, kind string, index int, seen map[string]string) error {
	if name == "" {
		return fmt.Errorf("%s %d: name is empty", kind, index)
	}
	if strings.ContainsAny(name, "[]\r\n") {
		return fmt.Errorf("%s %q: name contains a reserved character", kind, name)
	}
	portName := torNetworkPort(name).Name
	if kind == "service" {
		portName = torServicePort(name).Name
	}
	if err := pasta.ValidatePortName(portName); err != nil {
		return fmt.Errorf("%s %q: name cannot be used in a graph port", kind, name)
	}
	key := strings.ToLower(name)
	if previous := seen[key]; previous != "" {
		return fmt.Errorf("%s %q: output name conflicts with %s", kind, name, previous)
	}
	seen[key] = kind
	return nil
}

func validateTorDriverDaemonConfig(c TorDriverDaemonConfig) error {
	if c.MaxProxyConnections < 0 || c.MaxProxyConnections > 65536 {
		return errors.New("maxProxyConnections must be between 0 and 65536")
	}
	if !oneOf(c.OutboundFailures, "", "retry", "latch") {
		return errors.New("outboundFailures must be retry or latch")
	}
	if !oneOf(c.ConnectionPadding, "", "negotiated", "force", "reduced", "disabled") {
		return errors.New("connectionPadding has an invalid value")
	}
	if !oneOf(c.CircuitPadding, "", "standard", "reduced", "disabled") {
		return errors.New("circuitPadding has an invalid value")
	}
	if !oneOf(c.ClientIP, "", "ipv4", "dual", "prefer-ipv6", "ipv6") {
		return errors.New("clientIP has an invalid value")
	}
	if !oneOf(c.OnionTraffic, "", "allow", "only", "reject") {
		return errors.New("onionTraffic has an invalid value")
	}
	if (c.BandwidthRate == 0) != (c.BandwidthBurst == 0) || c.BandwidthBurst < c.BandwidthRate {
		return errors.New("bandwidthRate and bandwidthBurst must be set together and burst must be at least rate")
	}
	if c.MaxPendingCircuits > 1024 {
		return errors.New("maxPendingCircuits must be between 0 and 1024")
	}
	seenPorts := make(map[uint16]struct{}, len(c.ReachableORPorts))
	for _, port := range c.ReachableORPorts {
		if port == 0 {
			return errors.New("reachableORPorts must contain only nonzero ports")
		}
		if _, exists := seenPorts[port]; exists {
			return fmt.Errorf("reachableORPorts contains duplicate port %d", port)
		}
		seenPorts[port] = struct{}{}
	}
	for _, list := range [][]string{c.EntryNodes, c.ExitNodes, c.ExcludeNodes, c.ExcludeExitNodes} {
		for _, fingerprint := range list {
			decoded, err := hex.DecodeString(strings.TrimSpace(fingerprint))
			if err != nil || len(decoded) != 20 {
				return fmt.Errorf("invalid relay fingerprint %q", fingerprint)
			}
		}
	}
	for i, bridge := range c.Bridges {
		if !oneOf(bridge.Transport, "", "plain", "obfs4") {
			return fmt.Errorf("bridge %d: transport must be plain or obfs4", i)
		}
		if bridge.Transport == "obfs4" && !c.EnableObfs4 {
			return fmt.Errorf("bridge %d: obfs4 transport is not enabled", i)
		}
		if !oneOf(bridge.IAT, "", "disabled", "enabled", "paranoid") {
			return fmt.Errorf("bridge %d: invalid iat value", i)
		}
		endpoint, err := netip.ParseAddrPort(bridge.Address)
		if err != nil || endpoint.Port() == 0 || endpoint.Addr().Zone() != "" || endpoint.Addr().IsUnspecified() {
			return fmt.Errorf("bridge %d: address must be a numeric IP and nonzero port", i)
		}
		if bridge.Fingerprint != "" {
			decoded, decodeErr := hex.DecodeString(bridge.Fingerprint)
			if decodeErr != nil || len(decoded) != 20 {
				return fmt.Errorf("bridge %d: fingerprint must contain 40 hexadecimal characters", i)
			}
		}
		if bridge.Transport == "obfs4" {
			certificate, decodeErr := base64.RawStdEncoding.DecodeString(strings.TrimRight(bridge.Obfs4Certificate, "="))
			if bridge.Fingerprint == "" || decodeErr != nil || len(certificate) != 52 {
				return fmt.Errorf("bridge %d: obfs4 requires a fingerprint and a 52-byte base64 certificate", i)
			}
		} else if bridge.Obfs4Certificate != "" || (bridge.IAT != "" && bridge.IAT != "disabled") {
			return fmt.Errorf("bridge %d: plain bridges cannot use obfs4 options", i)
		}
	}
	if c.UseBridges && len(c.Bridges) == 0 {
		return errors.New("useBridges requires at least one bridge")
	}
	if !c.UseBridges && len(c.Bridges) > 0 {
		return errors.New("bridges require useBridges")
	}
	return nil
}

func torConfigGeneratesSecrets(c TorDriverConfig) bool {
	if !c.Enabled {
		return false
	}
	for _, service := range c.Services {
		if service.KeySecret == "" {
			return true
		}
	}
	for _, authorization := range c.ClientAuthorizations {
		if authorization.PrivateKeySecret == "" {
			return true
		}
	}
	return false
}

func validTorServiceID(value string) bool {
	return len(value) == 56 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyz234567") == ""
}

func validTorClientName(value string) bool {
	if len(value) > 16 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '+' && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func oneOf(value string, choices ...string) bool {
	return slices.Contains(choices, strings.TrimSpace(value))
}

func (c TorDriverConfig) torConfig() tor.Config {
	d := c.Driver
	out := tor.Config{
		SandboxSlot: tor.DefaultSandboxSlot, SandboxTemplate: tor.DefaultSandboxTemplate,
		TorProgram: tor.DefaultTorProgram, LogLevel: tor.LogNotice, ForwardTorLogs: true,
		MaxProxyConnections: d.MaxProxyConnections, BandwidthRate: d.BandwidthRate,
		BandwidthBurst: d.BandwidthBurst, ReachableORPorts: append([]uint16(nil), d.ReachableORPorts...),
		MaxPendingCircuits: d.MaxPendingCircuits, NumCPUs: d.NumCPUs,
		StrictNodes: d.StrictNodes, UseBridges: d.UseBridges,
	}
	if d.OutboundFailures == "latch" {
		out.OutboundFailures = tor.LatchOutboundErrors
	}
	switch d.ConnectionPadding {
	case "force":
		out.ConnectionPadding = tor.ForceConnectionPadding
	case "reduced":
		out.ConnectionPadding = tor.ReducedConnectionPadding
	case "disabled":
		out.ConnectionPadding = tor.DisableConnectionPadding
	}
	switch d.CircuitPadding {
	case "reduced":
		out.CircuitPadding = tor.ReducedCircuitPadding
	case "disabled":
		out.CircuitPadding = tor.DisableCircuitPadding
	}
	switch d.ClientIP {
	case "dual":
		out.ClientIP = tor.ClientDualStack
	case "prefer-ipv6":
		out.ClientIP = tor.ClientPreferIPv6
	case "ipv6":
		out.ClientIP = tor.ClientIPv6Only
	}
	switch d.OnionTraffic {
	case "only":
		out.OnionTraffic = tor.OnionTrafficOnly
	case "reject":
		out.OnionTraffic = tor.RejectOnionTraffic
	}
	out.EntryNodes = torFingerprints(d.EntryNodes)
	out.ExitNodes = torFingerprints(d.ExitNodes)
	out.ExcludeNodes = torFingerprints(d.ExcludeNodes)
	out.ExcludeExitNodes = torFingerprints(d.ExcludeExitNodes)
	if d.EnableObfs4 {
		out.Transports = []tor.TransportConfig{{Kind: tor.Obfs4, Program: sandbox.ProgramID("obfs4")}}
	}
	for _, bridge := range d.Bridges {
		item := tor.Bridge{Address: bridge.Address, Fingerprint: tor.Fingerprint(bridge.Fingerprint), Obfs4Certificate: bridge.Obfs4Certificate}
		if bridge.Transport == "obfs4" {
			item.Transport = tor.Obfs4
		}
		switch bridge.IAT {
		case "enabled":
			item.IAT = tor.IATEnabled
		case "paranoid":
			item.IAT = tor.IATParanoid
		}
		out.Bridges = append(out.Bridges, item)
	}
	return out
}

func torFingerprints(values []string) []tor.Fingerprint {
	out := make([]tor.Fingerprint, len(values))
	for i, value := range values {
		out[i] = tor.Fingerprint(strings.TrimSpace(value))
	}
	return out
}

func (c TorDriverNetworkConfig) torConfig() tor.NetworkConfig {
	out := tor.NetworkConfig{}
	if c.Circuits == "connection" {
		out.Circuits = tor.IsolateEachConnection
	}
	if c.IsolateDestinationAddress {
		out.Isolation |= tor.IsolateDestinationAddress
	}
	if c.IsolateDestinationPort {
		out.Isolation |= tor.IsolateDestinationPort
	}
	return out
}
