package amnesianodes

import (
	"context"
	"strings"
	"testing"

	"github.com/asciimoth/gonnect"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"

	"github.com/asciimoth/almagest/lib/links"
)

func TestCalculateAmnesiaProfileMTUUsesProfileCeilingAndS4(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	provider := links.NewLayer3DatagramMTUProvider(1500, true, false, links.MTUConstraint{Source: "carrier"})
	links.AttachDatagramMTUProvider(network, provider)
	defer links.DetachDatagramMTUProvider(network)
	profile := &wgoamnesia.Profile{
		Interface: wgoamnesia.WireGuardInterface{MTU: 1400, Addresses: []string{"10.0.0.2/32"}},
		Peer:      wgoamnesia.WireGuardPeer{Endpoint: "192.0.2.1:51820"},
		AmneziaWG: wgoamnesia.AmneziaWGConfig{TransportPadding: 16},
	}
	status, warning, err := calculateAmnesiaProfileMTU(context.Background(), network, profile, 11, "NL")
	if err != nil || warning != "" {
		t.Fatalf("calculateAmnesiaProfileMTU() warning = %q, error = %v", warning, err)
	}
	if status.mtu != 1400 || status.profileMTU != 1400 || status.carrierMaxUDP != 1472 {
		t.Fatalf("status = %+v", status)
	}
	profile.Interface.MTU = 1500
	status, warning, err = calculateAmnesiaProfileMTU(context.Background(), network, profile, 11, "NL")
	if err != nil || warning != "" || status.mtu != 1424 {
		t.Fatalf("carrier-limited status = %+v, warning = %q, error = %v", status, warning, err)
	}
}

func TestCalculateAmnesiaProfileMTURejectsIPv6BelowMinimum(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	provider := links.NewLayer3DatagramMTUProvider(1300, false, true, links.MTUConstraint{Source: "carrier"})
	links.AttachDatagramMTUProvider(network, provider)
	defer links.DetachDatagramMTUProvider(network)
	profile := &wgoamnesia.Profile{
		Interface: wgoamnesia.WireGuardInterface{Addresses: []string{"2001:db8::2/128"}},
		Peer:      wgoamnesia.WireGuardPeer{Endpoint: "[2001:db8::1]:51820"},
	}
	_, _, err := calculateAmnesiaProfileMTU(context.Background(), network, profile, 12, "")
	if err == nil || !strings.Contains(err.Error(), "incompatible with IPv6") {
		t.Fatalf("error = %v, want IPv6 incompatibility", err)
	}
}

func TestValidateAmnesiaProfileDatagrams(t *testing.T) {
	profile := &wgoamnesia.Profile{AmneziaWG: wgoamnesia.AmneziaWGConfig{
		JunkPacketCount: 1, JunkPacketMaxSize: 1400,
	}}
	if err := validateAmnesiaProfileDatagrams(1399, profile); err == nil {
		t.Fatal("validateAmnesiaProfileDatagrams() accepted oversized junk")
	}
	profile.AmneziaWG.JunkPacketMaxSize = 1200
	profile.AmneziaWG.InitiationPackets[0] = "<r 1300>"
	if err := validateAmnesiaProfileDatagrams(1299, profile); err == nil {
		t.Fatal("validateAmnesiaProfileDatagrams() accepted oversized custom packet")
	}
	profile.AmneziaWG.InitiationPackets[0] = ""
	profile.AmneziaWG.RandomTrailers = wgoamnesia.Toggle{Set: true, Enabled: true}
	if err := validateAmnesiaProfileDatagrams(1500, profile); err == nil {
		t.Fatal("validateAmnesiaProfileDatagrams() accepted oversized random trailer maximum")
	}
}
