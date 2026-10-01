package experimentalnodes

import (
	"encoding/base64"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tor "github.com/asciimoth/tor-driver"
)

func TestXTorDriverConfigMapsDaemonFeatures(t *testing.T) {
	fingerprints := []string{
		strings.Repeat("a", 40),
		strings.Repeat("b", 40),
		strings.Repeat("c", 40),
		strings.Repeat("d", 40),
	}
	config := XTorDriverConfig{Driver: XTorDriverDaemonConfig{
		MaxProxyConnections: 321, OutboundFailures: "latch",
		BandwidthRate: 1024, BandwidthBurst: 2048,
		ConnectionPadding: "disabled", CircuitPadding: "reduced",
		ReachableORPorts: []uint16{443, 9001}, MaxPendingCircuits: 41, NumCPUs: 3,
		ClientIP: "ipv6", OnionTraffic: "reject",
		EntryNodes: []string{fingerprints[0]}, ExitNodes: []string{fingerprints[1]},
		ExcludeNodes: []string{fingerprints[2]}, ExcludeExitNodes: []string{fingerprints[3]}, StrictNodes: true,
	}}
	host := XTorDriverHostConfig{
		TorExecutable: "/opt/tor", TempRoot: "/tmp/tor", StateRoot: "/var/lib/tor",
		Sandbox: "linux", StartupTimeout: "31s", CommandTimeout: "11s",
		DialTimeout: "91s", ShutdownTimeout: "12s", MaxCircuitDirtiness: "11m", CircuitBuildTimeout: "45s",
		Identity: &XTorDriverIdentity{UID: 123, GID: 456},
	}

	got, err := config.torConfig(host, "Tor node")
	if err != nil {
		t.Fatal(err)
	}
	if got.TorExecutable != host.TorExecutable || got.TempRoot != host.TempRoot {
		t.Fatalf("host paths = %q, %q", got.TorExecutable, got.TempRoot)
	}
	if !strings.HasPrefix(got.StateDirectory, filepath.Join(host.StateRoot, "node-")) {
		t.Fatalf("state directory = %q", got.StateDirectory)
	}
	if got.Identity == nil || got.Identity.UID != 123 || got.Identity.GID != 456 {
		t.Fatalf("identity = %#v", got.Identity)
	}
	if got.Sandbox != tor.LinuxSandbox || got.LogLevel != tor.LogNotice || !got.ForwardTorLogs {
		t.Fatalf("basic daemon policies = %#v", got)
	}
	if got.StartupTimeout != 31*time.Second || got.CommandTimeout != 11*time.Second ||
		got.DialTimeout != 91*time.Second || got.ShutdownTimeout != 12*time.Second {
		t.Fatalf("timeouts = %s, %s, %s, %s", got.StartupTimeout, got.CommandTimeout, got.DialTimeout, got.ShutdownTimeout)
	}
	if got.MaxProxyConnections != 321 || got.OutboundFailures != tor.LatchOutboundErrors ||
		got.MaxCircuitDirtiness != 11*time.Minute || got.CircuitBuildTimeout != 45*time.Second {
		t.Fatalf("connection limits = %#v", got)
	}
	if got.BandwidthRate != 1024 || got.BandwidthBurst != 2048 ||
		got.ConnectionPadding != tor.DisableConnectionPadding || got.CircuitPadding != tor.ReducedCircuitPadding {
		t.Fatalf("traffic policies = %#v", got)
	}
	if !reflect.DeepEqual(got.ReachableORPorts, []uint16{443, 9001}) || got.MaxPendingCircuits != 41 || got.NumCPUs != 3 {
		t.Fatalf("relay and CPU limits = %#v", got)
	}
	if got.ClientIP != tor.ClientIPv6Only || got.OnionTraffic != tor.RejectOnionTraffic || !got.StrictNodes {
		t.Fatalf("client policies = %#v", got)
	}
	if !reflect.DeepEqual(got.EntryNodes, []tor.Fingerprint{tor.Fingerprint(fingerprints[0])}) ||
		!reflect.DeepEqual(got.ExitNodes, []tor.Fingerprint{tor.Fingerprint(fingerprints[1])}) ||
		!reflect.DeepEqual(got.ExcludeNodes, []tor.Fingerprint{tor.Fingerprint(fingerprints[2])}) ||
		!reflect.DeepEqual(got.ExcludeExitNodes, []tor.Fingerprint{tor.Fingerprint(fingerprints[3])}) {
		t.Fatalf("relay selection = %#v", got)
	}
}

