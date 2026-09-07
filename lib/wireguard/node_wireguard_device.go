package wireguardnodes

import (
	"errors"
	"fmt"

	"github.com/asciimoth/gonnect/tun"
	wgdevice "github.com/asciimoth/wgo/device"
)

func (n *wireGuardNode) ensureDeviceLocked(batchSize int) {
	if n.dev != nil {
		return
	}
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.Logger != nil {
		logger = n.Logger
	}
	// wgo receives the node's engine worker spawner so its long-lived goroutines
	// remain visible to the engine worker tracker. This is safe only because
	// ensureDeviceLocked and all other wgo calls happen on runtime goroutines,
	// after workspace callbacks have released the workspace lock. Calling wgo
	// from a workspace callback would reintroduce the workspace -> runtimeMu ->
	// wgo -> workspace cycle.
	//
	// Keep handshake processing sequential for each device. wgo otherwise starts
	// one handshake consumer per CPU, but every consumer mutates the same state
	// machine in each peer. An initiation and response can then overtake each
	// other between the protocol operations. This can install different current
	// keypairs on the two peers: traffic works in one direction, incoming
	// keepalives keep the stale state alive, and the normal handshake retry never
	// repairs it. Encryption and decryption workers remain parallel.
	spawner := newWireGuardDeviceWorkerSpawner(n.WorkerSpawner)
	options := wgdevice.DeviceOptions{
		BatchSize: batchSize,
	}
	if spawner == nil {
		// Runtime-backed nodes always have a spawner. Keep direct test and
		// library construction safe when no spawner is available to select the
		// single handshake consumer.
		options.WorkerCount = 1
	}
	n.dev = wgdevice.NewDevice(nil, nil, logger, spawner, options)
	n.runtimeDeviceUp = false
	n.runtimeStats = n.dev.RuntimeStats()
	n.subscribeToRuntimeStatsLocked(n.dev)
	n.subscribeToReceiveErrorsLocked(n.dev)
}
func (n *wireGuardNode) applyConfigToDevice(rebuildPeers bool) error {
	config := n.effectiveConfig()
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
	// Config convergence must repair an attached network bind before it can
	// succeed. A failed active ApplyConfig can leave the device down and the bind
	// unusable. In that state, shouldRunDeviceLocked is false, so applying config
	// without this repair would skip endpoint activation and device Up, then
	// cancel the only recovery retry.
	if n.currentNetworkActiveLocked() && (n.currentBind == nil || n.currentBindFailed) {
		if _, err := n.attachCurrentNetworkBindLocked(config.batchSize); err != nil {
			return fmt.Errorf("repair network bind before applying config: %w", err)
		}
	}
	n.runtimeListenPort = config.listenPort
	running := n.shouldRunDeviceLocked()
	if !running && n.dev != nil {
		if err := n.bringDeviceDownLocked(); err != nil {
			return err
		}
	}
	n.ensureDeviceLocked(config.batchSize)
	deviceConfig, err := wireGuardDeviceConfig(config)
	if err != nil {
		return err
	}
	nextPeerKeys := make(map[wgdevice.NoisePublicKey]struct{}, len(deviceConfig.Peers))
	for _, peer := range deviceConfig.Peers {
		nextPeerKeys[peer.PublicKey] = struct{}{}
	}
	if rebuildPeers {
		for key := range n.runtimeOwnedPeerKeys {
			if _, keep := nextPeerKeys[key]; keep {
				continue
			}
			if _, err := n.dev.DeletePeer(key); err != nil {
				return fmt.Errorf("delete removed local peer: %w", err)
			}
		}
	}
	// ApplyConfig can make a partial peer change before it returns an error.
	// Record every key that this attempt can create, so a later desired config
	// can remove partial results. Deleting a peer that was not created is safe.
	n.runtimeOwnedPeerKeys = nextPeerKeys
	if err := n.dev.ApplyConfig(deviceConfig, wgdevice.ApplyConfigOptions{
		// ReplacePeers is intentionally false. WGConfig nodes own peers through
		// detached APIs, and a global replacement would delete those peers.
		ReplacePeers:   false,
		ApplyEndpoints: running,
		ActivatePeers:  running,
	}); err != nil {
		if running {
			// ApplyConfig refreshes an active default bind before it applies
			// peers. If that refresh fails, wgo can keep its logical device state
			// up while its receive sockets are closed. Force recovery through a
			// bind replacement and do not let a later attachment completion use
			// the stale runtimeDeviceUp value.
			n.runtimeDeviceUp = false
			n.currentBindFailed = true
		}
		return fmt.Errorf("apply config: %w", err)
	}
	n.controllerSource.Store(n.dev)
	n.refreshRuntimeStatsLocked()
	if !running {
		return nil
	}
	if err := n.bringDeviceUpLocked(); err != nil {
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
	return n.currentTunActiveLocked() && n.hasActiveTransportLocked()
}

func (n *wireGuardNode) hasActiveTransportLocked() bool {
	return n.currentNetworkActiveLocked() && n.currentBind != nil && !n.currentBindFailed
}

func (n *wireGuardNode) configApplyNeededLocked() bool {
	return n.appliedConfigGeneration != n.currentConfigGeneration() ||
		n.currentNetworkActiveLocked() && (n.currentBind == nil || n.currentBindFailed) ||
		n.currentTunActiveLocked() && n.hasActiveTransportLocked() && !n.runtimeDeviceUp
}

// shouldRunDeviceLocked keeps an already-running transport and its peers alive
// while the node waits for a replacement TUN. A device that has not completed
// its first full attachment still waits for both TUN and network inputs.
func (n *wireGuardNode) shouldRunDeviceLocked() bool {
	return n.hasActiveAttachmentsLocked() || n.runtimeDeviceUp && n.hasActiveTransportLocked()
}

func (n *wireGuardNode) bringDeviceUpLocked() error {
	if n.dev == nil {
		n.runtimeDeviceUp = false
		return nil
	}
	// Device.Up returns with the device down when any default or named
	// transport cannot reopen. Clear the mirror before the call so a failed
	// reopen cannot leave this node believing that the previous up state is
	// still active. Attachment completion uses this value to decide whether it
	// must schedule a new config apply, which is the recovery path after a
	// concurrent controller transport replacement.
	n.runtimeDeviceUp = false
	if err := n.dev.Up(); err != nil {
		return err
	}
	n.runtimeDeviceUp = true
	return nil
}

func (n *wireGuardNode) bringDeviceDownLocked() error {
	if n.dev == nil {
		n.runtimeDeviceUp = false
		return nil
	}
	// Device.Down transitions to down even when closing one transport reports
	// an error. Keep the local state conservative on that error path too.
	n.runtimeDeviceUp = false
	if err := n.dev.Down(); err != nil {
		return err
	}
	return nil
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
	// Remove the stale attachment without stopping the device. WireGuard keeps
	// its transport and peer sessions alive while it waits for the replacement.
	n.currentTun = nil
	n.currentTunGeneration = 0
	if err := n.dev.DetachTUN(); err != nil {
		return fmt.Errorf("detach stale TUN: %w", err)
	}
	return nil
}

func (n *wireGuardNode) attachCurrentTun(tunGeneration uint64, t tun.Tun, batchSize int) (bool, bool, error) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if tunGeneration != n.currentTunGenerationValue() {
		if t != nil {
			_ = t.Close()
		}
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
		return n.configApplyNeededLocked(), true, nil
	}
	n.ensureDeviceLocked(batchSize)
	drainWireGuardTunEvents(t)
	// A running device already has its current config and transport. Replacing
	// only its TUN must not reapply device config or refresh the UDP bind.
	managedTun, err := n.newManagedTun(t)
	if err != nil {
		_ = t.Close()
		return false, true, fmt.Errorf("prepare TUN event filter: %w", err)
	}
	if n.currentTun == nil {
		err = n.dev.AttachTUN(managedTun)
	} else {
		err = n.dev.ReplaceTUN(managedTun)
	}
	if err != nil {
		_ = t.Close()
		return false, true, fmt.Errorf("attach TUN: %w", err)
	}
	n.currentTun = t
	n.currentTunGeneration = tunGeneration
	return n.configApplyNeededLocked(), true, nil
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

func (n *wireGuardNode) attachNetworkPayload(networkGeneration uint64, network NetworkCloser, batchSize int) (bool, bool, bool, error) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if networkGeneration != n.currentNetworkGenerationValue() {
		if network != nil {
			_ = network.Close()
		}
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
			retry, err := n.attachCurrentNetworkBindLocked(batchSize)
			return n.configApplyNeededLocked(), true, retry, err
		}
		return n.configApplyNeededLocked(), true, false, nil
	}
	oldNetwork := n.currentNetwork
	n.currentNetwork = network
	n.currentNetworkGeneration = networkGeneration
	n.currentBindFailed = false
	retry, err := n.attachCurrentNetworkBindLocked(batchSize)
	if oldNetwork != nil {
		_ = oldNetwork.Close()
	}
	return n.configApplyNeededLocked(), true, retry, err
}

