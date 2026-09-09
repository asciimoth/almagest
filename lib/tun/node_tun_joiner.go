package tunnodes

import (
	"fmt"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeTunJoiner is the node class name for a multi-input TUN joiner.
	NodeTypeTunJoiner = "almagest/TunJoiner"
)

type TunJoinerClass struct {
	Runtime Runtime
}

func (TunJoinerClass) ClassName() string        { return NodeTypeTunJoiner }
func (TunJoinerClass) ShortDescription() string { return "TUN joiner" }
func (TunJoinerClass) LongDescription() string {
	return "Joins one default almagest/tun endpoint and any number of secondary almagest/tun endpoints using flow-aware gonnect/tun.Joiner routing."
}
func (TunJoinerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Default"),
		TunPort("left", "Secondary"),
		TunPort("right", "Tun"),
	}}
}
func (c TunJoinerClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	pool := enginePool(c.Runtime)
	return &tunJoinerNode{
		NodeBase:      NodeBase{Runtime: c.Runtime},
		pool:          pool,
		secondaryTuns: map[uint64]tun.Tun{},
		outputCfgs:    TunConfigByLink{},
	}, nil
}

// tunJoinerNode owns one gonnect/tun.Joiner and publishes it through a
// right-directed TypeTun output. The Default input accepts exactly one TUN and
// attaches it with Joiner.AttachDefault. The Secondary input intentionally
// relaxes TypeTun's normal one-link-per-port rule because Joiner supports an
// arbitrary number of secondary TUNs; each secondary link is tracked
// independently so link removal can call Joiner.Detach for the matching TUN.
type tunJoinerNode struct {
	NodeBase

	pool bufpool.Pool

	defaultIn   uint64
	secondaryIn uint64
	out         uint64

	defaultLink uint64
	defaultTun  tun.Tun

	secondaryTuns map[uint64]tun.Tun
	outputTuns    TunOutputCache
	outputCfgs    TunConfigByLink

	joiner *tun.Joiner
}

func (n *tunJoinerNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, nil, id)
	if n.joiner == nil {
		// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
		n.joiner = tun.NewJoinerWithOptions(nil, n.pool, tun.JoinerOptions{
			SharedAddressRouting: true,
		})
	}
	if n.secondaryTuns == nil {
		n.secondaryTuns = map[uint64]tun.Tun{}
	}
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
	n.defaultIn = restoredLeft(restored, 0)
	n.secondaryIn = restoredLeft(restored, 1)
	n.out = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *tunJoinerNode) OnReady() error {
	n.RequestPort(n.defaultIn)
	n.RequestPort(n.secondaryIn)
	n.sendAllOutputTuns()
	n.sendMergedTunConfig()
	return nil
}

func (n *tunJoinerNode) OnStop() {
	n.outputTuns.CloseAll()
	if n.joiner != nil {
		_ = n.joiner.Close()
	}
	n.outputTuns.Wait()
	n.defaultTun = nil
	n.secondaryTuns = map[uint64]tun.Tun{}
	n.outputCfgs = TunConfigByLink{}
}

func (n *tunJoinerNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.defaultIn:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.W, port, linkType)
	case n.secondaryIn:
		if portDirection != "left" || linkType != TypeTun {
			return pasta.LinkTypeErr(linkType)
		}
	case n.out:
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.W, port, linkType)
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *tunJoinerNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	switch {
	case portDirection == "left" && (port == n.defaultIn || port == n.secondaryIn):
		if port == n.defaultIn {
			n.defaultLink = link
		}
		std.RequestLocked(n.W, n.ID, link)
		n.sendMergedTunConfigToLink(link)
	case portDirection == "right" && port == n.out:
		n.sendOutputTunToLink(link)
	}
	return n.updateLabel()
}

func (n *tunJoinerNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	switch {
	case portDirection == "left" && port == n.defaultIn:
		if link == n.defaultLink {
			n.detachDefaultTun()
		}
	case portDirection == "left" && port == n.secondaryIn:
		n.detachSecondaryTun(link)
	case portDirection == "right" && port == n.out:
		n.outputTuns.Close(link)
		n.outputCfgs.Delete(link)
		n.sendMergedTunConfig()
	}
	return n.updateLabel()
}