func TestXTorDriverConfigMapsBridgesNetworksAndServices(t *testing.T) {
	fingerprint := strings.Repeat("e", 40)
	config := XTorDriverConfig{Driver: XTorDriverDaemonConfig{
		UseBridges: true, EnableObfs4: true,
		Bridges: []XTorDriverBridge{{
			Transport: "obfs4", Address: "192.0.2.1:443", Fingerprint: fingerprint,
			Obfs4Certificate: "certificate", IAT: "paranoid",
		}},
	}}
	host := XTorDriverHostConfig{Obfs4Executable: "/opt/lyrebird"}
	got, err := config.torConfig(host, "bridges")
	if err != nil {
		t.Fatal(err)
	}
	if !got.UseBridges || len(got.Transports) != 1 || got.Transports[0].Kind != tor.Obfs4 || got.Transports[0].Executable != host.Obfs4Executable {
		t.Fatalf("managed transports = %#v", got.Transports)
	}
	if len(got.Bridges) != 1 || got.Bridges[0].Transport != tor.Obfs4 || got.Bridges[0].Address != "192.0.2.1:443" ||
		got.Bridges[0].Fingerprint != tor.Fingerprint(fingerprint) || got.Bridges[0].Obfs4Certificate != "certificate" || got.Bridges[0].IAT != tor.IATParanoid {
		t.Fatalf("bridges = %#v", got.Bridges)
	}

	network := XTorDriverNetworkConfig{
		Circuits: "session", IsolateDestinationAddress: true, IsolateDestinationPort: true,
	}.torConfig()
	if network.Circuits != tor.SessionCircuits || network.Isolation != tor.IsolateDestinationAddress|tor.IsolateDestinationPort {
		t.Fatalf("session network = %#v", network)
	}
	if got := (XTorDriverNetworkConfig{Circuits: "connection"}).torConfig(); got.Circuits != tor.IsolateEachConnection || got.Isolation != 0 {
		t.Fatalf("per-connection network = %#v", got)
	}

	private, err := tor.NewClientAuthorizationPrivateKey([]byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := (tor.ClientAuthorization{PrivateKey: private}).PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	publicText, err := public.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	service, err := xTorServiceConfig(XTorDriverServiceConfig{
		Name: "web", Ports: []uint16{80, 443}, MaxStreams: 5, MaxStreamsPolicy: "close",
		AuthorizedClients: []string{string(publicText)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(service.Ports, []uint16{80, 443}) || service.MaxStreams != 5 ||
		service.MaxStreamsPolicy != tor.CloseRendezvousCircuit || service.KeyName != xTorServiceKeyName("web") || len(service.AuthorizedClients) != 1 {
		t.Fatalf("service = %#v", service)
	}
}

func TestXTorDriverConfigRejectsGraphAndPolicyEdges(t *testing.T) {
	private, err := tor.NewClientAuthorizationPrivateKey([]byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := (tor.ClientAuthorization{PrivateKey: private}).PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	publicText, err := public.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	manyPorts := make([]uint16, 129)
	for i := range manyPorts {
		manyPorts[i] = uint16(i + 1)
	}
	fingerprint := strings.Repeat("a", 40)
	certificate := base64.RawStdEncoding.EncodeToString(make([]byte, 52))
	tests := []struct {
		name   string
		config XTorDriverConfig
		want   string
	}{
		{name: "duplicate outputs", config: XTorDriverConfig{Networks: []XTorDriverNetworkConfig{{Name: "same"}}, Services: []XTorDriverServiceConfig{{Name: "SAME", Ports: []uint16{80}}}}, want: "conflicts"},
		{name: "connection destination isolation", config: XTorDriverConfig{Networks: []XTorDriverNetworkConfig{{Name: "client", Circuits: "connection", IsolateDestinationAddress: true}}}, want: "requires session"},
		{name: "empty service ports", config: XTorDriverConfig{Services: []XTorDriverServiceConfig{{Name: "service"}}}, want: "at least one port"},
		{name: "duplicate service ports", config: XTorDriverConfig{Services: []XTorDriverServiceConfig{{Name: "service", Ports: []uint16{80, 80}}}}, want: "duplicate port"},
		{name: "too many service ports", config: XTorDriverConfig{Services: []XTorDriverServiceConfig{{Name: "service", Ports: manyPorts}}}, want: "at most 128"},
		{name: "close without stream maximum", config: XTorDriverConfig{Services: []XTorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, MaxStreamsPolicy: "close"}}}, want: "requires a nonzero"},
		{name: "duplicate authorized client", config: XTorDriverConfig{Services: []XTorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, AuthorizedClients: []string{string(publicText), strings.ToLower(string(publicText))}}}}, want: "duplicate authorized client"},
		{name: "pending circuit driver maximum", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{MaxPendingCircuits: 1025}}, want: "between 0 and 1024"},
		{name: "zero reachable port", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{ReachableORPorts: []uint16{0}}}, want: "nonzero"},
		{name: "duplicate reachable port", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{ReachableORPorts: []uint16{443, 443}}}, want: "duplicate port"},
		{name: "bridge without mode", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{Bridges: []XTorDriverBridge{{Address: "192.0.2.1:443"}}}}, want: "require useBridges"},
		{name: "bridge hostname", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{UseBridges: true, Bridges: []XTorDriverBridge{{Address: "bridge.example:443"}}}}, want: "numeric IP"},
		{name: "plain bridge obfs option", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{UseBridges: true, Bridges: []XTorDriverBridge{{Address: "192.0.2.1:443", Obfs4Certificate: certificate}}}}, want: "cannot use obfs4"},
		{name: "obfs bridge certificate", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{UseBridges: true, EnableObfs4: true, Bridges: []XTorDriverBridge{{Transport: "obfs4", Address: "192.0.2.1:443", Fingerprint: fingerprint, Obfs4Certificate: "bad"}}}}, want: "52-byte"},
		{name: "invalid onion alphabet", config: XTorDriverConfig{ClientAuthorizations: []XTorDriverClientAuthConfig{{Address: strings.Repeat("!", 56)}}}, want: "invalid v3 onion"},
		{name: "invalid client name", config: XTorDriverConfig{ClientAuthorizations: []XTorDriverClientAuthConfig{{Address: strings.Repeat("a", 56), Name: "contains space"}}}, want: "name must contain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateXTorDriverConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestXTorDriverRejectsInvalidProtectedProcessPolicy(t *testing.T) {
	config := defaultXTorDriverConfig()
	for _, test := range []struct {
		name string
		host XTorDriverHostConfig
		want string
	}{
		{name: "sandbox", host: XTorDriverHostConfig{Sandbox: "unknown"}, want: "sandbox"},
		{name: "timeout", host: XTorDriverHostConfig{CommandTimeout: "later"}, want: "CommandTimeout"},
		{name: "short timeout", host: XTorDriverHostConfig{CommandTimeout: "1ms"}, want: "between 1s and 24h"},
		{name: "root identity", host: XTorDriverHostConfig{Identity: &XTorDriverIdentity{UID: 0, GID: 1}}, want: "nonzero UID and GID"},
		{name: "relative state root", host: XTorDriverHostConfig{StateRoot: "state"}, want: "absolute path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateXTorHostCompatibility(config, test.host)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("host compatibility error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestXTorDriverRejectsProtectedSandboxFeatureConflicts(t *testing.T) {
	for _, test := range []struct {
		name   string
		config XTorDriverConfig
		want   string
	}{
		{name: "onion service", config: XTorDriverConfig{Services: []XTorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, KeySecret: "key"}}}, want: "dynamic onion services"},
		{name: "managed transport", config: XTorDriverConfig{Driver: XTorDriverDaemonConfig{EnableObfs4: true}}, want: "managed transports"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateXTorHostCompatibility(test.config, XTorDriverHostConfig{Sandbox: "linux"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("host compatibility error = %v, want %q", err, test.want)
			}
		})
	}
}
