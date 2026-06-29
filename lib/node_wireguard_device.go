package almagest

import (
	"errors"
	"fmt"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/gonnect/tun"
	wgdevice "github.com/asciimoth/wgo/device"
)

func (n *wireGuardNode) detachDevicePart(name string, detach func() error, update func()) error {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	update()
	if n.dev == nil {
		return nil
	}
	var detachErr error
	if err := n.dev.Down(); err != nil {
		detachErr = fmt.Errorf("bring device down before detaching %s: %w", name, err)
	}
	if err := detach(); err != nil {
		detachErr = fmt.Errorf("detach %s: %w", name, err)
	}
	return detachErr
}
func (n *wireGuardNode) ensureDeviceLocked() {
	if n.dev != nil {
		return
	}
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.logger != nil {
		logger = n.logger
	}
	n.dev = wgdevice.NewDevice(nil, nil, logger)
	n.runtimeStats = n.dev.RuntimeStats()
	n.subscribeToRuntimeStatsLocked(n.dev)
	n.subscribeToReceiveErrorsLocked(n.dev)
}
func (n *wireGuardNode) applyConfigToDevice(rebuildPeers bool) error {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	return n.applyConfigToDeviceLocked(rebuildPeers)
}
func (n *wireGuardNode) applyConfigToDeviceLocked(rebuildPeers bool) error {
	n.ensureDeviceLocked()
	if err := validateWireGuardConfig(n.config); err != nil {
		n.formError = err.Error()
		return err
	}
	active := n.hasActiveAttachmentsLocked()
	if !active {
		if err := n.dev.Down(); err != nil {
			return err
		}
	}
	deviceConfig, err := wireGuardDeviceConfig(n.config)
	if err != nil {
		return err
	}
	if err := n.dev.ApplyConfig(deviceConfig, wgdevice.ApplyConfigOptions{
		ReplacePeers:   rebuildPeers,
		ApplyEndpoints: active,
		ActivatePeers:  active,
	}); err != nil {
		return fmt.Errorf("apply config: %w", err)
	}
	n.refreshRuntimeStatsLocked()
	if !active {
		return nil
	}
	if err := n.dev.Up(); err != nil {
		return fmt.Errorf("bring device up: %w", err)
	}
	n.refreshRuntimeStatsLocked()
	return nil
}
func (n *wireGuardNode) hasActiveAttachmentsLocked() bool {
	return n.currentTun != nil && n.currentBind != nil
}

func (n *wireGuardNode) attachTun(t tun.Tun) {
	n.runtimeMu.Lock()
	n.ensureDeviceLocked()
	if SameComparableValue(n.currentTun, t) {
		n.runtimeMu.Unlock()
		return
	}
	var err error
	if n.currentTun == nil {
		err = n.dev.AttachTUN(t)
	} else {
		err = n.dev.ReplaceTUN(t)
	}
	if err != nil {
		n.runtimeMu.Unlock()
		_ = t.Close()
		n.setRuntimeError(fmt.Errorf("attach TUN: %w", err))
		return
	}
	n.currentTun = t
	generation := n.nextRuntimeGenerationLocked()
	hasBind := n.currentBind != nil
	n.runtimeMu.Unlock()
	n.cancelConfigRetry()
	if hasBind {
		// Reconfiguring after TUN attach can open/listen on the network bind, so
		// keep it on the async runtime path.
		n.scheduleConfigApply(false, generation, 0, false)
	} else {
		n.clearRuntimeError()
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}
func (n *wireGuardNode) attachNetwork(link uint64, network NetworkCloser) {
	n.cancelConfigRetry()
	n.runtimeMu.Lock()
	n.ensureDeviceLocked()
	n.currentNetwork = network
	generation := n.nextRuntimeGenerationLocked()
	n.runtimeMu.Unlock()
	BindNetworkResource(n.w, n.id, link, network)
	_ = n.updateLabel()
	n.sendStatusBlock()

	// Attaching the bind may touch sockets and apply WireGuard config, so it is
	// scheduled after the network resource has been made visible to the graph.
	n.scheduleNetworkBindAttach(generation, 0, false)
}

// attachCurrentNetworkBind owns the bind swap and optional device activation for
// one runtime generation. It reports whether failures are retryable so callers
// do not need to reread mutable runtime state after the work completes.
func (n *wireGuardNode) attachCurrentNetworkBind(generation uint64) (bool, error, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if generation != n.currentRuntimeGeneration() {
		return false, nil, false
	}
	if n.currentNetwork == nil {
		return false, errors.New("network is detached"), true
	}
	networkBind := conn.NewDefaultBind(n.currentNetwork)
	if !n.currentNetwork.IsNative() {
		networkBind = conn.NewDefaultBindWithOptions(n.currentNetwork, conn.StdNetBindOptions{
			AllowSingleFamily: true,
		})
	}
	var err error
	if n.currentBind == nil {
		err = n.dev.AttachBind(networkBind)
	} else {
		err = n.dev.ReplaceBind(networkBind)
	}
	if err != nil {
		_ = n.dev.DetachBind()
		_ = networkBind.Close()
		n.currentBind = nil
		return n.currentNetwork != nil, fmt.Errorf("attach network: %w", err), true
	}
	n.currentBind = networkBind
	active := n.hasActiveAttachmentsLocked()
	if active {
		return active, n.applyConfigToDeviceLocked(false), true
	}
	return false, nil, true
}
func wireGuardDeviceConfig(config wireGuardConfig) (wgdevice.DeviceConfig, error) {
	privateKey, err := parseWireGuardPrivateKey(config.privateKey)
	if err != nil {
		return wgdevice.DeviceConfig{}, fmt.Errorf("private key: %w", err)
	}
	amneziaWG, err := config.amneziaWG.deviceConfig()
	if err != nil {
		return wgdevice.DeviceConfig{}, fmt.Errorf("amneziawg: %w", err)
	}
	out := wgdevice.DeviceConfig{
		PrivateKey: privateKey,
		ListenPort: uint16(config.listenPort),
		AmneziaWG:  amneziaWG,
		Peers:      make([]wgdevice.PeerConfig, 0, len(config.peers)),
	}
	for _, peer := range config.peers {
		if peer.publicKey == "" {
			return wgdevice.DeviceConfig{}, errors.New("peer public key is required")
		}
		publicKey, err := parseWireGuardPublicKey(peer.publicKey)
		if err != nil {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
		}
		presharedKey, err := parseWireGuardPresharedKey(peer.presharedKey)
		if err != nil {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s preshared key: %w", peerLabel(peer), err)
		}
		allowedIPs, err := parseWireGuardAllowedIPs(peer.allowedIPs)
		if err != nil {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s allowed IPs: %w", peerLabel(peer), err)
		}
		if peer.persistentKeepaliveInterval < 0 || peer.persistentKeepaliveInterval > 65535 {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s persistent keepalive must be between 0 and 65535", peerLabel(peer))
		}
		peerConfig := wgdevice.PeerConfig{
			PublicKey:                   publicKey,
			PresharedKey:                presharedKey,
			ProtocolVersion:             1,
			Endpoint:                    peer.endpoint,
			PersistentKeepaliveInterval: uint16(peer.persistentKeepaliveInterval),
			AllowedIPs:                  allowedIPs,
		}
		if peer.amneziaWGOverride {
			amneziaWG, err := peer.amneziaWG.deviceConfig()
			if err != nil {
				return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s amneziawg: %w", peerLabel(peer), err)
			}
			peerConfig.AmneziaWG = &amneziaWG
		}
		out.Peers = append(out.Peers, peerConfig)
	}
	return out, nil
}
