package networknodes

import (
	"fmt"
	"sync"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"golang.org/x/time/rate"
)

const (
	// NodeTypeSpoofer is the node class name for a TUN-to-network adapter.
	NodeTypeSpoofer = "almagest/Spoofer"

	defaultSpooferMTU   = 1500
	defaultSpooferQueue = 1024
)

type SpooferClass struct {
	Runtime Runtime
}

func (SpooferClass) ClassName() string        { return NodeTypeSpoofer }
func (SpooferClass) ShortDescription() string { return "TUN network spoofer" }
func (SpooferClass) LongDescription() string {
	return "Forwards intercepted TUN flows through a network."
}
func (SpooferClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		NetworkPort("right", "Network"),
	}}
}
func (c SpooferClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSpooferConfig()
	config.read(cfg)
	return &spooferNode{NodeBase: NodeBase{Runtime: c.Runtime}, pool: enginePool(c.Runtime), config: config}, nil
}

// spooferNode owns a running netstack Stack while both input links are
// present. Any change to options, the source TUN, or the target network
// replaces the Stack so callbacks always close over a coherent pair.
type spooferNode struct {
	NodeBase

	pool    bufpool.Pool
	tunp    uint64
	network uint64

	config spooferConfig

	tun          tun.Tun
	tunLink      uint64
	networkValue NetworkCloser
	networkNode  uint64
	runtime      *spooferRuntime
	formError    string

	forwardLogOnce    sync.Once
	forwardLogLimiter *rate.Limiter
}

func (n *spooferNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
	n.tunp = restoredLeft(restored, 0)
	n.network = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *spooferNode) OnReady() error {
	n.RequestPort(n.tunp)
	n.RequestPort(n.network)
	n.rebuildStack()
	return nil
}

func (n *spooferNode) OnStop() {
	n.tunLink = 0
	source := n.tun
	n.tun = nil
	n.stopStack()
	if source != nil {
		_ = source.Close()
	}
}

func (n *spooferNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunp:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.W, port, linkType)
	case n.network:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *spooferNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.tunp:
		n.tunLink = link
		std.RequestLocked(n.W, n.ID, link)
	case n.network:
		std.RequestLocked(n.W, n.ID, link)
	}
	return nil
}

func (n *spooferNode) OnLinkRemoved(link uint64, port uint64, _, _ string) error {
	switch port {
	case n.tunp:
		if link != n.tunLink {
			return nil
		}
		n.tunLink = 0
		source := n.tun
		n.tun = nil
		n.rebuildStack()
		if source != nil {
			_ = source.Close()
		}
	case n.network:
		n.networkValue = nil
		n.networkNode = 0
		n.rebuildStack()
	}
	return nil
}

func (n *spooferNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunp && receiverPortDirection == "left" && linkType == TypeTun {
		next, changed := AcceptTunPayload(n.W, n.ID, event, n.tunLink, n.tun)
		if !changed {
			return nil
		}
		n.tun = next
		n.rebuildStack()
		return nil
	}

	if event.ReceiverPort == n.network && receiverPortDirection == "right" && linkType == TypeNetwork {
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.networkValue)
		if !changed {
			return nil
		}
		n.networkValue = next
		n.networkNode = event.SenderNode
		n.rebuildStack()
	}
	return nil
}

func (n *spooferNode) OnInbox(message pasta.InboxMessage) error {
	failed, ok := message.Payload.(spooferTunFailed)
	if !ok || !tun.IsTunTermError(failed.err) || failed.runtime != n.runtime || failed.link != n.tunLink || n.tun == nil {
		return nil
	}
	if n.Logger != nil {
		n.Logger.Warnf(
			"Spoofer TUN input failed; requesting a replacement: link=%d terminal=%t error=%v input={%s}",
			failed.link,
			tun.IsTunTermError(failed.err),
			failed.err,
			nodekit.TunDebugString(n.tun),
		)
	}
	source := n.tun
	n.tun = nil
	n.stopStack()
	_ = source.Close()
	if err := n.updateLabel(); err != nil {
		return err
	}
	std.RequestLocked(n.W, n.ID, failed.link)
	return nil
}

func (n *spooferNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *spooferNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"mtu":                  n.config.mtu,
		"queueLength":          n.config.queueLength,
		"disableSACK":          n.config.disableSACK,
		"ttl":                  n.config.ttl,
		"icmpBurst":            n.config.icmpBurst,
		"icmpLimit":            n.config.icmpLimit,
		"tcpSendBufferSize":    n.config.tcpSendBufferSize,
		"tcpReceiveBufferSize": n.config.tcpReceiveBufferSize,
		"congestionControlAlg": n.config.congestionControlAlg,
		"disableNagle":         n.config.disableNagle,
		"disableTCPModRecBuff": n.config.disableTCPModRecBuff,
		"tcpKeepAlive":         n.config.tcpKeepAlive,
		"tcpKeepAliveIdle":     n.config.tcpKeepAliveIdle,
		"tcpKeepAliveInterval": n.config.tcpKeepAliveInterval,
		"tcpKeepAliveCount":    n.config.tcpKeepAliveCount,
		"tcpForwardWnd":        n.config.tcpForwardWnd,
		"tcpForwardAttempts":   n.config.tcpForwardAttempts,
	})
}

func (n *spooferNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.IntValue(values["mtu"]); ok {
		next.mtu = value
	}
	if value, ok := formular.IntValue(values["queueLength"]); ok {
		next.queueLength = value
	}
	if value, ok := formular.BoolValue(values["disableSACK"]); ok {
		next.disableSACK = value
	}
	if value, ok := formular.IntValue(values["ttl"]); ok {
		next.ttl = value
	}
	if value, ok := formular.IntValue(values["icmpBurst"]); ok {
		next.icmpBurst = value
	}
	if value, ok := formular.FloatValue(values["icmpLimit"]); ok {
		next.icmpLimit = value
	}
	if value, ok := formular.IntValue(values["tcpSendBufferSize"]); ok {
		next.tcpSendBufferSize = value
	}
	if value, ok := formular.IntValue(values["tcpReceiveBufferSize"]); ok {
		next.tcpReceiveBufferSize = value
	}
	if value, ok := formular.StringValue(values["congestionControlAlg"]); ok {
		next.congestionControlAlg = value
	}
	if value, ok := formular.BoolValue(values["disableNagle"]); ok {
		next.disableNagle = value
	}
	if value, ok := formular.BoolValue(values["disableTCPModRecBuff"]); ok {
		next.disableTCPModRecBuff = value
	}
	if value, ok := formular.BoolValue(values["tcpKeepAlive"]); ok {
		next.tcpKeepAlive = value
	}
	if value, ok := formular.IntValue(values["tcpKeepAliveIdle"]); ok {
		next.tcpKeepAliveIdle = value
	}
	if value, ok := formular.IntValue(values["tcpKeepAliveInterval"]); ok {
		next.tcpKeepAliveInterval = value
	}
	if value, ok := formular.IntValue(values["tcpKeepAliveCount"]); ok {
		next.tcpKeepAliveCount = value
	}
	if value, ok := formular.IntValue(values["tcpForwardWnd"]); ok {
		next.tcpForwardWnd = value
	}
	if value, ok := formular.IntValue(values["tcpForwardAttempts"]); ok {
		next.tcpForwardAttempts = value
	}
	next.normalize()
	if err := validateSpooferConfig(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	n.formError = ""
	n.config = next
	n.sendForcedMenuSnapshot()
	n.rebuildStack()
	return nil
}

func (n *spooferNode) String() string {
	return fmt.Sprintf("spooferNode(%d)", n.ID)
}
