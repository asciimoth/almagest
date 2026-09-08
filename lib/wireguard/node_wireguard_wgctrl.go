package wireguardnodes

import (
	"errors"
	"slices"
	"strings"
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
// stale interface settings cannot remain active.
func (n *wireGuardNode) acceptWGCtrlInterfaceConfig(link uint64, cfg WGCtrlInterfaceConfig) error {
	if n.wgctrlConfigs == nil {
		n.wgctrlConfigs = map[uint64]WGCtrlInterfaceConfig{}
	}
	ownedIPs, err := normalizeWireGuardOwnedIPValues(cfg.OwnedIPs)
	routes := []string(nil)
	if err == nil {
		routes, err = normalizeWireGuardOwnedIPValues(cfg.Routes)
	}
	dns := strings.TrimSpace(cfg.DNS)
	if err == nil {
		err = validateWireGuardDNS(dns)
	}
	action := "replace"
	if err != nil {
		action = "reject"
		delete(n.wgctrlConfigs, link)
		delete(n.wgctrlConfigOrder, link)
		if n.Logger != nil {
			n.Logger.Errf("WGCtrl link %d supplied invalid interface config: %v", link, err)
		}
	} else if len(ownedIPs) == 0 && len(routes) == 0 && dns == "" {
		action = "withdraw"
		delete(n.wgctrlConfigs, link)
		delete(n.wgctrlConfigOrder, link)
	} else {
		if n.wgctrlConfigOrder == nil {
			n.wgctrlConfigOrder = map[uint64]uint64{}
		}
		n.wgctrlConfigSeq++
		n.wgctrlConfigOrder[link] = n.wgctrlConfigSeq
		n.wgctrlConfigs[link] = WGCtrlInterfaceConfig{OwnedIPs: ownedIPs, Routes: routes, DNS: dns}
	}
	if n.Logger != nil {
		n.Logger.Debugf(
			"WireGuard WGCtrl interface update: link=%d action=%s supplied_addresses=%v normalized_addresses=%v supplied_routes=%v normalized_routes=%v supplied_dns=%q normalized_dns=%q effective_addresses=%v effective_routes=%v effective_dns=%q",
			link, action, cfg.OwnedIPs, ownedIPs, cfg.Routes, routes, cfg.DNS, dns,
			n.effectiveOwnedIPAddrs(), n.effectiveTunRoutes(), n.effectiveDNS(),
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
	if n.W != nil && n.ID != 0 {
		snapshot, ok := n.W.LinkSnapshotLocked(link)
		if !ok || snapshot.Type != TypeWGCtrl || snapshot.LeftPortNode != n.ID {
			n.completeWGCtrlInterfaceConfigApply(apply, errors.New("WireGuard controller link is unavailable"))
			return
		}
	}
	if apply.err != nil {
		n.completeWGCtrlInterfaceConfigApply(apply, apply.err)
		return
	}
	if n.tunLink == 0 {
		n.completeWGCtrlInterfaceConfigApply(apply, errors.New("WireGuard TUN link is unavailable"))
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
	networkExpected := n.netLink != 0
	go func() {
		n.runtimeMu.Lock()
		ready := n.wgCtrlInterfaceConfigReadyLocked(generation, networkExpected)
		n.runtimeMu.Unlock()
		n.sendInternal(wireGuardWGCtrlInterfaceConfigCheckResult{
			apply: apply, generation: generation, ready: ready,
		})
	}()
}

func (n *wireGuardNode) wgCtrlInterfaceConfigReadyLocked(generation uint64, networkExpected bool) bool {
	// A later TUN generation also contains an earlier controller contribution,
	// unless that controller has explicitly superseded it. Requiring exact
	// equality can leave an apply pending forever when two controller updates
	// cause adjacent TUN replacements.
	return n.currentTunGeneration == n.currentTunGenerationValue() &&
		n.currentTunGeneration >= generation &&
		n.currentTun != nil && TunIsReady(n.currentTun) &&
		// A desired network with no healthy current bind is a failed or
		// incomplete transport, not the same condition as an intentionally
		// absent network.
		(!networkExpected || n.runtimeDeviceUp && n.hasActiveTransportLocked())
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
		if generation == 0 || expected <= generation {
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

// effectiveTunRoutes merges peer routes owned by the WireGuard node with
// runtime route suggestions from controllers. Controller routes use stable
// link-ID order. Exact normalized duplicates keep their first occurrence.
func (n *wireGuardNode) effectiveTunRoutes() []string {
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
	appendValues(wireGuardPeerRoutes(n.effectiveConfig().peers))

	links := make([]uint64, 0, len(n.wgctrlConfigs))
	for link := range n.wgctrlConfigs {
		links = append(links, link)
	}
	slices.Sort(links)
	for _, link := range links {
		appendValues(n.wgctrlConfigs[link].Routes)
	}
	return out
}

// effectiveDNS returns the menu value when it is set. Otherwise, it returns
// the most recently received DNS suggestion that still belongs to an attached
// controller link.
func (n *wireGuardNode) effectiveDNS() string {
	if dns := strings.TrimSpace(n.config.dns); dns != "" {
		return dns
	}
	var (
		latestDNS   string
		latestOrder uint64
		latestLink  uint64
	)
	for link, cfg := range n.wgctrlConfigs {
		dns := strings.TrimSpace(cfg.DNS)
		order := n.wgctrlConfigOrder[link]
		if dns != "" && (order > latestOrder || order == latestOrder && link > latestLink) {
			latestDNS = dns
			latestOrder = order
			latestLink = link
		}
	}
	return latestDNS
}
