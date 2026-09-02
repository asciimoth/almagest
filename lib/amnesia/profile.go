package amnesianodes

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"
)

// amnesiaProfilePeer converts the backend-neutral Amnezia profile into the
// controller-owned part of a shared wgo device. It intentionally does not
// apply the profile's private key, listen port, firewall mark, TUN addresses,
// or default UDP bind. Those are device-global resources owned by the
// WireGuard and TUN nodes.
func amnesiaProfilePeer(controller wgdevice.DeviceAPI, profile *wgoamnesia.Profile) (wgdevice.PeerSpec, error) {
	if controller == nil {
		return wgdevice.PeerSpec{}, errors.New("WireGuard controller is not attached")
	}
	if profile == nil {
		return wgdevice.PeerSpec{}, errors.New("amnezia returned no profile")
	}
	if err := validateProfileIdentity(controller, profile); err != nil {
		return wgdevice.PeerSpec{}, err
	}

	publicKey, err := wgdevice.ParsePublicKey(strings.TrimSpace(profile.Peer.PublicKey))
	if err != nil {
		return wgdevice.PeerSpec{}, fmt.Errorf("peer public key: %w", err)
	}
	presharedKey, err := wgdevice.ParsePresharedKey(strings.TrimSpace(profile.Peer.PresharedKey))
	if err != nil {
		return wgdevice.PeerSpec{}, fmt.Errorf("peer preshared key: %w", err)
	}
	allowedIPs, err := profileAllowedIPs(profile.Peer.AllowedIPs)
	if err != nil {
		return wgdevice.PeerSpec{}, err
	}
	endpoint := strings.TrimSpace(profile.Peer.Endpoint)
	if endpoint == "" {
		return wgdevice.PeerSpec{}, errors.New("profile peer endpoint is empty")
	}
	awg, err := profileAmneziaWGConfig(profile.AmneziaWG)
	if err != nil {
		return wgdevice.PeerSpec{}, err
	}

	spec := wgdevice.PeerSpec{
		PublicKey:       publicKey,
		PresharedKey:    presharedKey,
		ProtocolVersion: 1,
		Endpoint: &wgdevice.PeerEndpoint{
			Transport: wgdevice.DefaultTransportID,
			Address:   endpoint,
		},
		AllowedIPs: allowedIPs,
		AmneziaWG:  &awg,
		Activation: wgdevice.PeerActivationEager,
	}
	if profile.Peer.PersistentKeepalive.Set {
		keepalive := profile.Peer.PersistentKeepalive
		if keepalive.Max > uint32(^uint16(0)) {
			return wgdevice.PeerSpec{}, errors.New("persistent keepalive exceeds 65535 seconds")
		}
		if keepalive.Min == keepalive.Max {
			spec.PersistentKeepaliveInterval = uint16(keepalive.Min)
		} else {
			rangeValue := wgdevice.AmneziaWGRange{Min: keepalive.Min, Max: keepalive.Max, Set: true}
			spec.PersistentKeepaliveRange = &rangeValue
		}
	}
	return spec, nil
}

func validateProfileIdentity(controller wgdevice.DeviceAPI, profile *wgoamnesia.Profile) error {
	privateKey := controller.PrivateKey()
	if privateKey.IsZero() {
		return errors.New("WireGuard device has no private key")
	}
	controllerPublic := privateKey.PublicKey().Base64()
	profilePublic := strings.TrimSpace(profile.Interface.PublicKey)
	if encodedPrivate := strings.TrimSpace(profile.Interface.PrivateKey); encodedPrivate != "" {
		parsed, err := wgdevice.ParsePrivateKey(encodedPrivate)
		if err != nil {
			return fmt.Errorf("profile private key: %w", err)
		}
		derived := parsed.PublicKey().Base64()
		if profilePublic != "" && profilePublic != derived {
			return errors.New("profile private and public keys do not match")
		}
		profilePublic = derived
	}
	if profilePublic == "" {
		return errors.New("profile has no WireGuard client identity")
	}
	if profilePublic != controllerPublic {
		return errors.New("profile client key does not match the WireGuard node; configure the WireGuard node with the profile private key")
	}
	return nil
}

func profileAllowedIPs(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("allowed IP %q: %w", value, err)
		}
		out = append(out, prefix)
	}
	if len(out) == 0 {
		return nil, errors.New("profile has no peer allowed IPs")
	}
	return out, nil
}

