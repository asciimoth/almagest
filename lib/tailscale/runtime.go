package tailscalenodes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgotailscale "github.com/asciimoth/wgo-tailscale"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

// scheduleReconcile invalidates the current presentation before it starts a
// worker. This prevents an old client event or WireGuard apply reply from
// making a changed graph look ready.
func (n *tailscaleNode) scheduleReconcile() {
	generation := n.generation.Add(1)
	n.cancelCurrentRun()
	ctx, cancel := context.WithCancel(n.Context())
	n.cancelMu.Lock()
	n.cancelRun = cancel
	n.cancelMu.Unlock()

	n.acceptRuntimeEvents = false
	n.pending = true
	n.activeRuntimeID = 0
	n.activeRuntimeDone = nil
	n.snapshot = wgotailscale.Snapshot{}
	n.runtimeError = ""
	n.withdrawWGConfig(true)
	n.clearRuntimeOutputs()
	n.updateInteraction(nil)

	req := tailscaleRuntimeRequest{
		generation: generation,
		config:     n.config,
		controller: n.controller,
		network:    n.network,
		tlsConfig:  nodekit.CloneTLSClientConfig(n.tlsConfig),
		tlsErr:     n.tlsConfigErr,
		nodeName:   n.currentNodeName(),
		ctx:        ctx,
	}
	if err := n.SpawnWorker("Tailscale runtime reconcile", func() {
		result, send := n.reconcile(req)
		if send && n.W != nil {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: result})
		}
	}); err != nil {
		cancel()
		n.pending = false
		n.runtimeError = "start runtime worker: " + err.Error()
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *tailscaleNode) cancelCurrentRun() {
	n.cancelMu.Lock()
	cancel := n.cancelRun
	n.cancelRun = nil
	n.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// reconcile is the only path that creates, starts, or closes a Tailscale
// client. It also owns detached WireGuard API replacement and closure.
func (n *tailscaleNode) reconcile(req tailscaleRuntimeRequest) (tailscaleRuntimeResult, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if req.generation != n.generation.Load() {
		return tailscaleRuntimeResult{}, false
	}

	result := tailscaleRuntimeResult{generation: req.generation, done: req.ctx.Done()}
	controllerReady := wireguardnodes.WGCtrlIsReady(req.controller)
	networkReady := nodekit.NetworkIsReady(req.network)
	if !req.config.enabled || !controllerReady || !networkReady || req.tlsErr != "" {
		n.closeRuntimeLocked(req.controller)
		if req.tlsErr != "" && req.config.enabled && controllerReady && networkReady {
			result.err = errors.New("TLS config: " + req.tlsErr)
		}
		return result, true
	}

	authKey, err := n.readAuthKey(req.config, req.nodeName)
	if err != nil {
		n.closeRuntimeLocked(req.controller)
		result.err = err
		return result, true
	}
	hostname := req.config.hostname
	if hostname == "" {
		hostname = strings.TrimSpace(req.nodeName)
	}
	if hostname == "" {
		n.closeRuntimeLocked(req.controller)
		result.err = errors.New("hostname is empty")
		return result, true
	}

	n.closeRuntimeLocked(req.controller)
	if req.generation != n.generation.Load() || req.ctx.Err() != nil {
		return tailscaleRuntimeResult{}, false
	}

	options := wgotailscale.Options{
		ControlURL:                        req.config.controlURL,
		Hostname:                          hostname,
		AuthKey:                           authKey,
		Ephemeral:                         req.config.ephemeral,
		ConfirmPeers:                      req.config.confirmPeers,
		TransportID:                       tailscaleTransportID(req.config.storageID),
		UseDefaultTransportForDirectPeers: true,
		ListenPort:                        uint16(req.config.listenPort),
		DisableDERP:                       req.config.disableDERP,
		DisableDiscovery:                  req.config.disableDiscovery,
		TLSConfig:                         req.tlsConfig,
		Cache:                             n.cache.callbacks(),
	}
	options = applyTailscaleE2EOptions(options)
	client, err := n.newClient(newTailscaleNetwork(req.network), req.controller, options)
	if err != nil {
		result.err = fmt.Errorf("create Tailscale client: %w", err)
		return result, true
	}
	events, unsubscribe := client.Subscribe(1)
	if err := client.Start(req.ctx); err != nil {
		unsubscribe()
		_ = client.Close()
		result.err = fmt.Errorf("start Tailscale client: %w", err)
		return result, true
	}
	if req.generation != n.generation.Load() || req.ctx.Err() != nil {
		unsubscribe()
		_ = client.Close()
		return tailscaleRuntimeResult{}, false
	}

	n.nextRuntimeID++
	runtimeID := n.nextRuntimeID
	n.runtimeClient = client
	n.runtimeController = req.controller
	n.runtimeID = runtimeID
	n.runtimeUnsubscribe = unsubscribe
	activate := make(chan struct{})
	if err := n.spawnEventWatcher(runtimeID, client, events, activate, req.ctx.Done()); err != nil {
		n.closeRuntimeLocked(req.controller)
		result.err = fmt.Errorf("start Tailscale event watcher: %w", err)
		return result, true
	}
	result.runtimeID = runtimeID
	result.snapshot = client.Snapshot()
	result.activate = activate
	return result, true
}

func (n *tailscaleNode) spawnEventWatcher(
	runtimeID uint64,
	client Client,
	events <-chan wgotailscale.Event,
	activate, done <-chan struct{},
) error {
	return n.SpawnWorker("Tailscale event watcher", func() {
		watchTailscaleRuntimeEvents(client, events, activate, done, func(snapshot wgotailscale.Snapshot) {
			if n.W == nil {
				return
			}
			n.W.SendInbox(pasta.InboxMessage{
				ReceiverNode: n.ID,
				Payload: tailscaleRuntimeEvent{
					runtimeID: runtimeID,
					snapshot:  snapshot,
				},
			})
		})
	})
}

// watchTailscaleRuntimeEvents does not consume coalesced client events until
// the workspace accepts the matching runtime result. This preserves an event
// that arrives between the initial snapshot and its inbox delivery.
func watchTailscaleRuntimeEvents(
	client Client,
	events <-chan wgotailscale.Event,
	activate, done <-chan struct{},
	send func(wgotailscale.Snapshot),
) {
	select {
	case <-activate:
	case <-done:
		return
	}
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
			send(client.Snapshot())
		case <-done:
			return
		}
	}
}

