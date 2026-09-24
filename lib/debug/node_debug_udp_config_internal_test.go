package debugnodes

import (
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
)

func TestUDPPingConfigStringUpdatesAndNormalization(t *testing.T) {
	ports := udpPingConfigPorts{
		network: 1, socketMode: 2, localHost: 3, localPort: 4,
		remoteHost: 5, remotePort: 6, payload: 7, timeout: 8,
	}
	cfg := udpPingConfig{
		network: "udp4", socketMode: udpSocketListen,
		localHost: "127.0.0.1", localPort: 1000,
		remoteHost: "127.0.0.1", remotePort: 2000,
		payload: "old", timeout: time.Second,
	}
	tests := []struct {
		port  uint64
		value string
	}{
		{ports.network, "udp6"},
		{ports.socketMode, udpSocketDial},
		{ports.localHost, "::1"},
		{ports.localPort, "3000"},
		{ports.remoteHost, "2001:db8::1"},
		{ports.remotePort, "4000"},
		{ports.payload, "new"},
		{ports.timeout, "250"},
	}
	for _, test := range tests {
		if !applyUDPPingConfigString(&cfg, ports, test.port, test.value, false) {
			t.Fatalf("port %d did not change config", test.port)
		}
	}
	if cfg.network != "udp6" || cfg.socketMode != udpSocketDial || cfg.localHost != "::1" ||
		cfg.localPort != 3000 || cfg.remoteHost != "2001:db8::1" || cfg.remotePort != 4000 ||
		cfg.payload != "new" || cfg.timeout != 250*time.Millisecond {
		t.Fatalf("updated config = %#v", cfg)
	}
	if applyUDPPingConfigString(&cfg, ports, 99, "ignored", false) {
		t.Fatal("unknown port changed config")
	}
	if hostForUDPNetwork("udp6") != "::1" || hostForUDPNetwork("udp4") != defaultUDPPingHost {
		t.Fatal("hostForUDPNetwork returned an unexpected host")
	}
}

func TestUDPPingConfigReadFormAndSave(t *testing.T) {
	cfg := readUDPPingConfig(configer.NewMemory(map[string]any{
		"network": "invalid", "socket_mode": "invalid",
		"local_host": "", "local_port": -1,
		"remote_host": "", "remote_port": 70000,
		"payload": "", "timeout_ms": 0,
	}), true)
	if cfg.network != defaultUDPPingNetwork || cfg.socketMode != udpSocketListen ||
		cfg.localPort != defaultUDPPingPort || cfg.remotePort != defaultUDPPingPort ||
		cfg.payload != defaultUDPPingPayload || cfg.timeout != defaultUDPPingTimeoutMS*time.Millisecond {
		t.Fatalf("normalized config = %#v", cfg)
	}
	if !applyUDPPingForm(&cfg, map[string]any{
		"network": "udp6", "socket_mode": udpSocketDial,
		"local_host": "::1", "local_port": 100,
		"remote_host": "::2", "remote_port": 200,
		"payload": "hello", "timeout_ms": 300,
	}, false) {
		t.Fatal("form did not change config")
	}
	if applyUDPPingForm(&cfg, map[string]any{}, false) {
		t.Fatal("empty form changed config")
	}
	stored := configer.NewMemory(map[string]any{"Class": "keep", "obsolete": true})
	if err := saveUDPPingConfig(stored, cfg); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"network": "udp6", "socket_mode": udpSocketDial,
		"local_host": "::1", "remote_host": "::2", "payload": "hello",
	} {
		if got := configer.String(stored, configer.Path{key}, ""); got != want {
			t.Fatalf("saved %s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int{"local_port": 100, "remote_port": 200, "timeout_ms": 300} {
		if got := configer.Int(stored, configer.Path{key}, -1); got != want {
			t.Fatalf("saved %s = %d, want %d", key, got, want)
		}
	}
}

func TestUDPPingNodeConfigPortAdapters(t *testing.T) {
	ports := udpPingConfigPorts{network: 1, socketMode: 2, localHost: 3, localPort: 4, remoteHost: 5, remotePort: 6, payload: 7, timeout: 8}
	server := &udpPingServerNode{cfgp: ports, cfg: udpPingConfig{network: "udp4", socketMode: udpSocketListen, localHost: "127.0.0.1", localPort: 1, remoteHost: "127.0.0.1", remotePort: 2, payload: "x", timeout: time.Second}}
	client := &udpPingClientNode{cfgp: ports, cfg: server.cfg}
	if !server.isConfigPort(1) || server.isConfigPort(7) || !client.isConfigPort(8) || client.isConfigPort(99) {
		t.Fatal("config port classification is incorrect")
	}
	if !server.applyConfigString(3, "localhost") || !client.applyConfigString(7, "changed") {
		t.Fatal("node config adapter did not apply a value")
	}
}
