package amnesianodes

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"syscall"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/links"
)

type managedAmnesiaLearningBind struct {
	conn.Bind
	node       *managedAmnesiaNode
	generation uint64
	learner    links.DatagramMTULearner
}

func (b managedAmnesiaLearningBind) Send(bufs [][]byte, endpoint conn.Endpoint) error {
	err := b.Bind.Send(bufs, endpoint)
	if err == nil || !errors.Is(err, syscall.EMSGSIZE) || endpoint == nil {
		return err
	}
	attemptedSize := 0
	for _, packet := range bufs {
		if len(packet) > attemptedSize {
			attemptedSize = len(packet)
		}
	}
	if attemptedSize == 0 || b.node == nil || b.generation != b.node.generation.Load() {
		return err
	}
	if b.learner != nil {
		b.learner.LearnDatagramMTU("udp", endpoint.DstToString(), endpoint.DstIP().Is6(), attemptedSize)
	}
	if b.node.Logger != nil {
		b.node.Logger.Warnf(
			"Amnesia UDP datagram is too large: endpoint=%q ipv6=%t attempted=%d generation=%d error=%v",
			endpoint.DstToString(), endpoint.DstIP().Is6(), attemptedSize, b.generation, err,
		)
	}
	return err
}

type managedAmnesiaMTUChanged struct {
	generation uint64
	revision   uint64
}

func (n *managedAmnesiaNode) setManagedNetworkMTU(provider links.DatagramMTUProvider, revision uint64) {
	n.unsubscribeManagedNetworkMTU()
	if provider == nil {
		n.networkMTURevision = 0
		return
	}
	if revision == 0 {
		revision = provider.Revision()
	}
	n.networkMTURevision = revision
	n.networkMTUGeneration++
	generation := n.networkMTUGeneration
	n.unsubscribeNetworkMTU = provider.SubscribeDatagramMTU(func(next uint64) {
		// A provider can change during a graph callback while Pasta holds the
		// workspace lock. Cross the asynchronous inbox boundary without trying
		// to acquire that lock from the provider callback.
		go func() {
			if n.W != nil && n.ID != 0 {
				n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: managedAmnesiaMTUChanged{
					generation: generation,
					revision:   next,
				}})
			}
		}()
	})
}

func (n *managedAmnesiaNode) unsubscribeManagedNetworkMTU() {
	if n.unsubscribeNetworkMTU != nil {
		n.unsubscribeNetworkMTU()
		n.unsubscribeNetworkMTU = nil
	}
	n.networkMTURevision = 0
	n.networkMTUGeneration++
}

func (n *managedAmnesiaNode) handleManagedNetworkMTUChanged(msg managedAmnesiaMTUChanged) {
	if msg.generation != n.networkMTUGeneration || n.networkPayload.DatagramMTU == nil {
		return
	}
	revision := n.networkPayload.DatagramMTU.Revision()
	if msg.revision < revision || revision == n.networkMTURevision {
		return
	}
	n.networkMTURevision = revision
	n.networkPayload.CapabilityRevision = revision
	n.scheduleReconcile(true)
}

