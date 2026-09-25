package experimentalnodes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/asciimoth/pasta/pasta"
	tor "github.com/asciimoth/tor-driver"
)

const xTorDriverConfigurationField = "configuration"

// XTorDriverHostConfig contains host-controlled Tor process settings. Keep
// this value in protected daemon configuration. Empty values use tor-driver
// defaults. Timeout values use Go duration syntax, such as "30s" or "10m".
type XTorDriverHostConfig struct {
	TorExecutable       string              `json:"TorExecutable,omitempty"`
	Obfs4Executable     string              `json:"Obfs4Executable,omitempty"`
	TempRoot            string              `json:"TempRoot,omitempty"`
	StateRoot           string              `json:"StateRoot,omitempty"`
	Sandbox             string              `json:"Sandbox,omitempty"`
	StartupTimeout      string              `json:"StartupTimeout,omitempty"`
	CommandTimeout      string              `json:"CommandTimeout,omitempty"`
	DialTimeout         string              `json:"DialTimeout,omitempty"`
	ShutdownTimeout     string              `json:"ShutdownTimeout,omitempty"`
	MaxCircuitDirtiness string              `json:"MaxCircuitDirtiness,omitempty"`
	CircuitBuildTimeout string              `json:"CircuitBuildTimeout,omitempty"`
	Identity            *XTorDriverIdentity `json:"Identity,omitempty"`
}

// XTorDriverIdentity selects the non-root Linux identity used for Tor and its
// managed transports. When it is absent, tor-driver keeps a non-root daemon
// identity or selects the host nobody account for a root daemon.
type XTorDriverIdentity struct {
	UID uint32 `json:"UID"`
	GID uint32 `json:"GID"`
}

// XTorDriverConfig is the complete node-owned Tor configuration. It maps the
// typed tor-driver API without exposing raw torrc text or executable paths.
type XTorDriverConfig struct {
	Enabled              bool                         `json:"enabled"`
	Driver               XTorDriverDaemonConfig       `json:"driver"`
	Networks             []XTorDriverNetworkConfig    `json:"networks"`
	Services             []XTorDriverServiceConfig    `json:"services,omitempty"`
	ClientAuthorizations []XTorDriverClientAuthConfig `json:"clientAuthorizations,omitempty"`
}

// XTorDriverDaemonConfig maps node-controlled Tor network policy. Host process
// policy, process timeouts, and logging policy are not workspace configuration.
type XTorDriverDaemonConfig struct {
	MaxProxyConnections int                `json:"maxProxyConnections,omitempty"`
	OutboundFailures    string             `json:"outboundFailures,omitempty"`
	BandwidthRate       uint64             `json:"bandwidthRate,omitempty"`
	BandwidthBurst      uint64             `json:"bandwidthBurst,omitempty"`
	ConnectionPadding   string             `json:"connectionPadding,omitempty"`
	CircuitPadding      string             `json:"circuitPadding,omitempty"`
	ReachableORPorts    []uint16           `json:"reachableORPorts,omitempty"`
	MaxPendingCircuits  uint16             `json:"maxPendingCircuits,omitempty"`
	NumCPUs             uint16             `json:"numCPUs,omitempty"`
	ClientIP            string             `json:"clientIP,omitempty"`
	OnionTraffic        string             `json:"onionTraffic,omitempty"`
	EntryNodes          []string           `json:"entryNodes,omitempty"`
	ExitNodes           []string           `json:"exitNodes,omitempty"`
	ExcludeNodes        []string           `json:"excludeNodes,omitempty"`
	ExcludeExitNodes    []string           `json:"excludeExitNodes,omitempty"`
	StrictNodes         bool               `json:"strictNodes,omitempty"`
	UseBridges          bool               `json:"useBridges,omitempty"`
	EnableObfs4         bool               `json:"enableObfs4,omitempty"`
	Bridges             []XTorDriverBridge `json:"bridges,omitempty"`
}

