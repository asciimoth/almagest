package almagest

import (
	"errors"
	"fmt"
	"time"

	"github.com/asciimoth/pasta/pasta"
	wgdevice "github.com/asciimoth/wgo/device"
)

type wireGuardRetryMessage struct {
	generation uint64
}
type wireGuardReceiveErrorMessage struct {
	dev                *wgdevice.Device
	generation         uint64
	bindGeneration     uint64
	bindOpenGeneration uint64
	intentionalClose   bool
	err                error
}

// wireGuardRuntimeResultMessage is the single completion path for async
// runtime work. It covers bind attach, config apply, and retry attempts so stale
// generation checks and retry/error handling stay in one place.
type wireGuardRuntimeResultMessage struct {
	generation      uint64
	retryGeneration uint64
	checkRetry      bool
	retryOnError    bool
	ready           bool
	err             error
}

// wireGuardRuntimeWork runs without the workspace callback on the stack.
// retryOnError says whether a runtime failure should schedule another retry;
// ok is false when the work became stale and no completion should be delivered.
type wireGuardRuntimeWork func() (retryOnError bool, err error, ok bool)
type wireGuardRuntimeStatsMessage struct {
	dev   *wgdevice.Device
	stats wgdevice.RuntimeStats
}

const (
	wireGuardConfigRetryInitialDelay = 100 * time.Millisecond
	wireGuardConfigRetryMaxDelay     = 10 * time.Second
)

func (n *wireGuardNode) nextRuntimeGenerationLocked() uint64 {
	return n.runtimeGeneration.Add(1)
}

// currentRuntimeGeneration is atomic so completion handlers can discard stale
// work without taking runtimeMu while the runtime worker is still active.
func (n *wireGuardNode) currentRuntimeGeneration() uint64 {
	return n.runtimeGeneration.Load()
}
func (n *wireGuardNode) scheduleConfigApply(rebuildPeers bool, runtimeGeneration, retryGeneration uint64, checkRetry bool) {
	n.scheduleRuntimeWork(runtimeGeneration, retryGeneration, checkRetry, func() (bool, error, bool) {
		n.runtimeMu.Lock()
		defer n.runtimeMu.Unlock()
		if runtimeGeneration != n.currentRuntimeGeneration() {
			return false, nil, false
		}
		err := n.applyConfigToDeviceLocked(rebuildPeers)
		return n.hasActiveAttachmentsLocked(), err, true
	})
}

// scheduleRuntimeWork serializes expensive device operations behind runtimeMu
// while keeping them out of workspace callbacks. Completion is sent back through
// the node inbox so status and retry changes happen on the normal node path.
func (n *wireGuardNode) scheduleRuntimeWork(generation, retryGeneration uint64, checkRetry bool, work wireGuardRuntimeWork) {
	if n.w == nil || n.id == 0 {
		return
	}
	go func() {
		if checkRetry && !n.isCurrentConfigRetry(retryGeneration) {
			return
		}
		retryOnError, err, ok := work()
		if !ok {
			return
		}
		n.sendInternal(wireGuardRuntimeResultMessage{
			generation:      generation,
			retryGeneration: retryGeneration,
			checkRetry:      checkRetry,
			retryOnError:    retryOnError,
			ready:           err == nil && retryOnError,
			err:             err,
		})
	}()
}

// handleRuntimeResult applies only the currently relevant async completion.
// Older completions are expected during restore/relink/retry churn and are
// dropped before they can clear a newer error or cancel a newer retry.
func (n *wireGuardNode) handleRuntimeResult(msg wireGuardRuntimeResultMessage) {
	if !n.acceptRuntimeResult(msg) {
		return
	}
	if msg.err != nil {
		n.failRuntime(msg.err, msg.retryOnError)
		return
	}
	n.markRuntimeReady(msg.generation, msg.ready)
	n.cancelConfigRetry()
	n.clearRuntimeError()
	_ = n.updateLabel()
	n.sendStatusBlock()
}

