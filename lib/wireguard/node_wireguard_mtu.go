package wireguardnodes

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/asciimoth/almagest/lib/links"
	wgdevice "github.com/asciimoth/wgo/device"
)

type wireGuardMTUResult struct {
	calculationGeneration uint64
	networkGeneration     uint64
	providerRevision      uint64
	maxUDPPayload         int
	constraint            links.MTUConstraint
	err                   error
}

type wireGuardMTUChanged struct {
	networkGeneration uint64
	revision          uint64
}

func (n *wireGuardNode) setCarrierMTUProvider(provider links.DatagramMTUProvider, revision uint64) {
	n.unsubscribeCarrierMTU()
	n.desiredDatagramMTU = provider
	if provider == nil {
		n.desiredMTURevision = 0
		return
	}
	if revision == 0 {
		revision = provider.Revision()
	}
	n.desiredMTURevision = revision
	networkGeneration := n.currentNetworkGenerationValue()
	n.unsubscribeDatagramMTU = provider.SubscribeDatagramMTU(func(next uint64) {
		n.sendInternal(wireGuardMTUChanged{networkGeneration: networkGeneration, revision: next})
	})
}

func (n *wireGuardNode) unsubscribeCarrierMTU() {
	if n.unsubscribeDatagramMTU != nil {
		n.unsubscribeDatagramMTU()
		n.unsubscribeDatagramMTU = nil
	}
	n.desiredDatagramMTU = nil
	n.desiredMTURevision = 0
}

func (n *wireGuardNode) scheduleCarrierMTUCalculation() {
	provider := n.desiredDatagramMTU
	networkGeneration := n.currentNetworkGenerationValue()
	providerRevision := uint64(0)
	if provider != nil {
		providerRevision = provider.Revision()
	}
	calculationGeneration := n.mtuCalculation.Add(1)
	n.mtuPending = true
	_ = n.updateLabel()
	n.sendStatusBlock()
	config := n.effectiveConfig()
	hasIPv6 := n.effectiveInterfaceHasIPv6(config)
	go func() {
		maxPayload, constraint, err := calculateWireGuardCarrierMTU(
			context.Background(), provider, config, n.ID, n.nodeName, hasIPv6,
		)
		n.sendInternal(wireGuardMTUResult{
			calculationGeneration: calculationGeneration,
			networkGeneration:     networkGeneration,
			providerRevision:      providerRevision,
			maxUDPPayload:         maxPayload,
			constraint:            constraint,
			err:                   err,
		})
	}()
}

func (n *wireGuardNode) handleCarrierMTUChanged(msg wireGuardMTUChanged) {
	if msg.networkGeneration != n.currentNetworkGenerationValue() || n.desiredDatagramMTU == nil {
		return
	}
	currentRevision := n.desiredDatagramMTU.Revision()
	if msg.revision < currentRevision || currentRevision == n.desiredMTURevision && !n.mtuPending {
		return
	}
	n.desiredMTURevision = currentRevision
	n.scheduleCarrierMTUCalculation()
}

func (n *wireGuardNode) handleCarrierMTUResult(msg wireGuardMTUResult) {
	if msg.calculationGeneration != n.mtuCalculation.Load() || msg.networkGeneration != n.currentNetworkGenerationValue() {
		return
	}
	if n.desiredDatagramMTU != nil && msg.providerRevision != n.desiredDatagramMTU.Revision() {
		n.scheduleCarrierMTUCalculation()
		return
	}
	n.mtuPending = false
	n.carrierMaxUDPPayload = msg.maxUDPPayload
	n.carrierMTUConstraint = msg.constraint.Clone()
	n.carrierMTUError = ""
	if msg.err != nil {
		n.carrierMTUError = msg.err.Error()
		if n.Logger != nil {
			n.Logger.Debugf("WireGuard carrier MTU calculation: %v", msg.err)
		}
	}
	n.sendTunConfig()
	n.checkPendingWGCtrlInterfaceConfigApplies()
	if n.Logger != nil {
		selected := n.tunConfig()
		n.Logger.Debugf(
			"WireGuard MTU selected: effective=%d controller_ceiling=%d carrier_udp_payload=%d carrier_cap=%d source=%q reason=%q input=%d overhead=%d path=%v",
			selected.MTU, n.effectiveControllerMTU(), n.carrierMaxUDPPayload,
			n.carrierMTUConstraint.Value, n.carrierMTUConstraint.Source,
			n.carrierMTUConstraint.Reason, n.carrierMTUConstraint.InputValue,
			n.carrierMTUConstraint.Overhead, n.carrierMTUConstraint.Path,
		)
	}
	if msg.err != nil && isMTUIncompatibility(msg.err) {
		n.setRuntimeError(msg.err)
		_ = n.updateLabel()
		n.sendStatusBlock()
		return
	}
	if n.networkPayloadReceived && n.desiredNetwork != nil {
		networkGeneration := n.currentNetworkGenerationValue()
		n.scheduleNetworkAttach(networkGeneration, n.desiredNetwork, n.config.batchSize)
	}
}

func isMTUIncompatibility(err error) bool {
	var incompatibility *mtuIncompatibilityError
	return errors.As(err, &incompatibility)
}

type mtuIncompatibilityError struct {
	mtu int
}

func (e *mtuIncompatibilityError) Error() string {
	return fmt.Sprintf("calculated WireGuard MTU %d is below the IPv6 minimum link MTU %d", e.mtu, links.IPv6MinimumLinkMTU)
}

