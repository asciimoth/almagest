package amnesianodes

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/gonnect"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"
)

func TestAmnesiaProfilePeerUsesControllerIdentityAndDefaultTransport(t *testing.T) {
	device, controller, privateKey := newAmnesiaTestController(t)
	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	profile := amnesiaTestProfile(privateKey.PublicKey(), peerKey, "de")

	spec, err := amnesiaProfilePeer(controller, profile)
	if err != nil {
		t.Fatalf("convert profile: %v", err)
	}
	if spec.Endpoint == nil || spec.Endpoint.Transport != wgdevice.DefaultTransportID {
		t.Fatalf("peer endpoint = %#v, want the WireGuard default transport", spec.Endpoint)
	}
	if err := controller.UpsertTrackedPeer(spec); err != nil {
		t.Fatalf("apply tracked peer: %v", err)
	}
	stored, found := device.PeerSpec(peerKey)
	if !found || stored.Endpoint == nil || stored.Endpoint.Transport != wgdevice.DefaultTransportID {
		t.Fatalf("stored peer = %#v, want default-transport endpoint", stored)
	}

	otherKey := amnesiaTestPrivateKey(t)
	profile.Interface.PublicKey = otherKey.PublicKey().Base64()
	if _, err := amnesiaProfilePeer(controller, profile); err == nil {
		t.Fatal("profile with a different WireGuard identity was accepted")
	}
}

func TestAmnesiaProfilePeerAcceptsProfileAfterPreConnectHandling(t *testing.T) {
	_, controller, privateKey := newAmnesiaTestController(t)
	profile := amnesiaTestProfile(privateKey.PublicKey(), amnesiaTestPrivateKey(t).PublicKey(), "")
	profile.PreConnect = []wgoamnesia.PreConnectAction{{Protocol: "udp", Endpoint: "127.0.0.1:1"}}
	if _, err := amnesiaProfilePeer(controller, profile); err != nil {
		t.Fatalf("convert profile after pre-connect handling: %v", err)
	}
}

func TestAmnesiaProfileValidationErrors(t *testing.T) {
	_, controller, privateKey := newAmnesiaTestController(t)
	valid := amnesiaTestProfile(privateKey.PublicKey(), amnesiaTestPrivateKey(t).PublicKey(), "")
	tests := []struct {
		name   string
		mutate func(*wgoamnesia.Profile)
	}{
		{name: "nil controller"},
		{name: "nil profile"},
		{name: "bad peer public key", mutate: func(profile *wgoamnesia.Profile) { profile.Peer.PublicKey = "bad" }},
		{name: "bad preshared key", mutate: func(profile *wgoamnesia.Profile) { profile.Peer.PresharedKey = "bad" }},
		{name: "missing allowed IPs", mutate: func(profile *wgoamnesia.Profile) { profile.Peer.AllowedIPs = nil }},
		{name: "bad allowed IP", mutate: func(profile *wgoamnesia.Profile) { profile.Peer.AllowedIPs = []string{"bad"} }},
		{name: "missing endpoint", mutate: func(profile *wgoamnesia.Profile) { profile.Peer.Endpoint = " " }},
		{name: "bad protocol version", mutate: func(profile *wgoamnesia.Profile) { profile.AmneziaWG.ProtocolVersion = "9" }},
		{name: "bad header protection key", mutate: func(profile *wgoamnesia.Profile) { profile.AmneziaWG.HeaderProtectionKey = "bad" }},
		{name: "excessive keepalive", mutate: func(profile *wgoamnesia.Profile) {
			profile.Peer.PersistentKeepalive = wgoamnesia.Uint32Range{Min: 1, Max: 65536, Set: true}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotController := wgdevice.DeviceAPI(controller)
			profile := *valid
			profile.Peer = valid.Peer
			profile.AmneziaWG = valid.AmneziaWG
			profilePtr := &profile
			switch test.name {
			case "nil controller":
				gotController = nil
			case "nil profile":
				profilePtr = nil
			default:
				test.mutate(profilePtr)
			}
			if _, err := amnesiaProfilePeer(gotController, profilePtr); err == nil {
				t.Fatal("invalid profile was accepted")
			}
		})
	}
}

