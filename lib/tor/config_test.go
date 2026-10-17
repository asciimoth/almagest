package tornodes

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	sandbox "github.com/asciimoth/gobox"
	tor "github.com/asciimoth/tor-driver"
)

func TestTorDriverConfigMapsDaemonFeatures(t *testing.T) {
	fingerprints := []string{
		strings.Repeat("a", 40),
		strings.Repeat("b", 40),
		strings.Repeat("c", 40),
		strings.Repeat("d", 40),
	}
	config := TorDriverConfig{Driver: TorDriverDaemonConfig{
		MaxProxyConnections: 321, OutboundFailures: "latch",
		BandwidthRate: 1024, BandwidthBurst: 2048,
		ConnectionPadding: "disabled", CircuitPadding: "reduced",
		ReachableORPorts: []uint16{443, 9001}, MaxPendingCircuits: 41, NumCPUs: 3,
		ClientIP: "ipv6", OnionTraffic: "reject",
		EntryNodes: []string{fingerprints[0]}, ExitNodes: []string{fingerprints[1]},
		ExcludeNodes: []string{fingerprints[2]}, ExcludeExitNodes: []string{fingerprints[3]}, StrictNodes: true,
	}}
	got := config.torConfig()
	if got.SandboxSlot != tor.DefaultSandboxSlot || got.SandboxTemplate != tor.DefaultSandboxTemplate || got.TorProgram != tor.DefaultTorProgram {
		t.Fatalf("sandbox selection = %#v", got)
	}
	if got.StateDirectory.Rel != "" || got.LogLevel != tor.LogNotice || !got.ForwardTorLogs {
		t.Fatalf("basic daemon policies = %#v", got)
	}
	if got.MaxProxyConnections != 321 || got.OutboundFailures != tor.LatchOutboundErrors {
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

func TestTorDriverConfigMapsBridgesNetworksAndServices(t *testing.T) {
	fingerprint := strings.Repeat("e", 40)
	config := TorDriverConfig{Driver: TorDriverDaemonConfig{
		UseBridges: true, EnableObfs4: true,
		Bridges: []TorDriverBridge{{
			Transport: "obfs4", Address: "192.0.2.1:443", Fingerprint: fingerprint,
			Obfs4Certificate: "certificate", IAT: "paranoid",
		}},
	}}
	got := config.torConfig()
	if !got.UseBridges || len(got.Transports) != 1 || got.Transports[0].Kind != tor.Obfs4 || got.Transports[0].Program != sandbox.ProgramID("obfs4") {
		t.Fatalf("managed transports = %#v", got.Transports)
	}
	if len(got.Bridges) != 1 || got.Bridges[0].Transport != tor.Obfs4 || got.Bridges[0].Address != "192.0.2.1:443" ||
		got.Bridges[0].Fingerprint != tor.Fingerprint(fingerprint) || got.Bridges[0].Obfs4Certificate != "certificate" || got.Bridges[0].IAT != tor.IATParanoid {
		t.Fatalf("bridges = %#v", got.Bridges)
	}

	network := TorDriverNetworkConfig{
		Circuits: "session", IsolateDestinationAddress: true, IsolateDestinationPort: true,
	}.torConfig()
	if network.Circuits != tor.SessionCircuits || network.Isolation != tor.IsolateDestinationAddress|tor.IsolateDestinationPort {
		t.Fatalf("session network = %#v", network)
	}
	if got := (TorDriverNetworkConfig{Circuits: "connection"}).torConfig(); got.Circuits != tor.IsolateEachConnection || got.Isolation != 0 {
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
	service, err := torServiceConfig(TorDriverServiceConfig{
		Name: "web", Ports: []uint16{80, 443}, MaxStreams: 5, MaxStreamsPolicy: "close",
		AuthorizedClients: []string{string(publicText)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(service.Ports, []uint16{80, 443}) || service.MaxStreams != 5 ||
		service.MaxStreamsPolicy != tor.CloseRendezvousCircuit || service.KeyName != torServiceKeyName("web") || len(service.AuthorizedClients) != 1 {
		t.Fatalf("service = %#v", service)
	}
}

func TestTorDriverConfigRejectsGraphAndPolicyEdges(t *testing.T) {
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
		config TorDriverConfig
		want   string
	}{
		{name: "duplicate outputs", config: TorDriverConfig{Networks: []TorDriverNetworkConfig{{Name: "same"}}, Services: []TorDriverServiceConfig{{Name: "SAME", Ports: []uint16{80}}}}, want: "conflicts"},
		{name: "connection destination isolation", config: TorDriverConfig{Networks: []TorDriverNetworkConfig{{Name: "client", Circuits: "connection", IsolateDestinationAddress: true}}}, want: "requires session"},
		{name: "empty service ports", config: TorDriverConfig{Services: []TorDriverServiceConfig{{Name: "service"}}}, want: "at least one port"},
		{name: "duplicate service ports", config: TorDriverConfig{Services: []TorDriverServiceConfig{{Name: "service", Ports: []uint16{80, 80}}}}, want: "duplicate port"},
		{name: "too many service ports", config: TorDriverConfig{Services: []TorDriverServiceConfig{{Name: "service", Ports: manyPorts}}}, want: "at most 128"},
		{name: "close without stream maximum", config: TorDriverConfig{Services: []TorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, MaxStreamsPolicy: "close"}}}, want: "requires a nonzero"},
		{name: "duplicate authorized client", config: TorDriverConfig{Services: []TorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, AuthorizedClients: []string{string(publicText), strings.ToLower(string(publicText))}}}}, want: "duplicate authorized client"},
		{name: "pending circuit driver maximum", config: TorDriverConfig{Driver: TorDriverDaemonConfig{MaxPendingCircuits: 1025}}, want: "between 0 and 1024"},
		{name: "zero reachable port", config: TorDriverConfig{Driver: TorDriverDaemonConfig{ReachableORPorts: []uint16{0}}}, want: "nonzero"},
		{name: "duplicate reachable port", config: TorDriverConfig{Driver: TorDriverDaemonConfig{ReachableORPorts: []uint16{443, 443}}}, want: "duplicate port"},
		{name: "bridge without mode", config: TorDriverConfig{Driver: TorDriverDaemonConfig{Bridges: []TorDriverBridge{{Address: "192.0.2.1:443"}}}}, want: "require useBridges"},
		{name: "bridge hostname", config: TorDriverConfig{Driver: TorDriverDaemonConfig{UseBridges: true, Bridges: []TorDriverBridge{{Address: "bridge.example:443"}}}}, want: "numeric IP"},
		{name: "plain bridge obfs option", config: TorDriverConfig{Driver: TorDriverDaemonConfig{UseBridges: true, Bridges: []TorDriverBridge{{Address: "192.0.2.1:443", Obfs4Certificate: certificate}}}}, want: "cannot use obfs4"},
		{name: "obfs bridge certificate", config: TorDriverConfig{Driver: TorDriverDaemonConfig{UseBridges: true, EnableObfs4: true, Bridges: []TorDriverBridge{{Transport: "obfs4", Address: "192.0.2.1:443", Fingerprint: fingerprint, Obfs4Certificate: "bad"}}}}, want: "52-byte"},
		{name: "invalid onion alphabet", config: TorDriverConfig{ClientAuthorizations: []TorDriverClientAuthConfig{{Address: strings.Repeat("!", 56)}}}, want: "invalid v3 onion"},
		{name: "invalid client name", config: TorDriverConfig{ClientAuthorizations: []TorDriverClientAuthConfig{{Address: strings.Repeat("a", 56), Name: "contains space"}}}, want: "name must contain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTorDriverConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}