// closeRuntimeLocked closes client resources before it closes a replaced
// detached controller. The caller must hold runtimeMu. A controller that is
// still the desired input remains usable for a later rebuild.
func (n *tailscaleNode) closeRuntimeLocked(nextController wireguardnodes.WGCtrlAPI) {
	unsubscribe := n.runtimeUnsubscribe
	client := n.runtimeClient
	controller := n.runtimeController
	n.runtimeUnsubscribe = nil
	n.runtimeClient = nil
	n.runtimeController = nil
	n.runtimeID = 0
	if unsubscribe != nil {
		unsubscribe()
	}
	if client != nil {
		_ = client.Close()
	}
	if !wireguardnodes.SameWGCtrlInstance(controller, nextController) {
		wireguardnodes.CloseWGCtrl(controller)
	}
}

func (n *tailscaleNode) stopRuntime() {
	n.runtimeMu.Lock()
	n.closeRuntimeLocked(nil)
	n.runtimeMu.Unlock()
}

func (n *tailscaleNode) handleRuntimeResult(result tailscaleRuntimeResult) {
	if result.generation != n.generation.Load() {
		return
	}
	n.pending = false
	n.activeRuntimeID = result.runtimeID
	n.activeRuntimeDone = result.done
	n.acceptRuntimeEvents = result.runtimeID != 0 && result.err == nil
	n.runtimeError = ""
	if result.err != nil {
		n.runtimeError = result.err.Error()
		n.clearRuntimeOutputs()
		n.updateInteraction(nil)
	} else {
		n.applySnapshot(result.snapshot)
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot(false)
	if result.activate != nil {
		close(result.activate)
	}
}

func (n *tailscaleNode) handleRuntimeEvent(event tailscaleRuntimeEvent) {
	if !n.acceptRuntimeEvents || event.runtimeID == 0 || event.runtimeID != n.activeRuntimeID {
		return
	}
	previousInteraction := interactionIdentity(n.snapshot.Interaction)
	n.applySnapshot(event.snapshot)
	_ = n.updateLabel()
	if interactionIdentity(n.snapshot.Interaction) != previousInteraction {
		n.sendMenuSnapshot(false)
	} else {
		n.sendStatusBlock()
		n.sendPeerConfirmationBlock()
	}
}

func interactionIdentity(interaction *wgotailscale.Interaction) string {
	if interaction == nil {
		return ""
	}
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", interaction.ID, interaction.Kind, interaction.URL, interaction.Message)
}

func (n *tailscaleNode) applySnapshot(snapshot wgotailscale.Snapshot) {
	n.snapshot = snapshot
	n.runtimeError = snapshot.LastError
	n.dnsResolver.SetView(snapshot.DNS)
	n.setFirewallObject(firewallObject(snapshot.ACL.FirewallConfig()))
	n.updateInteraction(snapshot.Interaction)
	n.applyWGConfigForSnapshot(snapshot)
}

func (n *tailscaleNode) applyWGConfigForSnapshot(snapshot wgotailscale.Snapshot) {
	if snapshot.State != wgotailscale.StateRunning || !snapshot.Network.Up || n.ctrlLink == 0 {
		n.withdrawWGConfig(false)
		return
	}
	desired := tailscaleWGInterfaceConfig(snapshot)
	if n.wgAppliedID == n.activeRuntimeID && sameWGConfig(n.wgAppliedConfig, desired) && (n.wgConfigured || n.wgApplyPending) {
		return
	}

	n.wgApplySequence++
	sequence := n.wgApplySequence
	runtimeID := n.activeRuntimeID
	runtimeDone := n.activeRuntimeDone
	result := make(chan error, 1)
	n.wgApplyPending = true
	n.wgConfigured = false
	n.wgAppliedID = runtimeID
	n.wgAppliedConfig = desired.Clone()
	if !wireguardnodes.EmitWGCtrlInterfaceConfigApply(n.W, n.ID, n.ctrlLink, desired, result) {
		n.wgApplyPending = false
		n.runtimeError = "apply WireGuard interface config: controller link is unavailable"
		return
	}
	if err := n.SpawnWorker("Tailscale WireGuard interface apply", func() {
		var err error
		select {
		case err = <-result:
		case <-runtimeDone:
			return
		}
		if n.W != nil {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: tailscaleWGApplyResult{
				runtimeID: runtimeID,
				sequence:  sequence,
				config:    desired,
				err:       err,
			}})
		}
	}); err != nil {
		n.wgApplyPending = false
		n.runtimeError = "wait for WireGuard interface config: " + err.Error()
	}
}