// acceptRuntimeResult validates both runtime resource generation and retry
// generation. Retry generation is checked only for completions that came from a
// scheduled retry; first-attempt async work has no retry generation to match.
func (n *wireGuardNode) acceptRuntimeResult(msg wireGuardRuntimeResultMessage) bool {
	if msg.generation != n.currentRuntimeGeneration() {
		return false
	}
	if msg.checkRetry && !n.isCurrentConfigRetry(msg.retryGeneration) {
		return false
	}
	return true
}

func (n *wireGuardNode) markRuntimeReady(generation uint64, active bool) {
	if !active {
		return
	}
	n.runtimeMu.Lock()
	if generation == n.currentRuntimeGeneration() && n.hasActiveAttachmentsLocked() {
		n.runtimeReadyGeneration = generation
	}
	n.runtimeMu.Unlock()
}

// sendInternal delivers node-owned messages through the workspace inbox rather
// than the formular channel. The inbox is used here as the async boundary
// between runtime goroutines and node state updates.
func (n *wireGuardNode) sendInternal(payload any) {
	if n.w == nil || n.id == 0 || payload == nil {
		return
	}
	n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: payload})
}

// deferInternal prevents inbox handlers from blocking the workspace lock behind
// runtimeMu. Handlers that need runtime state requeue briefly if runtime work is
// still holding the device lock.
func (n *wireGuardNode) deferInternal(payload any) {
	if n.w == nil || n.id == 0 || payload == nil {
		return
	}
	go func() {
		timer := time.NewTimer(10 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		n.sendInternal(payload)
	}()
}
func (n *wireGuardNode) scheduleNetworkBindAttach(generation, retryGeneration uint64, checkRetry bool) {
	n.scheduleRuntimeWork(generation, retryGeneration, checkRetry, func() (bool, error, bool) {
		return n.attachCurrentNetworkBind(generation)
	})
}
func (n *wireGuardNode) notifyReceiveError(dev *wgdevice.Device, err error) {
	if n.w == nil || n.id == 0 {
		return
	}
	var trackedErr wireGuardTrackedBindReceiveError
	var bindGeneration uint64
	var bindOpenGeneration uint64
	var intentionalClose bool
	if errors.As(err, &trackedErr) {
		bindGeneration = trackedErr.generation
		bindOpenGeneration = trackedErr.openGeneration
		intentionalClose = trackedErr.intentionalClose
		err = trackedErr.err
	}
	go func() {
		n.sendInternal(wireGuardReceiveErrorMessage{
			dev:                dev,
			generation:         n.currentRuntimeGeneration(),
			bindGeneration:     bindGeneration,
			bindOpenGeneration: bindOpenGeneration,
			intentionalClose:   intentionalClose,
			err:                err,
		})
	}()
}
func (n *wireGuardNode) handleReceiveError(msg wireGuardReceiveErrorMessage) {
	if msg.dev == nil || msg.err == nil {
		return
	}
	if msg.generation != n.currentRuntimeGeneration() {
		return
	}
	if !n.runtimeMu.TryLock() {
		n.deferInternal(msg)
		return
	}
	current := msg.dev == n.dev && msg.generation == n.currentRuntimeGeneration()
	if current && msg.bindGeneration != 0 && msg.bindGeneration != n.currentBindGen {
		current = false
	}
	if current && msg.intentionalClose && msg.bindOpenGeneration != 0 && n.currentBindOpen != nil && msg.bindOpenGeneration != n.currentBindOpen.Load() {
		current = false
	}
	retry := n.currentTun != nil && n.currentNetwork != nil && n.currentBind != nil
	if current && retry {
		n.currentBindFailed = true
		n.nextRuntimeGenerationLocked()
	}
	n.runtimeMu.Unlock()
	if !current {
		return
	}
	n.failRuntime(fmt.Errorf("receive from bind: %w", msg.err), retry)
	_ = n.updateLabel()
	n.sendStatusBlock()
}
func (n *wireGuardNode) retryConfigToDevice(generation uint64) {
	if !n.isCurrentConfigRetry(generation) {
		return
	}
	n.runtimeMu.Lock()
	needsBind := (n.currentBind == nil || n.currentBindFailed) && n.currentNetwork != nil
	runtimeGeneration := n.currentRuntimeGeneration()
	n.runtimeMu.Unlock()
	if needsBind {
		// If an earlier bind attach failed before currentBind was installed, or
		// the active receive routine failed, recreate the bind before config can
		// be applied.
		n.scheduleNetworkBindAttach(runtimeGeneration, generation, true)
		return
	}
	n.scheduleConfigApply(false, runtimeGeneration, generation, true)
}
func (n *wireGuardNode) failRuntime(err error, retry bool) {
	n.setRuntimeError(err)
	if retry {
		n.scheduleConfigRetry()
	}
}
func (n *wireGuardNode) scheduleConfigRetry() {
	if n.w == nil || n.id == 0 {
		return
	}

	n.retryMu.Lock()
	if n.retryCancel != nil {
		n.retryMu.Unlock()
		return
	}
	delay := n.retryDelay
	if delay <= 0 {
		delay = wireGuardConfigRetryInitialDelay
	}
	nextDelay := delay * 2
	if nextDelay > wireGuardConfigRetryMaxDelay {
		nextDelay = wireGuardConfigRetryMaxDelay
	}
	n.retryDelay = nextDelay
	generation := n.retryGeneration
	cancel := make(chan struct{})
	n.retryCancel = cancel
	n.retryMu.Unlock()

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-cancel:
			return
		}

		n.retryMu.Lock()
		if n.retryCancel != cancel {
			n.retryMu.Unlock()
			return
		}
		n.retryCancel = nil
		n.retryMu.Unlock()

		n.sendInternal(wireGuardRetryMessage{generation: generation})
	}()
}
func (n *wireGuardNode) cancelConfigRetry() {
	n.retryMu.Lock()
	n.retryGeneration++
	if n.retryCancel != nil {
		close(n.retryCancel)
		n.retryCancel = nil
	}
	n.retryDelay = wireGuardConfigRetryInitialDelay
	n.retryMu.Unlock()
}
func (n *wireGuardNode) isCurrentConfigRetry(generation uint64) bool {
	n.retryMu.Lock()
	defer n.retryMu.Unlock()
	return n.retryGeneration == generation
}

