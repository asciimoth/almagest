package yggdrasilnodes

import (
	"context"
	"crypto/ed25519"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/gonnect"
)

// The Gonnect loopback network supplies multicast sockets and in-memory TCP.
// This adapter gives its lo interface a link-local IPv6 address, as a real
// multicast-capable interface has.
type linkLocalTestNetwork struct{ *gonnect.LoopbackNetwork }

func (n *linkLocalTestNetwork) Interfaces() ([]gonnect.NetworkInterface, error) {
	return []gonnect.NetworkInterface{&gonnect.LiteralInterface{
		IndexVal: 1, NameVal: "lo", MTUVal: 1500,
		FlagsVal: net.FlagUp | net.FlagRunning | net.FlagMulticast,
		AddrsVal: []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}},
	}}, nil
}
func (n *linkLocalTestNetwork) InterfacesByName(name string) ([]gonnect.NetworkInterface, error) {
	if name != "lo" {
		return nil, net.ErrClosed
	}
	return n.Interfaces()
}
func (n *linkLocalTestNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	host, portText, err := net.SplitHostPort(address)
	addressIP, _, _ := strings.Cut(host, "%")
	linkLocal := err == nil && net.ParseIP(addressIP).IsLinkLocalUnicast()
	if linkLocal {
		// LoopbackNetwork maps arbitrary hosts to localhost. Use its IPv6
		// listener registry because the transport dials link-local peers as tcp6.
		network = "tcp6"
	}
	listener, err := n.LoopbackNetwork.Listen(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if linkLocal {
		port, _ := strconv.Atoi(portText)
		if port == 0 {
			_, portText, _ = net.SplitHostPort(listener.Addr().String())
			port, _ = strconv.Atoi(portText)
		}
		return linkLocalTestListener{Listener: listener, addr: &net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: port, Zone: "lo"}}, nil
	}
	return listener, nil
}

type linkLocalTestListener struct {
	net.Listener
	addr net.Addr
}

func (l linkLocalTestListener) Addr() net.Addr { return l.addr }

func TestLinkLocalMulticastAutopeeringLifecycle(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	base.AllowAnyHost = true
	source := &linkLocalTestNetwork{base}
	firstNetwork := gonnect.DetachNetwork(source, nil, nil)
	secondNetwork := gonnect.DetachNetwork(source, nil, nil)
	defer func() { _ = firstNetwork.Close(); _ = secondNetwork.Close() }()
	config := configuration{enabled: true, multicastInterfaces: `[{"regex":"^lo$","beacon":true,"listen":true}]`}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	var first, second runtimeState
	defer first.close()
	defer second.close()
	a := desired{generation: 1, config: config, key: ed25519.NewKeyFromSeed(make([]byte, 32)), networks: map[string]nodekit.NetworkCloser{"default": firstNetwork}}
	b := desired{generation: 1, config: config, key: ed25519.NewKeyFromSeed([]byte("12345678901234567890123456789012")), networks: map[string]nodekit.NetworkCloser{"default": secondNetwork}}
	if got := first.reconcile(a); got.state != "waiting for TUN" || first.multicast == nil {
		t.Fatalf("first discovery did not start: %+v", got)
	}
	if got := second.reconcile(b); got.state != "waiting for TUN" || second.multicast == nil {
		t.Fatalf("second discovery did not start: %+v", got)
	}
	first.multicast.AnnounceNow()
	second.multicast.AnnounceNow()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(first.core.ConnectedPeerURIs()) > 0 && len(second.core.ConnectedPeerURIs()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(first.core.ConnectedPeerURIs()) == 0 || len(second.core.ConnectedPeerURIs()) == 0 {
		t.Fatalf("multicast discovery did not connect peers: first=%v second=%v", first.core.ConnectedPeerURIs(), second.core.ConnectedPeerURIs())
	}
	a.generation++
	a.networks = nil
	if got := first.reconcile(a); got.state != "waiting for network" || first.multicast != nil {
		t.Fatalf("discovery survived network detach: %+v", got)
	}
	a.generation++
	a.networks = map[string]nodekit.NetworkCloser{"default": firstNetwork}
	if got := first.reconcile(a); got.state != "waiting for TUN" || first.multicast == nil {
		t.Fatalf("discovery did not restart: %+v", got)
	}
}
