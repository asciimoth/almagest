package yggdrasilnodes

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestFeatureConfigValidation(t *testing.T) {
	valid := readConfig(configer.NewMemory(map[string]any{
		"multicastInterfaces": `[{"regex":"^eth[0-9]+$","beacon":true,"listen":true,"port":0,"priority":1}]`,
		"autoPeerEnabled":     true, "autoPeerSources": "BUILTIN", "autoPeerCountries": "Georgia", "autoPeerSchemes": "TLS",
		"autoPeerFetchInterval": "30m", "autoPeerCheckInterval": "2s",
		"jumperEnabled": true, "jumperAddresses": "tls://peer.example:1234",
		"jumperCheckInterval": "1s", "jumperLinkTimeout": "2s",
		"nodeInfo": `{"service":"example"}`, "nodeInfoPrivacy": true,
	}))
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	if valid.autoPeerCountries != "georgia" || valid.autoPeerSchemes != "tls" {
		t.Fatalf("filters were not normalized: %+v", valid)
	}
	for _, test := range []struct {
		name   string
		change map[string]any
	}{
		{"bad multicast JSON", map[string]any{"multicastInterfaces": "["}},
		{"bad multicast regex", map[string]any{"multicastInterfaces": `[{"regex":"[","beacon":true}]`}},
		{"bad multicast port", map[string]any{"multicastInterfaces": `[{"regex":".*","port":65536}]`}},
		{"unknown multicast field", map[string]any{"multicastInterfaces": `[{"regex":".*","password":"plain"}]`}},
		{"bad source", map[string]any{"autoPeerSources": "file:///tmp/peers"}},
		{"bad scheme", map[string]any{"autoPeerSchemes": "quic"}},
		{"negative threshold", map[string]any{"autoPeerMinimumConnected": -1}},
		{"bad interval", map[string]any{"autoPeerFetchInterval": "tomorrow"}},
		{"bad jumper address", map[string]any{"jumperAddresses": "ws://peer:1234"}},
		{"bad NodeInfo", map[string]any{"nodeInfo": "[]"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := valid.apply(test.change).validate(); err == nil {
				t.Fatal("accepted invalid feature configuration")
			}
		})
	}
	if got := valid.values()["jumperAddresses"]; got != "tls://peer.example:1234" {
		t.Fatalf("saved jumper address = %v", got)
	}
	if !strings.Contains(valid.block("").Items[5].ID, "multicast") {
		t.Fatal("multicast control missing from menu")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, raw := range []string{"http://example.com:80", "tcp://", "tcp://example.com", "tls://[::1", "garbage", "tcp://host:99999", "tls://host:0"} {
		if (configuration{peers: raw}).validate() == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"tcp://127.0.0.1:1234", "tls://[200::1]:1234", "tls://peer.example:1234?key=abc"} {
		if err := (configuration{peers: raw}).validate(); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"default", "[", "a=*.onion b=*.onion", "a=one a=two"} {
		if (configuration{networks: raw}).validate() == nil {
			t.Errorf("accepted pattern %q", raw)
		}
	}
	config := readConfig(configer.NewMemory(map[string]any{"peers": " tcp://host:1\n tcp://host:1 ", "networks": "onion=*.onion\ni2p=*.i2p"}))
	if config.peers != "tcp://host:1" {
		t.Fatal("peers were not normalized")
	}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimePreservesCoreAcrossNetworkChanges(t *testing.T) {
	var r runtimeState
	defer r.close()
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	a := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	defer func() { _ = a.Close() }()
	b := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	defer func() { _ = b.Close() }()
	d := desired{generation: 1, key: key, config: configuration{enabled: true}, networks: map[string]nodekit.NetworkCloser{"default": a}}
	first := r.reconcile(d)
	if first.address == "" {
		t.Fatal(first)
	}
	original := r.core
	for i := 0; i < 4; i++ {
		d.generation++
		d.networks = map[string]nodekit.NetworkCloser{"default": b, "*.onion": nil}
		if got := r.reconcile(d); got.address != first.address || r.core != original {
			t.Fatal("network update rebuilt core", got)
		}
		d.generation++
		d.networks = nil
		if got := r.reconcile(d); got.state != "waiting for network" {
			t.Fatal(got)
		}
	}
	d.config.enabled = false
	if got := r.reconcile(d); got.state != "disabled" || r.core != nil {
		t.Fatal(got)
	}
	d.config.enabled = true
	if got := r.reconcile(d); got.address != first.address {
		t.Fatal("disable changed identity", got)
	}
}
func TestStaleRuntimeResults(t *testing.T) {
	n := node{generation: 10, state: "starting", address: "current"}
	if err := n.OnInbox(pasta.InboxMessage{Payload: result{generation: 9, state: "up", address: "old", requestTun: true}}); err != nil {
		t.Fatal(err)
	}
	if n.state != "starting" || n.address != "current" {
		t.Fatal("stale result changed state")
	}
	n.stopped = true
	_ = n.OnInbox(pasta.InboxMessage{Payload: result{generation: 10, state: "up", address: "old"}})
	if n.state != "starting" {
		t.Fatal("result revived stopped node")
	}
}

func TestRuntimeTerminalTunFailureAndRecovery(t *testing.T) {
	var r runtimeState
	defer r.close()
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	defer func() { _ = network.Close() }()
	device, peer := tun.Pipe(1, 1500, 0, 0)
	defer func() { _ = peer.Close() }()
	d := desired{generation: 1, config: configuration{enabled: true}, key: ed25519.NewKeyFromSeed(make([]byte, 32)), device: device, networks: map[string]nodekit.NetworkCloser{"default": network}}
	if got := r.reconcile(d); got.state != "up" {
		t.Fatal(got)
	}
	original := r.core
	_ = device.Close()
	deadline := time.Now().Add(time.Second)
	var got result
	for time.Now().Before(deadline) {
		got = r.reconcile(d)
		if got.requestTun {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !got.requestTun || got.state == "up" {
		t.Fatal("failed TUN remained ready", got)
	}
	replacement, other := tun.Pipe(1, 1500, 0, 0)
	defer func() { _ = other.Close() }()
	d.device = replacement
	d.generation++
	if got = r.reconcile(d); got.state != "up" || got.requestTun || r.core != original {
		t.Fatal("replacement did not preserve core", got)
	}
	_ = network.Close()
	if got = r.reconcile(d); !got.requestNetwork || got.state != "waiting for network" {
		t.Fatal("failed network remained ready", got)
	}
}

func TestUnrelatedMappingPreservesListener(t *testing.T) {
	var r runtimeState
	defer r.close()
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	defer func() { _ = network.Close() }()
	d := desired{generation: 1, config: configuration{enabled: true, listen: "tcp://127.0.0.1:0"}, key: ed25519.NewKeyFromSeed(make([]byte, 32)), networks: map[string]nodekit.NetworkCloser{"default": network}}
	r.reconcile(d)
	listener := r.listeners[d.config.listen]
	if listener == nil {
		t.Fatal("listener missing")
	}
	d.networks = map[string]nodekit.NetworkCloser{"default": network, "*.onion": nil}
	r.reconcile(d)
	if r.listeners[d.config.listen] != listener {
		t.Fatal("unrelated mapping replaced listener")
	}
	delete(d.networks, "*.onion")
	r.reconcile(d)
	if r.listeners[d.config.listen] != listener {
		t.Fatal("removing unrelated mapping replaced listener")
	}
}