func (n *wireGuardNode) attachCurrentNetworkBind(networkGeneration uint64, batchSize int) (bool, bool, bool, error) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if networkGeneration != n.currentNetworkGenerationValue() {
		return false, false, false, nil
	}
	retry, err := n.attachCurrentNetworkBindLocked(batchSize)
	return n.configApplyNeededLocked(), true, retry, err
}

// attachCurrentNetworkBindLocked installs or rebuilds the UDP bind for the
// current desired network. It intentionally leaves the device down when a TUN
// is active; the following config-apply job is responsible for bringing the
// device up with the latest workspace-owned config snapshot.
func (n *wireGuardNode) attachCurrentNetworkBindLocked(batchSize int) (bool, error) {
	if !n.currentNetworkActiveLocked() {
		return false, errors.New("network is detached")
	}
	n.ensureDeviceLocked(batchSize)
	networkBind := newWireGuardNetworkBind(n.currentNetwork, batchSize)
	activeTun := n.currentTunActiveLocked()
	if activeTun {
		if n.currentBind != nil {
			n.nextBindGenerationLocked()
		}
		if err := n.bringDeviceDownLocked(); err != nil {
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

func (n *wireGuardNode) detachCurrentTun(tunGeneration uint64) (bool, error, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if n.currentTun == nil || n.currentTunGeneration > tunGeneration {
		return false, nil, false
	}
	n.currentTun = nil
	n.currentTunGeneration = 0
	if n.dev == nil {
		return n.configApplyNeededLocked(), nil, true
	}
	if err := n.dev.DetachTUN(); err != nil {
		return false, fmt.Errorf("detach TUN: %w", err), true
	}
	return n.configApplyNeededLocked(), nil, true
}

func (n *wireGuardNode) detachCurrentNetwork(networkGeneration uint64) (bool, error, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if n.currentNetwork == nil || n.currentNetworkGeneration > networkGeneration {
		return false, nil, false
	}
	n.currentNetwork = nil
	n.currentNetworkGeneration = 0
	n.currentBind = nil
	n.currentBindGen = 0
	n.currentBindOpen = nil
	n.currentBindFailed = false
	if n.dev == nil {
		return n.configApplyNeededLocked(), nil, true
	}
	var detachErr error
	if err := n.bringDeviceDownLocked(); err != nil {
		detachErr = fmt.Errorf("bring device down before detaching network bind: %w", err)
	}
	if err := n.dev.DetachBind(); err != nil {
		detachErr = fmt.Errorf("detach network bind: %w", err)
	}
	if detachErr != nil {
		return false, detachErr, true
	}
	return n.configApplyNeededLocked(), nil, true
}

func (n *wireGuardNode) stopRuntime() {
	n.runtimeMu.Lock()
	n.unsubscribeFromRuntimeStatsLocked()
	n.unsubscribeFromReceiveErrorsLocked()
	dev := n.dev
	n.dev = nil
	n.controllerSource.Store(nil)
	n.currentTun = nil
	n.currentTunGeneration = 0
	n.currentNetwork = nil
	n.currentNetworkGeneration = 0
	n.currentBind = nil
	n.currentBindGen = 0
	n.currentBindOpen = nil
	n.currentBindFailed = false
	n.runtimeDeviceUp = false
	n.appliedConfigGeneration = 0
	n.runtimeStats = wgdevice.RuntimeStats{}
	n.runtimeOwnedPeerKeys = nil
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
	amneziaWG, err := config.amneziaWG.deviceConfigWhenEnabled(config.amneziaWGEnabled)
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
		peerConfig, err := wireGuardPeerDeviceConfig(peer)
		if err != nil {
			return wgdevice.DeviceConfig{}, err
		}
		out.Peers = append(out.Peers, peerConfig)
	}
	return out, nil
}

// wireGuardPeerDeviceConfig converts the shared persisted/menu peer model to
// wgo's validated configuration model. WireGuard and WGConfig use this one
// conversion path so their form fields have identical semantics.
func wireGuardPeerDeviceConfig(peer wireGuardPeerConfig) (wgdevice.PeerConfig, error) {
	if peer.publicKey == "" {
		return wgdevice.PeerConfig{}, errors.New("peer public key is required")
	}
	publicKey, err := parseWireGuardPublicKey(peer.publicKey)
	if err != nil {
		return wgdevice.PeerConfig{}, fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
	}
	presharedKey, err := parseWireGuardPresharedKey(peer.presharedKey)
	if err != nil {
		return wgdevice.PeerConfig{}, fmt.Errorf("peer %s preshared key: %w", peerLabel(peer), err)
	}
	allowedIPs, err := parseWireGuardAllowedIPs(peer.allowedIPs)
	if err != nil {
		return wgdevice.PeerConfig{}, fmt.Errorf("peer %s allowed IPs: %w", peerLabel(peer), err)
	}
	if peer.persistentKeepaliveInterval < 0 || peer.persistentKeepaliveInterval > 65535 {
		return wgdevice.PeerConfig{}, fmt.Errorf("peer %s persistent keepalive must be between 0 and 65535", peerLabel(peer))
	}
	out := wgdevice.PeerConfig{
		PublicKey:                   publicKey,
		PresharedKey:                presharedKey,
		ProtocolVersion:             1,
		Endpoint:                    peer.endpoint,
		PersistentKeepaliveInterval: uint16(peer.persistentKeepaliveInterval),
		AllowedIPs:                  allowedIPs,
	}
	if peer.amneziaWGOverride {
		amneziaWG, err := peer.amneziaWG.deviceConfigWhenEnabled(peer.amneziaWGEnabled)
		if err != nil {
			return wgdevice.PeerConfig{}, fmt.Errorf("peer %s amneziawg: %w", peerLabel(peer), err)
		}
		out.AmneziaWG = &amneziaWG
	}
	return out, nil
}
