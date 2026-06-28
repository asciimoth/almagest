package almagest

import (
	"fmt"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return tunJoinerClass{engine: e}
	}, false)
}

const (
	// NodeTypeTunJoiner is the node class name for a multi-input TUN joiner.
	NodeTypeTunJoiner = "almagest/TunJoiner"
)

type tunJoinerClass struct {
	engine *engine
}

func (tunJoinerClass) ClassName() string        { return NodeTypeTunJoiner }
func (tunJoinerClass) ShortDescription() string { return "TUN joiner" }
func (tunJoinerClass) LongDescription() string {
	return "Joins one default almagest/tun endpoint and any number of secondary almagest/tun endpoints using gonnect/tun.Joiner."
}
func (tunJoinerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Default"),
		TunPort("left", "Secondary"),
		TunPort("right", "Tun"),
	}}
}
func (c tunJoinerClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	pool := enginePool(c.engine)
	return &tunJoinerNode{
		pool:           pool,
		joiner:         tun.NewJoiner(pool),
		secondaryTuns:  map[uint64]tun.Tun{},
		sentOutputTuns: map[uint64]tun.Tun{},
		outputCfgs:     TunConfigByLink{},
	}, nil
}

// tunJoinerNode owns one gonnect/tun.Joiner and publishes it through a
// right-directed TypeTun output. The Default input accepts exactly one TUN and
// attaches it with Joiner.AttachDefault. The Secondary input intentionally
// relaxes TypeTun's normal one-link-per-port rule because Joiner supports an
// arbitrary number of secondary TUNs; each secondary link is tracked
// independently so link removal can call Joiner.Detach for the matching TUN.
type tunJoinerNode struct {
	pasta.BasicNode

	pool bufpool.Pool

	w  *pasta.Workspace
	id uint64

	defaultIn   uint64
	secondaryIn uint64
	out         uint64

	defaultLink uint64
	defaultTun  tun.Tun

	secondaryTuns  map[uint64]tun.Tun
	sentOutputTuns map[uint64]tun.Tun
	sentOutputWait map[uint64]tun.Tun
	outputCfgs     TunConfigByLink

	joiner *tun.Joiner
}

func (n *tunJoinerNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if n.joiner == nil {
		n.joiner = tun.NewJoiner(n.pool)
	}
	if n.secondaryTuns == nil {
		n.secondaryTuns = map[uint64]tun.Tun{}
	}
	if n.sentOutputTuns == nil {
		n.sentOutputTuns = map[uint64]tun.Tun{}
	}
	if n.sentOutputWait == nil {
		n.sentOutputWait = map[uint64]tun.Tun{}
	}
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.defaultIn = restored.LeftPorts[0]
		}
		if len(restored.LeftPorts) > 1 {
			n.secondaryIn = restored.LeftPorts[1]
		}
		if len(restored.RightPorts) > 0 {
			n.out = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *tunJoinerNode) OnReady() error {
	n.requestPort(n.defaultIn)
	n.requestPort(n.secondaryIn)
	n.sendAllOutputTuns()
	n.sendMergedTunConfig()
	return nil
}

func (n *tunJoinerNode) OnStop() {
	n.closeSentOutputTuns()
	if n.joiner != nil {
		_ = n.joiner.Close()
	}
	n.waitSentOutputTuns()
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
		return TunPreLinkAdd(n.w, port, linkType)
	case n.secondaryIn:
		if portDirection != "left" || linkType != TypeTun {
			return pasta.LinkTypeErr(linkType)
		}
	case n.out:
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
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
		std.Request(n.w, n.id, link)
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
		n.closeSentOutputTun(link)
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
			n.sendOutputTunToLink(event.Link)
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
	payload, ok := event.Payload.(TunPayload)
	if !ok || payload.Tun == nil {
		return nil
	}
	switch event.ReceiverPort {
	case n.defaultIn:
		return n.attachDefaultTun(event.Link, payload.Tun)
	case n.secondaryIn:
		return n.attachSecondaryTun(event.Link, payload.Tun)
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
	BindTunResource(n.w, n.id, link, t)
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
	BindTunResource(n.w, n.id, link, t)
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

func (n *tunJoinerNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *tunJoinerNode) sendAllOutputTuns() {
	snapshot, ok := n.w.PortSnapshot(n.out)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		n.sendOutputTunToLink(link)
	}
}

func (n *tunJoinerNode) sendOutputTunToLink(link uint64) {
	if n.joiner == nil {
		return
	}
	if wrapper := n.sentOutputTuns[link]; TunIsReady(wrapper) {
		n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
		return
	}
	n.closeSentOutputTun(link)
	wrapper := DetachTunForLink(n.w, n.id, link, n.joiner, n.pool)
	if wrapper == nil {
		return
	}
	n.sentOutputTuns[link] = wrapper
	n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
}

func (n *tunJoinerNode) sendMergedTunConfig() {
	n.sendMergedTunConfigToPort(n.defaultIn)
	n.sendMergedTunConfigToPort(n.secondaryIn)
}

func (n *tunJoinerNode) sendMergedTunConfigToPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		n.sendMergedTunConfigToLink(link)
	}
}

func (n *tunJoinerNode) sendMergedTunConfigToLink(link uint64) {
	if link == 0 {
		return
	}
	n.w.EmitEvent(n.id, link, n.outputCfgs.Merge())
}

func (n *tunJoinerNode) closeSentOutputTun(link uint64) {
	t := n.sentOutputTuns[link]
	delete(n.sentOutputTuns, link)
	if t == nil {
		return
	}
	_ = t.Close()
	if _, ok := t.(tunWaiter); ok {
		if n.sentOutputWait == nil {
			n.sentOutputWait = map[uint64]tun.Tun{}
		}
		n.sentOutputWait[link] = t
	}
}

func (n *tunJoinerNode) closeSentOutputTuns() {
	for link, t := range n.sentOutputTuns {
		delete(n.sentOutputTuns, link)
		if t == nil {
			continue
		}
		_ = t.Close()
		if _, ok := t.(tunWaiter); ok {
			if n.sentOutputWait == nil {
				n.sentOutputWait = map[uint64]tun.Tun{}
			}
			n.sentOutputWait[link] = t
		}
	}
}

func (n *tunJoinerNode) waitSentOutputTuns() {
	for link, t := range n.sentOutputWait {
		delete(n.sentOutputWait, link)
		waitTun(t)
	}
}

func (n *tunJoinerNode) updateLabel() error {
	if n.defaultTun == nil {
		return n.w.SetNodeLabel(n.id, "waiting for default")
	}
	secondaryCount := len(n.secondaryTuns)
	if secondaryCount == 0 {
		return n.w.SetNodeLabel(n.id, "joined default")
	}
	return n.w.SetNodeLabel(n.id, fmt.Sprintf("joined default + %d secondary", secondaryCount))
}