func TestAmnesiaProfileIdentityAndKeepaliveVariants(t *testing.T) {
	_, controller, privateKey := newAmnesiaTestController(t)
	profile := amnesiaTestProfile(privateKey.PublicKey(), amnesiaTestPrivateKey(t).PublicKey(), "")
	profile.Interface.PrivateKey = privateKey.Base64()
	profile.Interface.PublicKey = ""
	profile.Peer.PersistentKeepalive = wgoamnesia.Uint32Range{Min: 10, Max: 20, Set: true}
	spec, err := amnesiaProfilePeer(controller, profile)
	if err != nil {
		t.Fatal(err)
	}
	if spec.PersistentKeepaliveRange == nil || spec.PersistentKeepaliveRange.Min != 10 || spec.PersistentKeepaliveRange.Max != 20 {
		t.Fatalf("keepalive range = %#v", spec.PersistentKeepaliveRange)
	}
	profile.Peer.PersistentKeepalive = wgoamnesia.Uint32Range{Min: 15, Max: 15, Set: true}
	spec, err = amnesiaProfilePeer(controller, profile)
	if err != nil || spec.PersistentKeepaliveInterval != 15 {
		t.Fatalf("keepalive interval = %d, %v", spec.PersistentKeepaliveInterval, err)
	}
	profile.Interface.PrivateKey = "bad"
	if _, err := amnesiaProfilePeer(controller, profile); err == nil {
		t.Fatal("invalid profile private key was accepted")
	}
	other := amnesiaTestPrivateKey(t)
	profile.Interface.PrivateKey = other.Base64()
	profile.Interface.PublicKey = privateKey.PublicKey().Base64()
	if _, err := amnesiaProfilePeer(controller, profile); err == nil {
		t.Fatal("mismatched profile identity was accepted")
	}
}

func TestAmnesiaProfileEncodingHelpers(t *testing.T) {
	for input, want := range map[string]wgdevice.AmneziaWGVersion{
		"": wgdevice.AmneziaWGVersionAuto, "1.5": wgdevice.AmneziaWGV1_5,
		"2": wgdevice.AmneziaWGV2, "3.1": wgdevice.AmneziaWGV3_1,
	} {
		if got, err := profileAmneziaWGVersion(input); err != nil || got != want {
			t.Fatalf("profileAmneziaWGVersion(%q) = %v, %v", input, got, err)
		}
	}
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(keyBytes),
		base64.RawStdEncoding.EncodeToString(keyBytes),
		hex.EncodeToString(keyBytes),
	} {
		if key, err := profileHeaderProtectionKey(encoded); err != nil || string(key[:]) != string(keyBytes) {
			t.Fatalf("decode key %q: %x, %v", encoded, key, err)
		}
	}
	if _, err := profileHeaderProtectionKey(strings.Repeat("x", 32)); err == nil {
		t.Fatal("invalid header key was accepted")
	}
	if got, err := profileAllowedIPs([]string{" ", "192.0.2.0/24"}); err != nil || len(got) != 1 {
		t.Fatalf("allowed IPs = %#v, %v", got, err)
	}
}

func newAmnesiaTestController(t *testing.T) (*wgdevice.Device, wgdevice.DeviceAPI, wgdevice.NoisePrivateKey) {
	t.Helper()
	network := gonnect.NewLoopbackNetwork()
	device := wgdevice.NewDevice(nil, conn.NewDefaultBind(network), wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
	privateKey := amnesiaTestPrivateKey(t)
	if err := device.SetPrivateKey(privateKey); err != nil {
		device.Close()
		_ = network.Close()
		t.Fatalf("set device private key: %v", err)
	}
	controller := wgdevice.DetachDevice(device)
	t.Cleanup(func() {
		controller.Close()
		device.Close()
		_ = network.Close()
	})
	return device, controller, privateKey
}

func amnesiaTestPrivateKey(t *testing.T) wgdevice.NoisePrivateKey {
	t.Helper()
	key, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate WireGuard key: %v", err)
	}
	return key
}

func amnesiaTestProfile(clientKey, peerKey wgdevice.NoisePublicKey, country string) *wgoamnesia.Profile {
	return &wgoamnesia.Profile{
		Name: "test-profile",
		Interface: wgoamnesia.WireGuardInterface{
			PublicKey: clientKey.Base64(), Addresses: []string{"10.20.0.2/32"}, DNS: []string{"10.20.0.1"},
		},
		Peer: wgoamnesia.WireGuardPeer{
			PublicKey: peerKey.Base64(), Endpoint: "127.0.0.1:51820", AllowedIPs: []string{"0.0.0.0/0"},
		},
		API: wgoamnesia.ProfileAPI{ServerCountryCode: country},
	}
}
