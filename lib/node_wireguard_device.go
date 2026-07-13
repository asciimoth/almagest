package almagest

import (
	"errors"
	"fmt"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/gonnect/tun"
	wgdevice "github.com/asciimoth/wgo/device"
)

func (n *wireGuardNode) ensureDeviceLocked() {
	if n.dev != nil {
		return
	}
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.logger != nil {
		logger = n.logger
	}
	// wgo receives the node's engine worker spawner so its long-lived goroutines
	// remain visible to the engine worker tracker. This is safe only because
	// ensureDeviceLocked and all other wgo calls happen on runtime goroutines,
	// after workspace callbacks have released the workspace lock. Calling wgo
	// from a workspace callback would reintroduce the workspace -> runtimeMu ->
	// wgo -> workspace cycle.
	n.dev = wgdevice.NewDevice(nil, nil, logger, n.workerSpawner, wgdevice.DeviceOptions{
		BatchSize: n.config.batchSize,
	})
	n.runtimeStats = n.dev.RuntimeStats()
	n.subscribeToRuntimeStatsLocked(n.dev)
	n.subscribeToReceiveErrorsLocked(n.dev)
}
func (n *wireGuardNode) applyConfigToDevice(rebuildPeers bool) error {
	config := n.config.clone()
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	return n.applyConfigToDeviceLocked(config, rebuildPeers)
}
func (n *wireGuardNode) applyConfigToDeviceLocked(config wireGuardConfig, rebuildPeers bool) error {
	if err := validateWireGuardConfig(config); err != nil {
		return err
	}
	if err := n.detachStaleTunLocked(); err != nil {
		return err
	}
	n.runtimeListenPort = config.listenPort
	active := n.hasActiveAttachmentsLocked()
	if !active {
		if n.dev == nil {
			n.runtimeStats = wgdevice.RuntimeStats{}
			return nil
		}
		if err := n.dev.Down(); err != nil {
			return err
		}
	}
	n.ensureDeviceLocked()
	deviceConfig, err := wireGuardDeviceConfig(config)
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
		// A failed device bring-up can leave a non-nil bind behind, but that
		// bind is not reusable until the attach path rebuilds it.
		n.currentBindFailed = true
		return fmt.Errorf("bring device up: %w", err)
	}
	n.runtimeListenPort = int(n.dev.ListenPort())
	n.refreshRuntimeStatsLocked()
	return nil
}
func (n *wireGuardNode) hasActiveAttachmentsLocked() bool {
	return n.currentTunActiveLocked() && n.currentNetworkActiveLocked() && n.currentBind != nil && !n.currentBindFailed
}

func (n *wireGuardNode) currentTunActiveLocked() bool {
	return n.currentTun != nil && n.currentTunGeneration == n.currentTunGenerationValue()
}
func (n *wireGuardNode) currentNetworkActiveLocked() bool {
	// Generation match and presence are not enough: wrapper-preserving
	// middleware can close and later re-emit the same underlying source.
	return n.currentNetwork != nil &&
		n.currentNetworkGeneration == n.currentNetworkGenerationValue() &&
		NetworkIsReady(n.currentNetwork)
}

func (n *wireGuardNode) detachStaleTunLocked() error {
	if n.dev == nil || n.currentTun == nil || n.currentTunGeneration == n.currentTunGenerationValue() {
		return nil
	}
	// A TUN event can bring wgo up after Almagest has already invalidated the
	// TUN generation. Remove that stale TUN before ApplyConfig touches the
	// listen port, because wgo updates the bind when it thinks the device is up.
	n.currentTun = nil
	n.currentTunGeneration = 0
	if err := n.dev.Down(); err != nil {
		return fmt.Errorf("bring device down before detaching stale TUN: %w", err)
	}
	if err := n.dev.DetachTUN(); err != nil {
		return fmt.Errorf("detach stale TUN: %w", err)
	}
	return nil
}

func (n *wireGuardNode) attachCurrentTun(tunGeneration uint64, t tun.Tun) (bool, bool, error) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if tunGeneration != n.currentTunGenerationValue() {
		return false, false, nil
	}
	if t == nil {
		return false, false, nil
	}
	if !TunIsReady(t) {
		_ = t.Close()
		return false, false, nil
	}
	if SameTunInstance(n.currentTun, t) {
		n.currentTunGeneration = tunGeneration
		return n.currentBind != nil, true, nil
	}
	n.ensureDeviceLocked()
	drainWireGuardTunEvents(t)
	var err error
	if n.currentTun == nil {
		err = n.dev.AttachTUN(t)
	} else {
		err = n.dev.ReplaceTUN(t)
	}
	if err != nil {
		_ = t.Close()
		return false, true, fmt.Errorf("attach TUN: %w", err)
	}
	n.currentTun = t
	n.currentTunGeneration = tunGeneration
	return n.currentBind != nil, true, nil
}