func (n *tunJoinerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "right" {
		if std.IsRequest(event.Payload) {
			n.refreshOutputTunToLink(event.Link)
			return nil
		}
		if cfg, ok := event.Payload.(TunConfig); ok {
			n.outputCfgs.Set(event.Link, cfg)
			n.sendMergedTunConfig()
		}
		return nil
	}
	if receiverPortDirection != "left" {
		return nil
	}
	if std.IsRequest(event.Payload) {
		n.sendMergedTunConfigToLink(event.Link)
		return nil
	}
	switch event.ReceiverPort {
	case n.defaultIn:
		next, changed := acceptTunPayload(event, n.defaultLink, n.defaultTun)
		if !changed {
			return nil
		}
		return n.attachDefaultTun(event.Link, next)
	case n.secondaryIn:
		next, changed := acceptTunPayload(event, 0, n.secondaryTuns[event.Link])
		if !changed {
			return nil
		}
		return n.attachSecondaryTun(event.Link, next)
	default:
		return nil
	}
}

func (n *tunJoinerNode) attachDefaultTun(link uint64, t tun.Tun) error {
	if link != n.defaultLink || SameTunInstance(n.defaultTun, t) {
		return nil
	}
	if err := n.joiner.AttachDefault(t); err != nil {
		return fmt.Errorf("attach default tun: %w", err)
	}
	BindTunResource(n.W, n.ID, link, t)
	n.defaultTun = t
	return n.updateLabel()
}

func (n *tunJoinerNode) attachSecondaryTun(link uint64, t tun.Tun) error {
	if SameTunInstance(n.secondaryTuns[link], t) {
		return nil
	}
	n.detachSecondaryTun(link)
	if err := n.joiner.AttachSecondary(t); err != nil {
		return fmt.Errorf("attach secondary tun: %w", err)
	}
	BindTunResource(n.W, n.ID, link, t)
	n.secondaryTuns[link] = t
	return n.updateLabel()
}

func (n *tunJoinerNode) detachDefaultTun() {
	if n.defaultTun != nil {
		_ = n.joiner.Detach(n.defaultTun)
	}
	n.defaultLink = 0
	n.defaultTun = nil
}

func (n *tunJoinerNode) detachSecondaryTun(link uint64) {
	t := n.secondaryTuns[link]
	delete(n.secondaryTuns, link)
	if t != nil {
		_ = n.joiner.Detach(t)
	}
}

func (n *tunJoinerNode) sendAllOutputTuns() {
	n.ForEachPortLink(n.out, func(link uint64) {
		n.sendOutputTunToLink(link)
	})
}

func (n *tunJoinerNode) sendOutputTunToLink(link uint64) {
	if n.joiner == nil {
		return
	}
	n.outputTuns.SendLocked(n.W, n.ID, link, n.joiner, n.pool)
}

func (n *tunJoinerNode) refreshOutputTunToLink(link uint64) {
	if n.joiner == nil {
		return
	}
	n.outputTuns.RefreshLocked(n.W, n.ID, link, n.joiner, n.pool)
}

func (n *tunJoinerNode) sendMergedTunConfig() {
	n.sendMergedTunConfigToPort(n.defaultIn)
	n.sendMergedTunConfigToPort(n.secondaryIn)
}

func (n *tunJoinerNode) sendMergedTunConfigToPort(port uint64) {
	BroadcastMergedTunConfig(n.W, n.ID, port, n.outputCfgs)
}

func (n *tunJoinerNode) sendMergedTunConfigToLink(link uint64) {
	EmitMergedTunConfig(n.W, n.ID, link, n.outputCfgs)
}

func (n *tunJoinerNode) updateLabel() error {
	if n.defaultTun == nil {
		return n.SetLabel("waiting for default")
	}
	secondaryCount := len(n.secondaryTuns)
	if secondaryCount == 0 {
		return n.SetLabel("joined default")
	}
	return n.SetLabel(fmt.Sprintf("joined default + %d secondary", secondaryCount))
}