func (n *tailscaleNode) handleWGApplyResult(result tailscaleWGApplyResult) {
	if result.runtimeID == 0 || result.runtimeID != n.activeRuntimeID || result.sequence != n.wgApplySequence {
		return
	}
	n.wgApplyPending = false
	n.wgConfigured = result.err == nil
	if result.err != nil {
		n.runtimeError = "apply WireGuard interface config: " + result.err.Error()
		n.wgAppliedID = 0
		n.wgAppliedConfig = wireguardnodes.WGCtrlInterfaceConfig{}
	} else {
		n.wgAppliedID = result.runtimeID
		n.wgAppliedConfig = result.config.Clone()
		if n.snapshot.LastError == "" {
			n.runtimeError = ""
		}
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *tailscaleNode) withdrawWGConfig(force bool) {
	hadConfig := n.wgApplyPending || n.wgConfigured || n.wgAppliedID != 0 || !n.wgAppliedConfig.IsZero()
	n.wgApplySequence++
	n.wgApplyPending = false
	n.wgConfigured = false
	n.wgAppliedID = 0
	n.wgAppliedConfig = wireguardnodes.WGCtrlInterfaceConfig{}
	if n.ctrlLink != 0 && (force || hadConfig) {
		wireguardnodes.EmitWGCtrlInterfaceConfig(n.W, n.ID, n.ctrlLink, wireguardnodes.WGCtrlInterfaceConfig{})
	}
}

func (n *tailscaleNode) clearRuntimeOutputs() {
	if n.dnsResolver != nil {
		n.dnsResolver.SetView(wgotailscale.DNSView{})
	}
	n.setFirewallObject(emptyFirewallObject())
}

func (n *tailscaleNode) setFirewallObject(object std.Object) {
	if n.firewall.Equal(object) {
		return
	}
	n.firewall = object
	n.sendFirewallAll()
}

func emptyFirewallObject() std.Object {
	object, err := std.ObjectFromJSON([]byte("{}"))
	if err != nil {
		return std.NilObject()
	}
	return object
}

func firewallObject(config *gonnect.FirewallConfig) std.Object {
	object, err := std.ObjectFromJSON([]byte(nodekit.FormatFirewallConfigJSON(config)))
	if err != nil {
		return emptyFirewallObject()
	}
	return object
}

func sameWGConfig(a, b wireguardnodes.WGCtrlInterfaceConfig) bool {
	return a.DNS == b.DNS && a.MTU == b.MTU && slices.Equal(a.OwnedIPs, b.OwnedIPs) && slices.Equal(a.Routes, b.Routes)
}

func tailscaleWGInterfaceConfig(snapshot wgotailscale.Snapshot) wireguardnodes.WGCtrlInterfaceConfig {
	addresses := make([]string, 0, len(snapshot.Network.Addresses))
	for _, address := range snapshot.Network.Addresses {
		if address.IsValid() {
			addresses = append(addresses, address.String())
		}
	}
	slices.Sort(addresses)
	addresses = slices.Compact(addresses)

	routes := make([]string, 0, len(snapshot.Network.Routes))
	for _, route := range snapshot.Network.Routes {
		if route.Prefix.IsValid() {
			routes = append(routes, route.Prefix.String())
		}
	}
	slices.Sort(routes)
	routes = slices.Compact(routes)
	return wireguardnodes.WGCtrlInterfaceConfig{OwnedIPs: addresses, Routes: routes, MTU: snapshot.Network.MTU}
}

func tailscaleTransportID(storageID string) wgdevice.TransportID {
	if len(storageID) > 16 {
		storageID = storageID[:16]
	}
	return wgdevice.TransportID("tailscale-" + storageID)
}