// Detached TUN wrappers can have buffered lifecycle events, including their
// initial EventUp. Almagest explicitly brings the device up after config apply,
// so old buffered events should not drive wgo's state machine after attach.
func drainWireGuardTunEvents(t tun.Tun) {
	if t == nil {
		return
	}
	events := t.Events()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func (n *wireGuardNode) attachNetworkPayload(networkGeneration uint64, network NetworkCloser) (bool, bool, bool, error) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if networkGeneration != n.currentNetworkGenerationValue() {
		return false, false, false, nil
	}
	if network == nil {
		return false, false, false, nil
	}
	if !NetworkIsReady(network) {
		_ = network.Close()
		return false, false, false, nil
	}
	if SameNetworkInstance(n.currentNetwork, network) {
		n.currentNetworkGeneration = networkGeneration
		if n.currentBind == nil || n.currentBindFailed {
			// A same-source re-emit is also the recovery signal for a failed
			// network bind; rebuild before config apply can clear the error.
			retry, err := n.attachCurrentNetworkBindLocked()
			return n.hasActiveAttachmentsLocked(), true, retry, err
		}
		return n.hasActiveAttachmentsLocked(), true, false, nil
	}
	n.currentNetwork = network
	n.currentNetworkGeneration = networkGeneration
	n.currentBindFailed = false
	retry, err := n.attachCurrentNetworkBindLocked()
	return n.hasActiveAttachmentsLocked(), true, retry, err
}

func (n *wireGuardNode) attachCurrentNetworkBind(networkGeneration uint64) (bool, bool, bool, error) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if networkGeneration != n.currentNetworkGenerationValue() {
		return false, false, false, nil
	}
	retry, err := n.attachCurrentNetworkBindLocked()
	return n.hasActiveAttachmentsLocked(), true, retry, err
}

// attachCurrentNetworkBindLocked installs or rebuilds the UDP bind for the
// current desired network. It intentionally leaves the device down when a TUN
// is active; the following config-apply job is responsible for bringing the
// device up with the latest workspace-owned config snapshot.
func (n *wireGuardNode) attachCurrentNetworkBindLocked() (bool, error) {
	if !n.currentNetworkActiveLocked() {
		return false, errors.New("network is detached")
	}
	n.ensureDeviceLocked()
	networkBindOptions := conn.StdNetBindOptions{
		BatchSize: n.config.batchSize,
	}
	if !n.currentNetwork.IsNative() {
		networkBindOptions.AllowSingleFamily = true
	}
	networkBind := conn.NewDefaultBindWithOptions(n.currentNetwork, networkBindOptions)
	activeTun := n.currentTunActiveLocked()
	if activeTun {
		if n.currentBind != nil {
			n.nextBindGenerationLocked()
		}
		if err := n.dev.Down(); err != nil {
			_ = networkBind.Close()
			return n.currentNetworkActiveLocked(), fmt.Errorf("bring device down before attaching network: %w", err)
		}
	}
	var err error
	bindGeneration := n.nextBindGenerationLocked()
	bindState := &wireGuardTrackedBindState{}
	trackedBind := wireGuardTrackedBind{Bind: networkBind, generation: bindGeneration, state: bindState}
	if n.currentBind == nil {
		err = n.dev.AttachBind(trackedBind)
	} else {
		err = n.dev.ReplaceBind(trackedBind)
	}
	if err != nil {
		_ = n.dev.DetachBind()
		_ = networkBind.Close()
		n.currentBind = nil
		n.currentBindGen = 0
		n.currentBindOpen = nil
		n.currentBindFailed = false
		return n.currentNetworkActiveLocked(), fmt.Errorf("attach network: %w", err)
	}
	n.currentBind = trackedBind
	n.currentBindOpen = &bindState.open
	n.currentBindFailed = false
	return false, nil
}

func (n *wireGuardNode) detachCurrentTun(tunGeneration uint64) (error, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if n.currentTun == nil || n.currentTunGeneration > tunGeneration {
		return nil, false
	}
	n.currentTun = nil
	n.currentTunGeneration = 0
	if n.dev == nil {
		return nil, true
	}
	var detachErr error
	if err := n.dev.Down(); err != nil {
		detachErr = fmt.Errorf("bring device down before detaching TUN: %w", err)
	}
	if err := n.dev.DetachTUN(); err != nil {
		detachErr = fmt.Errorf("detach TUN: %w", err)
	}
	return detachErr, true
}

func (n *wireGuardNode) detachCurrentNetwork(networkGeneration uint64) (error, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if n.currentNetwork == nil || n.currentNetworkGeneration > networkGeneration {
		return nil, false
	}
	n.currentNetwork = nil
	n.currentNetworkGeneration = 0
	n.currentBind = nil
	n.currentBindGen = 0
	n.currentBindOpen = nil
	n.currentBindFailed = false
	if n.dev == nil {
		return nil, true
	}
	var detachErr error
	if err := n.dev.Down(); err != nil {
		detachErr = fmt.Errorf("bring device down before detaching network bind: %w", err)
	}
	if err := n.dev.DetachBind(); err != nil {
		detachErr = fmt.Errorf("detach network bind: %w", err)
	}
	return detachErr, true
}

func (n *wireGuardNode) stopRuntime() {
	n.runtimeMu.Lock()
	n.unsubscribeFromRuntimeStatsLocked()
	n.unsubscribeFromReceiveErrorsLocked()
	dev := n.dev
	n.dev = nil
	n.currentTun = nil
	n.currentTunGeneration = 0
	n.currentNetwork = nil
	n.currentNetworkGeneration = 0
	n.currentBind = nil
	n.currentBindGen = 0
	n.currentBindOpen = nil
	n.currentBindFailed = false
	n.runtimeStats = wgdevice.RuntimeStats{}
	n.runtimeListenPort = 0
	n.runtimeMu.Unlock()
	if dev != nil {
		dev.Close()
	}
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