func calculateAmnesiaProfileMTU(
	ctx context.Context,
	network gonnect.Network,
	profile *wgoamnesia.Profile,
	nodeID uint64,
	requestedCountry string,
) (amnesiaProfileStatus, string, error) {
	status := profileStatus(profile, requestedCountry)
	if profile == nil {
		return status, "", fmt.Errorf("calculate Amnezia MTU: profile is nil")
	}
	provider := links.DatagramMTUProviderFromNetwork(network)
	if provider == nil {
		return status, "carrier network does not provide a UDP datagram limit", nil
	}
	endpoint := strings.TrimSpace(profile.Peer.Endpoint)
	if endpoint == "" {
		return status, "profile endpoint is empty; carrier UDP datagram limit is unknown", nil
	}
	carrier, err := provider.MaxUDPPayload(ctx, "udp", endpoint)
	if err != nil {
		return status, fmt.Sprintf("carrier UDP datagram lookup failed: %v", err), nil
	}
	if !carrier.Known || carrier.MaxPayload <= 0 {
		return status, "carrier UDP datagram limit is unknown", nil
	}
	if err := validateAmnesiaProfileDatagrams(carrier.MaxPayload, profile); err != nil {
		return status, "", err
	}
	carrierConstraint, err := carrier.Constraint.WithNode(nodeID)
	if err != nil {
		return status, "", err
	}
	overhead := links.WireGuardDataOverhead + int(profile.AmneziaWG.TransportPadding)
	calculated := links.WireGuardInnerMTU(carrier.MaxPayload, int(profile.AmneziaWG.TransportPadding))
	if calculated <= 0 {
		return status, "", fmt.Errorf(
			"amnezia carrier UDP payload %d cannot contain WireGuard overhead %d",
			carrier.MaxPayload, overhead,
		)
	}
	calculatedConstraint := links.MTUConstraint{
		Value: calculated, SourceNode: nodeID, Source: "Amnezia carrier",
		Reason:     "carrier UDP payload minus WireGuard and Amnezia S4 overhead",
		InputValue: carrier.MaxPayload, Overhead: overhead, Result: calculated,
		Path: carrierConstraint.Path,
	}
	selected := calculatedConstraint
	if profile.Interface.MTU > 0 {
		profileConstraint := links.MTUConstraint{
			Value: profile.Interface.MTU, SourceNode: nodeID,
			Source: "Amnezia profile", Reason: "profile interface MTU ceiling",
			InputValue: profile.Interface.MTU, Result: profile.Interface.MTU,
			Path: []uint64{nodeID},
		}
		selected = links.MinimumMTUConstraint(profileConstraint, calculatedConstraint)
	}
	if amnesiaProfileCarriesIPv6(profile) && selected.Value < links.IPv6MinimumLinkMTU {
		return status, "", fmt.Errorf(
			"amnezia carrier is incompatible with IPv6: safe MTU %d is below the required minimum %d",
			selected.Value, links.IPv6MinimumLinkMTU,
		)
	}
	status.mtu = selected.Value
	status.carrierMaxUDP = carrier.MaxPayload
	status.transportS4 = int(profile.AmneziaWG.TransportPadding)
	status.mtuConstraint = &selected
	return status, "", nil
}

func validateAmnesiaProfileDatagrams(limit int, profile *wgoamnesia.Profile) error {
	if profile == nil || limit <= 0 {
		return nil
	}
	awg := profile.AmneziaWG
	messages := []struct {
		name string
		size uint64
	}{
		{name: "handshake initiation", size: uint64(wgdevice.MessageInitiationSize) + uint64(awg.InitPadding)},
		{name: "handshake response", size: uint64(wgdevice.MessageResponseSize) + uint64(awg.ResponsePadding)},
		{name: "cookie reply", size: uint64(wgdevice.MessageCookieReplySize) + uint64(awg.CookiePadding)},
	}
	if awg.JunkPacketCount > 0 && awg.JunkPacketMaxSize > 0 {
		messages = append(messages, struct {
			name string
			size uint64
		}{name: "junk packet", size: uint64(awg.JunkPacketMaxSize)})
	}
	for index, spec := range awg.InitiationPackets {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		size, err := links.AmneziaInitiationPacketSize(spec)
		if err != nil {
			return fmt.Errorf("custom initiation packet I%d: %w", index+1, err)
		}
		messages = append(messages, struct {
			name string
			size uint64
		}{name: "custom initiation packet I" + strconv.Itoa(index+1), size: uint64(size)})
	}
	if awg.RandomTrailers.Set && awg.RandomTrailers.Enabled {
		messages = append(messages, struct {
			name string
			size uint64
		}{name: "maximum random handshake trailer", size: uint64(wgdevice.MaxMessageSize)})
	}
	for _, message := range messages {
		if message.size > uint64(limit) {
			return fmt.Errorf(
				"amnezia %s size %d exceeds carrier UDP payload %d",
				message.name, message.size, limit,
			)
		}
	}
	return nil
}

func amnesiaProfileCarriesIPv6(profile *wgoamnesia.Profile) bool {
	if profile == nil {
		return false
	}
	for _, value := range append(append([]string(nil), profile.Interface.Addresses...), profile.Peer.AllowedIPs...) {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(value)); err == nil && prefix.Addr().Is6() {
			return true
		}
		if address, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil && address.Is6() {
			return true
		}
	}
	return false
}