// sendTunConfig publishes this WireGuard interface's owned IP prefixes and
// peer route prefixes to the attached TUN provider. TypeTun defines TunConfig
// as flowing opposite the TUN payload, so this left-directed consumer sends
// suggestions upstream to the right-directed provider whenever the link or
// WireGuard config changes.
func (n *wireGuardNode) sendTunConfig() {
	if n.tunLink == 0 {
		return
	}
	n.sendTunConfigToLink(n.tunLink)
}
func (n *wireGuardNode) sendTunConfigToLink(link uint64) {
	if n.w == nil || n.id == 0 || link == 0 || link != n.tunLink {
		return
	}
	EmitTunConfig(n.w, n.id, link, n.tunConfig())
}
func (n *wireGuardNode) tunConfig() TunConfig {
	return TunConfig{
		Addrs:  wireGuardOwnedIPAddrs(n.config.ownedIPs),
		Routes: wireGuardPeerRoutes(n.config.peers),
	}
}
func (n *wireGuardNode) updateLabel() error {
	return n.SetLabel(n.stateText())
}
func (n *wireGuardNode) stateText() string {
	n.runtimeMu.Lock()
	hasTun := n.currentTun != nil
	hasNetwork := n.currentNetwork != nil
	bindFailed := n.currentBindFailed
	ready := n.runtimeReadyGeneration == n.currentRuntimeGeneration() && n.hasActiveAttachmentsLocked()
	n.runtimeMu.Unlock()
	switch {
	case n.formError != "":
		return "invalid config"
	case n.runtimeError != "" || bindFailed:
		return "error"
	case n.config.privateKey == "":
		return "missing private key"
	case !hasTun && !hasNetwork:
		return "waiting for TUN and network"
	case !hasTun:
		return "waiting for TUN"
	case !hasNetwork:
		return "waiting for network"
	case !ready:
		return "starting"
	default:
		return "up"
	}
}
func (n *wireGuardNode) setRuntimeError(err error) {
	if err == nil {
		n.clearRuntimeError()
		return
	}
	text := err.Error()
	if n.runtimeError == text {
		return
	}
	n.runtimeError = text
	if n.logger != nil {
		n.logger.Errf("WireGuard node error: %v", err)
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}
func (n *wireGuardNode) clearRuntimeError() {
	if n.runtimeError == "" {
		return
	}
	n.runtimeError = ""
	_ = n.updateLabel()
	n.sendStatusBlock()
}
func (n *wireGuardNode) subscribeToRuntimeStatsLocked(dev *wgdevice.Device) {
	if dev == nil || n.w == nil || n.id == 0 {
		return
	}
	n.unsubscribeFromRuntimeStatsLocked()
	n.unsubscribeRuntimeStats = dev.SubscribeRuntimeStats(func(stats wgdevice.RuntimeStats) {
		n.queueRuntimeStats(dev, stats)
	})
}
func (n *wireGuardNode) unsubscribeFromRuntimeStatsLocked() {
	if n.unsubscribeRuntimeStats == nil {
		return
	}
	n.unsubscribeRuntimeStats()
	n.unsubscribeRuntimeStats = nil
}
func (n *wireGuardNode) subscribeToReceiveErrorsLocked(dev *wgdevice.Device) {
	if dev == nil || n.w == nil || n.id == 0 {
		return
	}
	n.unsubscribeFromReceiveErrorsLocked()
	n.unsubscribeReceiveErrs = dev.SubscribeReceiveErrors(func(receiveErr wgdevice.ReceiveError) {
		if !wgdevice.ReceiveErrorShouldRecover(receiveErr.Err) {
			return
		}
		n.notifyReceiveError(dev, receiveErr.Err)
	})
}
func (n *wireGuardNode) unsubscribeFromReceiveErrorsLocked() {
	if n.unsubscribeReceiveErrs == nil {
		return
	}
	n.unsubscribeReceiveErrs()
	n.unsubscribeReceiveErrs = nil
}
func (n *wireGuardNode) queueRuntimeStats(dev *wgdevice.Device, stats wgdevice.RuntimeStats) {
	if n.w == nil || n.id == 0 {
		return
	}
	n.statsMu.Lock()
	n.pendingRuntimeStatsDev = dev
	n.pendingRuntimeStats = stats
	if n.runtimeStatsQueued {
		n.statsMu.Unlock()
		return
	}
	n.runtimeStatsQueued = true
	n.statsMu.Unlock()
	go func() {
		n.sendInternal(wireGuardRuntimeStatsMessage{dev: dev})
	}()
}

// takeRuntimeStats consumes the latest coalesced stats only after the inbox
// handler can take runtimeMu. That avoids losing an update while runtime work is
// still mutating the device state.
func (n *wireGuardNode) takeRuntimeStats(dev *wgdevice.Device) (wgdevice.RuntimeStats, bool) {
	n.statsMu.Lock()
	defer n.statsMu.Unlock()
	if !n.runtimeStatsQueued || n.pendingRuntimeStatsDev != dev {
		return wgdevice.RuntimeStats{}, false
	}
	stats := n.pendingRuntimeStats
	n.runtimeStatsQueued = false
	n.pendingRuntimeStatsDev = nil
	n.pendingRuntimeStats = wgdevice.RuntimeStats{}
	return stats, true
}
func (n *wireGuardNode) handleRuntimeStats(msg wireGuardRuntimeStatsMessage) {
	if msg.dev == nil {
		return
	}
	if !n.runtimeMu.TryLock() {
		n.deferInternal(msg)
		return
	}
	stats, ok := n.takeRuntimeStats(msg.dev)
	if !ok {
		stats = msg.stats
	}
	if msg.dev != n.dev {
		n.runtimeMu.Unlock()
		return
	}
	if n.runtimeStats.Equal(stats) {
		n.runtimeMu.Unlock()
		return
	}
	n.runtimeStats = stats
	n.runtimeMu.Unlock()
	n.sendStatusBlock()
}
func (n *wireGuardNode) refreshRuntimeStatsLocked() {
	if n.dev == nil {
		n.runtimeStats = wgdevice.RuntimeStats{}
		return
	}
	n.runtimeStats = n.dev.RuntimeStats()
}
