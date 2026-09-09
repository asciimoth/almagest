package wireguardnodes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

func TestCalculateWireGuardCarrierMTUAcrossPeers(t *testing.T) {
	provider := links.NewLayer3DatagramMTUProvider(1500, true, true, links.MTUConstraint{Source: "carrier"})
	config := defaultWireGuardConfig()
	config.peers = []wireGuardPeerConfig{
		{endpoint: "192.0.2.1:51820"},
		{endpoint: "[2001:db8::1]:51820"},
	}
	maxPayload, constraint, err := calculateWireGuardCarrierMTU(
		context.Background(), provider, config, 7, "test WireGuard", true,
	)
	if err != nil {
		t.Fatalf("calculateWireGuardCarrierMTU() error = %v", err)
	}
	if maxPayload != 1452 || constraint.Value != 1420 {
		t.Fatalf("result = payload %d, constraint %+v; want payload 1452 and MTU 1420", maxPayload, constraint)
	}
	if constraint.Overhead != links.WireGuardDataOverhead || len(constraint.Path) != 1 || constraint.Path[0] != 7 {
		t.Fatalf("constraint provenance = %+v", constraint)
	}
}

func TestCalculateWireGuardCarrierMTUSubtractsPeerS4(t *testing.T) {
	provider := links.NewLayer3DatagramMTUProvider(1500, true, false, links.MTUConstraint{Source: "carrier"})
	config := defaultWireGuardConfig()
	config.peers = []wireGuardPeerConfig{{
		endpoint:          "192.0.2.1:51820",
		amneziaWGOverride: true,
		amneziaWGEnabled:  true,
		amneziaWG:         wireGuardAmneziaConfig{transportPadding: 16},
	}}
	_, constraint, err := calculateWireGuardCarrierMTU(
		context.Background(), provider, config, 8, "Amnezia peer", false,
	)
	if err != nil {
		t.Fatalf("calculateWireGuardCarrierMTU() error = %v", err)
	}
	if constraint.Value != 1424 || constraint.Overhead != 48 {
		t.Fatalf("constraint = %+v, want MTU 1424 and overhead 48", constraint)
	}
}

func TestCalculateWireGuardCarrierMTURejectsIPv6BelowMinimum(t *testing.T) {
	provider := links.NewLayer3DatagramMTUProvider(1300, false, true, links.MTUConstraint{Source: "carrier"})
	config := defaultWireGuardConfig()
	config.peers = []wireGuardPeerConfig{{endpoint: "[2001:db8::1]:51820"}}
	_, _, err := calculateWireGuardCarrierMTU(context.Background(), provider, config, 9, "test", true)
	var incompatibility *mtuIncompatibilityError
	if !errors.As(err, &incompatibility) {
		t.Fatalf("error = %v, want mtuIncompatibilityError", err)
	}
}

func TestValidateWireGuardStandaloneMessages(t *testing.T) {
	if err := validateWireGuardStandaloneMessages(100, wireGuardAmneziaConfig{}, false); err == nil {
		t.Fatal("validateWireGuardStandaloneMessages() accepted an oversized plain WireGuard handshake")
	}
	config := wireGuardAmneziaConfig{
		junkCount: 1,
		junkMax:   1400,
	}
	config.initiationPackets[0] = "<r 1300>"
	if err := validateWireGuardStandaloneMessages(1399, config, true); err == nil {
		t.Fatal("validateWireGuardStandaloneMessages() accepted an oversized junk packet")
	}
	config.junkMax = 1200
	if err := validateWireGuardStandaloneMessages(1299, config, true); err == nil {
		t.Fatal("validateWireGuardStandaloneMessages() accepted an oversized custom initiation packet")
	}
	if err := validateWireGuardStandaloneMessages(1400, config, true); err != nil {
		t.Fatalf("validateWireGuardStandaloneMessages() error = %v", err)
	}
}

func TestCalculateWireGuardCarrierMTURejectsConstraintCycle(t *testing.T) {
	provider := links.NewLayer3DatagramMTUProvider(1500, true, false, links.MTUConstraint{
		Source: "cycle", Path: []uint64{10},
	})
	config := defaultWireGuardConfig()
	config.peers = []wireGuardPeerConfig{{endpoint: "192.0.2.1:51820"}}
	_, _, err := calculateWireGuardCarrierMTU(context.Background(), provider, config, 10, "test", false)
	if err == nil {
		t.Fatal("calculateWireGuardCarrierMTU() accepted a dependency cycle")
	}
}

