package yggdrasilnodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/gonnect"
)

func TestPublicAutopeeringMappedSourceAndNodeInfo(t *testing.T) {
	carrier := gonnect.NewLoopbackNetwork()
	carrier.AllowAnyHost = true
	sourceCarrier := gonnect.NewLoopbackNetwork()
	sourceCarrier.AllowAnyHost = true
	clientCarrier := gonnect.DetachNetwork(carrier, nil, nil)
	serverCarrier := gonnect.DetachNetwork(carrier, nil, nil)
	fetchCarrier := gonnect.DetachNetwork(sourceCarrier, nil, nil)
	defer func() { _ = clientCarrier.Close(); _ = serverCarrier.Close(); _ = fetchCarrier.Close() }()

	peerPort := 24391
	peerURL := fmt.Sprintf("tcp://127.0.0.1:%d", peerPort)
	var server runtimeState
	defer server.close()
	server.reconcile(desired{
		generation: 1, config: configuration{enabled: true, listen: peerURL},
		key:      ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize)),
		networks: map[string]nodekit.NetworkCloser{"default": serverCarrier},
	})
	if server.listeners[peerURL] == nil {
		t.Fatal("public listener was not created")
	}

	port := 24392
	listener, err := sourceCarrier.Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	document := fmt.Sprintf(`{"schema_version":"1","generated_at":"2026-01-01T00:00:00Z","sources":{"public_peers_repo":"test","public_peers_branch":"main","uptime_page":"test"},"peers":[{"continent":"Asia","country":"Georgia","source_file":"test","endpoints":[{"url":%q,"protocol":"tcp","host":"127.0.0.1","port":%d}]}]}`, peerURL, peerPort)
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(document))
	})}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()

	sourceURL := fmt.Sprintf("http://source.example:%d/", port)
	config := configuration{
		enabled: true, autoPeerEnabled: true, autoPeerSources: sourceURL,
		autoPeerCountries: "georgia", autoPeerSchemes: "tcp", autoPeerFetchInterval: "1s",
		autoPeerCheckInterval: "100ms", autoPeerMinimumConnected: 1, autoPeerMinimumFromFetch: 1,
		nodeInfo: `{"service":"test"}`, nodeInfoPrivacy: true, jumperEnabled: true, jumperAddresses: "tcp://peer.example:1234",
	}
	var client runtimeState
	defer client.close()
	d := desired{
		generation: 1, config: config,
		key:      ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, ed25519.SeedSize)),
		networks: map[string]nodekit.NetworkCloser{"default": clientCarrier, "source.example": fetchCarrier},
	}
	if err := d.config.validate(); err != nil {
		t.Fatal(err)
	}
	client.reconcile(d)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(client.core.ConnectedPeerURIs()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(client.core.ConnectedPeerURIs()) == 0 {
		t.Fatalf("mapped public-peer source did not connect: candidates=%v", client.autoPeer.Peers())
	}
	if client.nodeInfoConfig == "" {
		t.Fatal("NodeInfo settings were not applied")
	}

	d.generation++
	d.config.autoPeerEnabled = false
	adapter := client.autoPeerAdapter
	client.reconcile(d)
	if client.autoPeer != nil || len(adapter.owned) != 0 {
		t.Fatal("disabled autopeering retained its manager or owned URLs")
	}
	d.generation++
	d.config.autoPeerEnabled = true
	d.networks = map[string]nodekit.NetworkCloser{"default": clientCarrier, "source.example": nil}
	client.reconcile(d)
	if client.autoPeer == nil || len(client.autoPeer.Peers()) != 0 {
		t.Fatal("disabled source mapping fetched peers")
	}
	d.generation++
	d.networks["source.example"] = fetchCarrier
	client.reconcile(d)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(client.autoPeer.Peers()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(client.autoPeer.Peers()) == 0 {
		t.Fatal("public-peer list did not recover after source carrier reattach")
	}
}

func TestPublicPeerSourceUsesMostSpecificCarrier(t *testing.T) {
	defaultNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	specificNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	defer func() { _ = defaultNetwork.Close(); _ = specificNetwork.Close() }()
	networks := map[string]nodekit.NetworkCloser{
		"default": defaultNetwork, "*.example": nil, "api.example": specificNetwork,
	}
	if got := sourceNetwork("https://api.example/peers.json", networks); got != specificNetwork {
		t.Fatal("exact host did not select its carrier")
	}
	if got := sourceNetwork("https://other.example/peers.json", networks); got != nil {
		t.Fatal("disconnected matching carrier did not block the source")
	}
	if got := sourceNetwork("https://unmatched.test/peers.json", networks); got != defaultNetwork {
		t.Fatal("unmatched source did not use default carrier")
	}
}