func profileAmneziaWGConfig(src wgoamnesia.AmneziaWGConfig) (wgdevice.AmneziaWGConfig, error) {
	cfg := wgdevice.DefaultAmneziaWGConfig()
	version, err := profileAmneziaWGVersion(src.ProtocolVersion)
	if err != nil {
		return wgdevice.AmneziaWGConfig{}, err
	}
	cfg.Version = version
	cfg.JunkCount = src.JunkPacketCount
	cfg.JunkMin = src.JunkPacketMinSize
	cfg.JunkMax = src.JunkPacketMaxSize
	if !profileFitsInt(src.InitPadding) || !profileFitsInt(src.ResponsePadding) ||
		!profileFitsInt(src.CookiePadding) || !profileFitsInt(src.TransportPadding) {
		return wgdevice.AmneziaWGConfig{}, errors.New("AmneziaWG padding exceeds the platform int range")
	}
	cfg.InitPadding = int(src.InitPadding)
	cfg.ResponsePadding = int(src.ResponsePadding)
	cfg.CookiePadding = int(src.CookiePadding)
	cfg.TransportPadding = int(src.TransportPadding)
	if src.InitHeader.Set {
		cfg.InitHeader = wgdevice.AmneziaWGHeaderRange{Start: src.InitHeader.Min, End: src.InitHeader.Max}
	}
	if src.ResponseHeader.Set {
		cfg.ResponseHeader = wgdevice.AmneziaWGHeaderRange{Start: src.ResponseHeader.Min, End: src.ResponseHeader.Max}
	}
	if src.CookieHeader.Set {
		cfg.CookieHeader = wgdevice.AmneziaWGHeaderRange{Start: src.CookieHeader.Min, End: src.CookieHeader.Max}
	}
	if src.TransportHeader.Set {
		cfg.TransportHeader = wgdevice.AmneziaWGHeaderRange{Start: src.TransportHeader.Min, End: src.TransportHeader.Max}
	}
	cfg.InitiationPackets = src.InitiationPackets
	if key := strings.TrimSpace(src.HeaderProtectionKey); key != "" {
		cfg.HeaderProtectionKey, err = profileHeaderProtectionKey(key)
		if err != nil {
			return wgdevice.AmneziaWGConfig{}, fmt.Errorf("header protection key: %w", err)
		}
	}
	cfg.ContentPadding = profileDeviceRange(src.ContentPaddingAddition)
	cfg.RekeyAfterTime = profileDeviceRange(src.RekeyAfterTime)
	cfg.RekeyTimeout = profileDeviceRange(src.RekeyTimeout)
	cfg.RejectAfterTime = profileDeviceRange(src.RejectAfterTime)
	cfg.KeepaliveTimeout = profileDeviceRange(src.KeepaliveTimeout)
	cfg.MaxHandshakeAttempts = profileDeviceRange(src.MaxHandshakeAttempts)
	if src.RandomTrailers.Set {
		cfg.RandomTrailers = src.RandomTrailers.Enabled
	}
	if src.DisableCookies.Set {
		cfg.DisableCookies = src.DisableCookies.Enabled
	}
	if err := wgdevice.ValidateAmneziaWGConfig(cfg); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("AmneziaWG profile: %w", err)
	}
	return cfg, nil
}

func profileAmneziaWGVersion(value string) (wgdevice.AmneziaWGVersion, error) {
	switch strings.TrimSpace(value) {
	case "":
		return wgdevice.AmneziaWGVersionAuto, nil
	case "1.5":
		return wgdevice.AmneziaWGV1_5, nil
	case "2":
		return wgdevice.AmneziaWGV2, nil
	case "3.1":
		return wgdevice.AmneziaWGV3_1, nil
	default:
		return wgdevice.AmneziaWGVersionAuto, fmt.Errorf("unsupported AmneziaWG protocol version %q", value)
	}
}

func profileDeviceRange(src wgoamnesia.Uint32Range) wgdevice.AmneziaWGRange {
	return wgdevice.AmneziaWGRange{Min: src.Min, Max: src.Max, Set: src.Set}
}

func profileFitsInt(value uint32) bool {
	return uint64(value) <= uint64(int(^uint(0)>>1))
}

func profileHeaderProtectionKey(value string) (wgdevice.AmneziaWGHeaderProtectionKey, error) {
	var key wgdevice.AmneziaWGHeaderProtectionKey
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == len(key) {
		copy(key[:], decoded)
		return key, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil && len(decoded) == len(key) {
		copy(key[:], decoded)
		return key, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(key) {
		return key, errors.New("key must encode exactly 32 bytes")
	}
	copy(key[:], decoded)
	return key, nil
}