func calculateWireGuardCarrierMTU(
	ctx context.Context,
	provider links.DatagramMTUProvider,
	config wireGuardConfig,
	node uint64,
	nodeName string,
	hasIPv6 bool,
) (int, links.MTUConstraint, error) {
	if provider == nil {
		return 0, links.MTUConstraint{}, errors.New("carrier maximum UDP payload is unknown; using other MTU constraints or VTun fallback")
	}
	var (
		minimum         links.MTUConstraint
		minimumPayload  int
		queryErrs       []error
		applicablePeers int
	)
	for index, peer := range config.peers {
		endpoint := strings.TrimSpace(peer.endpoint)
		if endpoint == "" {
			continue
		}
		applicablePeers++
		result, err := provider.MaxUDPPayload(ctx, "udp", endpoint)
		if err != nil {
			queryErrs = append(queryErrs, fmt.Errorf("peer %d endpoint %q: %w", index+1, endpoint, err))
			continue
		}
		if !result.Known || result.MaxPayload <= 0 {
			continue
		}
		s4 := wireGuardPeerTransportPadding(config, peer)
		amneziaConfig, amneziaEnabled := wireGuardPeerAmneziaConfig(config, peer)
		if err := validateWireGuardStandaloneMessages(result.MaxPayload, amneziaConfig, amneziaEnabled); err != nil {
			return 0, links.MTUConstraint{}, fmt.Errorf("peer %d endpoint %q: %w", index+1, endpoint, err)
		}
		safe := links.WireGuardInnerMTU(result.MaxPayload, s4)
		if safe <= 0 {
			return 0, links.MTUConstraint{}, fmt.Errorf("peer %d endpoint %q: carrier UDP payload %d cannot hold WireGuard overhead %d", index+1, endpoint, result.MaxPayload, links.WireGuardDataOverhead+s4)
		}
		constraint := result.Constraint.Clone()
		constraint.Value = safe
		constraint.SourceNode = node
		constraint.Source = nodeName
		constraint.Reason = "WireGuard carrier cap"
		constraint.InputValue = result.MaxPayload
		constraint.Overhead = links.WireGuardDataOverhead + s4
		constraint.Result = safe
		constraint, err = constraint.WithNode(node)
		if err != nil {
			return 0, links.MTUConstraint{}, err
		}
		if minimum.Value == 0 || safe < minimum.Value {
			minimum = constraint
			minimumPayload = result.MaxPayload
		}
	}
	if minimum.Value == 0 {
		if applicablePeers == 0 {
			return 0, links.MTUConstraint{}, errors.New("carrier maximum UDP payload is unknown because no default-transport peer endpoint is configured")
		}
		if err := errors.Join(queryErrs...); err != nil {
			return 0, links.MTUConstraint{}, fmt.Errorf("carrier maximum UDP payload is unknown: %w", err)
		}
		return 0, links.MTUConstraint{}, errors.New("carrier maximum UDP payload is unknown for all peer endpoints")
	}
	if hasIPv6 && minimum.Value < links.IPv6MinimumLinkMTU {
		return minimumPayload, minimum, &mtuIncompatibilityError{mtu: minimum.Value}
	}
	return minimumPayload, minimum, errors.Join(queryErrs...)
}

func wireGuardPeerAmneziaConfig(config wireGuardConfig, peer wireGuardPeerConfig) (wireGuardAmneziaConfig, bool) {
	if peer.amneziaWGOverride {
		return peer.amneziaWG, peer.amneziaWGEnabled
	}
	return config.amneziaWG, config.amneziaWGEnabled
}

func wireGuardPeerTransportPadding(config wireGuardConfig, peer wireGuardPeerConfig) int {
	awg, enabled := wireGuardPeerAmneziaConfig(config, peer)
	if !enabled {
		return 0
	}
	return awg.transportPadding
}

func (n *wireGuardNode) effectiveInterfaceHasIPv6(config wireGuardConfig) bool {
	for _, value := range n.effectiveOwnedIPAddrs() {
		if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Addr().Is6() {
			return true
		}
	}
	for _, peer := range config.peers {
		prefixes, _ := parseWireGuardAllowedIPs(peer.allowedIPs)
		for _, prefix := range prefixes {
			if prefix.Addr().Is6() {
				return true
			}
		}
	}
	return false
}

func validateWireGuardStandaloneMessages(limit int, config wireGuardAmneziaConfig, enabled bool) error {
	if limit <= 0 {
		return nil
	}
	if !enabled {
		config = wireGuardAmneziaConfig{}
	}
	messages := []struct {
		name string
		size int
	}{
		{name: "handshake initiation", size: wgdevice.MessageInitiationSize + config.initPadding},
		{name: "handshake response", size: wgdevice.MessageResponseSize + config.responsePadding},
		{name: "cookie reply", size: wgdevice.MessageCookieReplySize + config.cookiePadding},
	}
	if config.junkCount > 0 {
		messages = append(messages, struct {
			name string
			size int
		}{name: "junk packet", size: config.junkMax})
	}
	for index, spec := range config.initiationPackets {
		size, err := links.AmneziaInitiationPacketSize(spec)
		if err != nil {
			return fmt.Errorf("custom initiation packet I%d: %w", index+1, err)
		}
		if size > 0 {
			messages = append(messages, struct {
				name string
				size int
			}{name: "custom initiation packet I" + strconv.Itoa(index+1), size: size})
		}
	}
	for _, message := range messages {
		if message.size > limit {
			return fmt.Errorf("%s size %d exceeds carrier UDP payload %d", message.name, message.size, limit)
		}
	}
	return nil
}
