package wireguardnodes

import (
	"errors"
	"slices"
)

type wireGuardWGCtrlInterfaceConfigCheckResult struct {
	apply      *wgCtrlInterfaceConfigApply
	generation uint64
	ready      bool
}

// sendWGCtrl emits a live detached controller API on every WG control link.
// It runs only from workspace callbacks, so wgctrlPayloads needs no mutex.
func (n *wireGuardNode) sendWGCtrl() {
	if n.W == nil || n.ctrlp == 0 {
		return
	}
	n.ForEachPortLink(n.ctrlp, n.sendWGCtrlToLink)
}

// sendWGCtrlToLink preserves a live per-link API. A closed cached API is
// replaced, which lets closable middleware request a fresh controller after it
// changes its active path.
func (n *wireGuardNode) sendWGCtrlToLink(link uint64) {
	if payload, ok := n.wgctrlPayloads[link]; ok && WGCtrlIsReady(payload.API) {
		_ = EmitWGCtrlPayload(n.W, n.ID, link, payload)
		return
	}
	delete(n.wgctrlPayloads, link)
	source := n.controllerSource.Load()
	if !WGCtrlIsReady(source) {
		return
	}
	payload := EmitDetachedWGCtrl(n.W, n.ID, link, source)
	if WGCtrlIsReady(payload.API) {
		n.wgctrlPayloads[link] = payload
	}
}

// refreshWGCtrlToLink emits a new detached API in response to RequestValue.
// Closable middleware closes its old routed payload asynchronously and sends a
// request immediately. Re-emitting the cached payload in that window would
// give the new path an API that is about to close.
//
// The old API is not closed in this workspace callback. Its consumer or the
// middleware owns that operation, and the graph resource remains an idempotent
// fallback for link or node teardown.
func (n *wireGuardNode) refreshWGCtrlToLink(link uint64) {
	source := n.controllerSource.Load()
	if !WGCtrlIsReady(source) {
		return
	}
	payload := EmitDetachedWGCtrl(n.W, n.ID, link, source)
	if WGCtrlIsReady(payload.API) {
		n.wgctrlPayloads[link] = payload
	}
}

// acceptWGCtrlInterfaceConfig replaces the suggestion owned by one controller
// link. Invalid latest input removes that link's previous valid suggestion so
// stale addresses cannot remain active.
func (n *wireGuardNode) acceptWGCtrlInterfaceConfig(link uint64, cfg WGCtrlInterfaceConfig) error {
	ownedIPs, err := normalizeWireGuardOwnedIPValues(cfg.OwnedIPs)
	action := "replace"
	if err != nil {
		action = "reject"
		delete(n.wgctrlConfigs, link)
		if n.Logger != nil {
			n.Logger.Errf("WGCtrl link %d supplied invalid owned IPs: %v", link, err)
		}
	} else if len(ownedIPs) == 0 {
		action = "withdraw"
		delete(n.wgctrlConfigs, link)
	} else {
		n.wgctrlConfigs[link] = WGCtrlInterfaceConfig{OwnedIPs: ownedIPs}
	}
	if n.Logger != nil {
		n.Logger.Debugf(
			"WireGuard WGCtrl interface address update: link=%d action=%s supplied_addresses=%v normalized_addresses=%v effective_addresses=%v",
			link, action, cfg.OwnedIPs, ownedIPs, n.effectiveOwnedIPAddrs(),
		)
	}
	n.sendTunConfig()
	n.sendStatusBlock()
	return err
}

// commitWGCtrlInterfaceConfigApply runs from the low-priority commit event.
// By this point, regular TunConfig propagation has completed and any
// replacement TUN payload has advanced tunGeneration.
func (n *wireGuardNode) commitWGCtrlInterfaceConfigApply(link uint64, apply *wgCtrlInterfaceConfigApply) {
	if apply == nil {
		return
	}
	if apply.link != link {
		n.completeWGCtrlInterfaceConfigApply(apply, errors.New("WireGuard interface config apply was not accepted"))
		return
	}
	if apply.err != nil || n.tunLink == 0 {
		n.completeWGCtrlInterfaceConfigApply(apply, apply.err)
		return
	}
	if n.wgctrlPendingApplies == nil {
		n.wgctrlPendingApplies = map[*wgCtrlInterfaceConfigApply]uint64{}
	}
	generation := n.currentTunGenerationValue()
	n.wgctrlPendingApplies[apply] = generation
	n.scheduleWGCtrlInterfaceConfigCheck(apply, generation)
}

func (n *wireGuardNode) scheduleWGCtrlInterfaceConfigCheck(apply *wgCtrlInterfaceConfigApply, generation uint64) {
	go func() {
		n.runtimeMu.Lock()
		ready := generation == n.currentTunGenerationValue() &&
			n.currentTunGeneration == generation &&
			n.currentTun != nil && TunIsReady(n.currentTun) &&
			(n.runtimeDeviceUp || !n.hasActiveTransportLocked())
		n.runtimeMu.Unlock()
		n.sendInternal(wireGuardWGCtrlInterfaceConfigCheckResult{
			apply: apply, generation: generation, ready: ready,
		})
	}()
}

func (n *wireGuardNode) handleWGCtrlInterfaceConfigCheckResult(msg wireGuardWGCtrlInterfaceConfigCheckResult) {
	generation, ok := n.wgctrlPendingApplies[msg.apply]
	if !ok || generation != msg.generation || !msg.ready {
		return
	}
	n.completeWGCtrlInterfaceConfigApply(msg.apply, nil)
}

func (n *wireGuardNode) checkPendingWGCtrlInterfaceConfigApplies() {
	for apply, generation := range n.wgctrlPendingApplies {
		n.scheduleWGCtrlInterfaceConfigCheck(apply, generation)
	}
}

func (n *wireGuardNode) failWGCtrlInterfaceConfigApplies(link uint64, err error) {
	for apply := range n.wgctrlPendingApplies {
		if link == 0 || apply.link == link {
			n.completeWGCtrlInterfaceConfigApply(apply, err)
		}
	}
}

func (n *wireGuardNode) failWGCtrlInterfaceConfigAppliesForTunGeneration(generation uint64, err error) {
	for apply, expected := range n.wgctrlPendingApplies {
		if generation == 0 || expected == generation {
			n.completeWGCtrlInterfaceConfigApply(apply, err)
		}
	}
}

func (n *wireGuardNode) completeWGCtrlInterfaceConfigApply(apply *wgCtrlInterfaceConfigApply, err error) {
	if apply == nil {
		return
	}
	delete(n.wgctrlPendingApplies, apply)
	select {
	case apply.result <- err:
	default:
	}
}

// effectiveOwnedIPAddrs merges menu addresses first, followed by controller
// contributions in stable link-ID order. Exact normalized duplicates are
// removed while preserving the first occurrence.
func (n *wireGuardNode) effectiveOwnedIPAddrs() []string {
	seen := map[string]struct{}{}
	var out []string
	appendValues := func(values []string) {
		for _, value := range values {
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	appendValues(wireGuardOwnedIPAddrs(n.config.ownedIPs))

	links := make([]uint64, 0, len(n.wgctrlConfigs))
	for link := range n.wgctrlConfigs {
		links = append(links, link)
	}
	slices.Sort(links)
	for _, link := range links {
		appendValues(n.wgctrlConfigs[link].OwnedIPs)
	}
	return out
}