// XTorDriverBridge is one plain or obfs4 bridge.
type XTorDriverBridge struct {
	Transport        string `json:"transport,omitempty"`
	Address          string `json:"address"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	Obfs4Certificate string `json:"obfs4Certificate,omitempty"`
	IAT              string `json:"iat,omitempty"`
}

// XTorDriverNetworkConfig creates one independently isolated client Network
// and one left-directed output port.
type XTorDriverNetworkConfig struct {
	Name                      string `json:"name"`
	Circuits                  string `json:"circuits,omitempty"`
	IsolateDestinationAddress bool   `json:"isolateDestinationAddress,omitempty"`
	IsolateDestinationPort    bool   `json:"isolateDestinationPort,omitempty"`
}

// XTorDriverServiceConfig creates one v3 onion service and one left-directed
// listen-only Network port. KeySecret selects the Almagest secret that stores
// the expanded Ed25519 service key. An empty selector creates a new secret.
type XTorDriverServiceConfig struct {
	Name              string   `json:"name"`
	Ports             []uint16 `json:"ports"`
	MaxStreams        uint16   `json:"maxStreams,omitempty"`
	MaxStreamsPolicy  string   `json:"maxStreamsPolicy,omitempty"`
	KeySecret         string   `json:"keySecret,omitempty"`
	AuthorizedClients []string `json:"authorizedClients,omitempty"`
}

// XTorDriverClientAuthConfig installs one client-side v3 authorization key.
// PrivateKeySecret must contain tor-driver's base64 X25519 private-key format.
// An empty selector creates and saves a new node-scoped secret.
type XTorDriverClientAuthConfig struct {
	Address          string `json:"address"`
	PrivateKeySecret string `json:"privateKeySecret,omitempty"`
	Name             string `json:"name,omitempty"`
}

func defaultXTorDriverConfig() XTorDriverConfig {
	return XTorDriverConfig{
		Enabled:  true,
		Networks: []XTorDriverNetworkConfig{{Name: "default"}},
	}
}

func (c *XTorDriverConfig) normalize() {
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

func (c *XTorDriverDaemonConfig) normalize() {
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

func validateXTorDriverConfig(c XTorDriverConfig) error {
	c.normalize()
	if err := validateXTorDriverDaemonConfig(c.Driver); err != nil {
		return err
	}
	seen := make(map[string]string, len(c.Networks)+len(c.Services))
	for i, network := range c.Networks {
		if err := validateXTorOutputName(network.Name, "network", i, seen); err != nil {
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
		if err := validateXTorOutputName(service.Name, "service", i, seen); err != nil {
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
		if !validXTorServiceID(address) {
			return fmt.Errorf("client authorization %d: invalid v3 onion address", i)
		}
		if !validXTorClientName(authorization.Name) {
			return fmt.Errorf("client authorization %d: name must contain at most 16 letters, digits, '+', '-', or '_'", i)
		}
		if _, exists := seenAuth[address]; exists {
			return fmt.Errorf("client authorization %d: duplicate onion address", i)
		}
		seenAuth[address] = struct{}{}
	}
	return nil
}

func validateXTorOutputName(name, kind string, index int, seen map[string]string) error {
	if name == "" {
		return fmt.Errorf("%s %d: name is empty", kind, index)
	}
	if strings.ContainsAny(name, "[]\r\n") {
		return fmt.Errorf("%s %q: name contains a reserved character", kind, name)
	}
	portName := xTorNetworkPort(name).Name
	if kind == "service" {
		portName = xTorServicePort(name).Name
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

func validateXTorDriverDaemonConfig(c XTorDriverDaemonConfig) error {
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

func validateXTorHostCompatibility(c XTorDriverConfig, host XTorDriverHostConfig) error {
	if host.Identity != nil && (host.Identity.UID == 0 || host.Identity.GID == 0) {
		return errors.New("protected Tor identity requires nonzero UID and GID")
	}
	for field, path := range map[string]string{
		"TorExecutable": host.TorExecutable,
		"TempRoot":      host.TempRoot,
		"StateRoot":     host.StateRoot,
	} {
		if path != "" && (!filepath.IsAbs(path) || !safeXTorText(path)) {
			return fmt.Errorf("protected Tor %s must be an absolute path without control characters", field)
		}
	}
	if host.Obfs4Executable != "" && (!filepath.IsAbs(host.Obfs4Executable) || !safeXTorText(host.Obfs4Executable) || strings.ContainsAny(host.Obfs4Executable, " \t\"")) {
		return errors.New("protected Tor Obfs4Executable must be an absolute path without whitespace, quotes, or control characters")
	}
	for _, value := range []struct {
		name  string
		value string
		zero  bool
	}{
		{"StartupTimeout", host.StartupTimeout, false},
		{"CommandTimeout", host.CommandTimeout, false},
		{"DialTimeout", host.DialTimeout, false},
		{"ShutdownTimeout", host.ShutdownTimeout, false},
		{"MaxCircuitDirtiness", host.MaxCircuitDirtiness, false},
		{"CircuitBuildTimeout", host.CircuitBuildTimeout, true},
	} {
		if strings.TrimSpace(value.value) == "" {
			continue
		}
		duration, err := time.ParseDuration(value.value)
		if err != nil {
			return fmt.Errorf("tor %s: %w", value.name, err)
		}
		if (duration == 0 && !value.zero) || duration < 0 || (duration > 0 && duration < time.Second) || duration > 24*time.Hour {
			return fmt.Errorf("tor %s must be between 1s and 24h", value.name)
		}
	}
	sandbox := strings.TrimSpace(host.Sandbox)
	if !oneOf(sandbox, "", "dynamic", "linux") {
		return errors.New("protected Tor sandbox must be dynamic or linux")
	}
	if sandbox == "linux" && len(c.Services) > 0 {
		return errors.New("protected Tor linux sandbox does not support dynamic onion services")
	}
	if sandbox == "linux" && c.Driver.EnableObfs4 {
		return errors.New("protected Tor linux sandbox does not support managed transports")
	}
	return nil
}

func xTorConfigGeneratesSecrets(c XTorDriverConfig) bool {
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

func validXTorServiceID(value string) bool {
	return len(value) == 56 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyz234567") == ""
}

func validXTorClientName(value string) bool {
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

func safeXTorText(value string) bool {
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}

func oneOf(value string, choices ...string) bool {
	return slices.Contains(choices, strings.TrimSpace(value))
}

func (c XTorDriverConfig) torConfig(host XTorDriverHostConfig, nodeName string) (tor.Config, error) {
	d := c.Driver
	host.Sandbox = strings.TrimSpace(host.Sandbox)
	if !oneOf(host.Sandbox, "", "dynamic", "linux") {
		return tor.Config{}, errors.New("protected Tor sandbox must be dynamic or linux")
	}
	parseDuration := func(field, value string) (time.Duration, error) {
		if strings.TrimSpace(value) == "" {
			return 0, nil
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("tor %s: %w", field, err)
		}
		return parsed, nil
	}
	startup, err := parseDuration("StartupTimeout", host.StartupTimeout)
	if err != nil {
		return tor.Config{}, err
	}
	command, err := parseDuration("CommandTimeout", host.CommandTimeout)
	if err != nil {
		return tor.Config{}, err
	}
	dial, err := parseDuration("DialTimeout", host.DialTimeout)
	if err != nil {
		return tor.Config{}, err
	}
	shutdown, err := parseDuration("ShutdownTimeout", host.ShutdownTimeout)
	if err != nil {
		return tor.Config{}, err
	}
	dirtiness, err := parseDuration("MaxCircuitDirtiness", host.MaxCircuitDirtiness)
	if err != nil {
		return tor.Config{}, err
	}
	build, err := parseDuration("CircuitBuildTimeout", host.CircuitBuildTimeout)
	if err != nil {
		return tor.Config{}, err
	}
	out := tor.Config{
		TorExecutable: host.TorExecutable, TempRoot: host.TempRoot,
		LogLevel: tor.LogNotice, ForwardTorLogs: true, StartupTimeout: startup,
		CommandTimeout: command, DialTimeout: dial, ShutdownTimeout: shutdown,
		MaxProxyConnections: d.MaxProxyConnections, MaxCircuitDirtiness: dirtiness,
		CircuitBuildTimeout: build, BandwidthRate: d.BandwidthRate,
		BandwidthBurst: d.BandwidthBurst, ReachableORPorts: append([]uint16(nil), d.ReachableORPorts...),
		MaxPendingCircuits: d.MaxPendingCircuits, NumCPUs: d.NumCPUs,
		StrictNodes: d.StrictNodes, UseBridges: d.UseBridges,
	}
	if host.StateRoot != "" {
		sum := sha256.Sum256([]byte(nodeName))
		out.StateDirectory = filepath.Join(host.StateRoot, "node-"+hex.EncodeToString(sum[:8]))
	}
	if host.Identity != nil {
		out.Identity = &tor.Identity{UID: host.Identity.UID, GID: host.Identity.GID}
	}
	if host.Sandbox == "linux" {
		out.Sandbox = tor.LinuxSandbox
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
		out.Transports = []tor.TransportConfig{{Kind: tor.Obfs4, Executable: host.Obfs4Executable}}
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
	return out, nil
}

func torFingerprints(values []string) []tor.Fingerprint {
	out := make([]tor.Fingerprint, len(values))
	for i, value := range values {
		out[i] = tor.Fingerprint(strings.TrimSpace(value))
	}
	return out
}

func (c XTorDriverNetworkConfig) torConfig() tor.NetworkConfig {
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
