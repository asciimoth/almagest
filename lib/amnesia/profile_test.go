package amnesianodes

import (
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