func TestCalculateWireGuardCarrierMTUComposesThreeLayers(t *testing.T) {
	provider := links.NewLayer3DatagramMTUProvider(
		1500, true, false, links.MTUConstraint{Source: "native carrier"},
	)
	want := []int{1440, 1380, 1320}
	for layer, wantMTU := range want {
		config := defaultWireGuardConfig()
		config.peers = []wireGuardPeerConfig{{endpoint: "192.0.2.1:51820"}}
		_, constraint, err := calculateWireGuardCarrierMTU(
			context.Background(), provider, config, uint64(layer+1), "nested WireGuard", false,
		)
		if err != nil {
			t.Fatalf("layer %d calculation error = %v", layer+1, err)
		}
		if constraint.Value != wantMTU {
			t.Fatalf("layer %d MTU = %d, want %d", layer+1, constraint.Value, wantMTU)
		}
		if len(constraint.Path) != layer+1 {
			t.Fatalf("layer %d path = %v", layer+1, constraint.Path)
		}
		provider = links.NewLayer3DatagramMTUProvider(wantMTU, true, false, constraint)
	}
}

func TestWireGuardMTUProviderAttachDetachReattach(t *testing.T) {
	w := pasta.NewWorkspace(wireGuardTestLogFactory{})
	t.Cleanup(func() { w.Close() })

	var wireGuard *wireGuardNode
	if err := w.AddNodeClass(captureWireGuardClass{node: &wireGuard}); err != nil {
		t.Fatalf("AddNodeClass WireGuard: %v", err)
	}
	firstProvider := links.NewLayer3DatagramMTUProvider(1500, true, false, links.MTUConstraint{Source: "first carrier"})
	secondProvider := links.NewLayer3DatagramMTUProvider(1400, true, false, links.MTUConstraint{Source: "second carrier"})
	network := &recordingWireGuardTestNetwork{Network: gonnect.NewLoopbackNetwork()}
	var source *wireGuardTestNetworkSourceNode
	if err := w.AddNodeClass(wireGuardTestNetworkSourceClass{
		network: network, provider: firstProvider, node: &source,
	}); err != nil {
		t.Fatalf("AddNodeClass NetworkSource: %v", err)
	}
	wgID, err := w.AddNodeByClass("test/WireGuard", "wg")
	if err != nil {
		t.Fatalf("AddNodeByClass WireGuard: %v", err)
	}
	networkID, err := w.AddNodeByClass("test/WireGuardNetworkSource", "network")
	if err != nil {
		t.Fatalf("AddNodeByClass NetworkSource: %v", err)
	}
	if wireGuard == nil || source == nil {
		t.Fatal("test node capture failed")
	}
	wgSnapshot := testkit.MustNodeSnapshot(t, w, wgID)
	networkSnapshot := testkit.MustNodeSnapshot(t, w, networkID)

	firstLink, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink first: %v", err)
	}
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return wireGuard.networkPayloadReceived &&
			links.SameDatagramMTUProvider(wireGuard.desiredDatagramMTU, firstProvider) &&
			wireGuard.desiredMTURevision == firstProvider.Revision() &&
			wireGuard.unsubscribeDatagramMTU != nil
	}, "first MTU provider attached")

	firstProvider.SetMTU(1450, true, false, links.MTUConstraint{Source: "first carrier update"})
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return wireGuard.desiredMTURevision == firstProvider.Revision() && !wireGuard.mtuPending
	}, "first MTU provider update processed")

	w.RemoveLink(firstLink)
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return !wireGuard.networkPayloadReceived && wireGuard.desiredDatagramMTU == nil &&
			wireGuard.desiredMTURevision == 0 && wireGuard.unsubscribeDatagramMTU == nil
	}, "first MTU provider detached")

	w.Lock()
	source.provider = secondProvider
	w.Unlock()
	secondLink, _, err := w.AddLink(networkSnapshot.LeftPorts[0], wgSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink second: %v", err)
	}
	_ = secondLink
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return wireGuard.networkPayloadReceived &&
			links.SameDatagramMTUProvider(wireGuard.desiredDatagramMTU, secondProvider) &&
			wireGuard.desiredMTURevision == secondProvider.Revision() &&
			wireGuard.unsubscribeDatagramMTU != nil && !wireGuard.mtuPending
	}, "second MTU provider attached")

	secondRevision := secondProvider.Revision()
	firstProvider.SetMTU(1300, true, false, links.MTUConstraint{Source: "stale carrier update"})
	time.Sleep(25 * time.Millisecond)
	w.Lock()
	if !links.SameDatagramMTUProvider(wireGuard.desiredDatagramMTU, secondProvider) || wireGuard.desiredMTURevision != secondRevision {
		w.Unlock()
		t.Fatal("detached provider update changed the reattached MTU provider")
	}
	w.Unlock()

	secondProvider.SetMTU(1350, true, false, links.MTUConstraint{Source: "second carrier update"})
	eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return wireGuard.desiredMTURevision == secondProvider.Revision() && !wireGuard.mtuPending
	}, "second MTU provider update processed")
}
