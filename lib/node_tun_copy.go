package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return tunCopyClass{engine: e}
	}, false)
}

const (
	// NodeTypeTunCopy is the node class name for a bidirectional TUN packet
	// copier.
	NodeTypeTunCopy = "almagest/TunCopy"
)

type tunCopyClass struct {
	engine *engine
}

func (tunCopyClass) ClassName() string        { return NodeTypeTunCopy }
func (tunCopyClass) ShortDescription() string { return "TUN copy" }
func (tunCopyClass) LongDescription() string {
	return "Copies packets bidirectionally between two attached almagest/tun endpoints using gonnect/tun.Point2Point."
}
func (tunCopyClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "TunA"),
		TunPort("left", "TunB"),
	}}
}
func (c tunCopyClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunCopyNode{p2p: tun.NewP2P(c.engine.pool)}, nil
}

// tunCopyNode owns one Point2Point bridge. Both ports are left-directed
// TypeTun inputs: attaching a TUN payload to TunA calls Point2Point.SetA, and
// attaching a TUN payload to TunB calls Point2Point.SetB. Detaching either link
// calls the matching setter with nil so the bridge immediately stops using the
// removed endpoint.
type tunCopyNode struct {
	pasta.BasicNode

	w  *pasta.Workspace
	id uint64

	tunA uint64
	tunB uint64

	linkA uint64
	linkB uint64

	currentA tun.Tun
	currentB tun.Tun

	p2p *tun.Point2Point
}

func (n *tunCopyNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if n.p2p == nil {
		n.p2p = tun.NewP2P(nil)
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.tunA = restored.LeftPorts[0]
		}
		if len(restored.LeftPorts) > 1 {
			n.tunB = restored.LeftPorts[1]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *tunCopyNode) OnReady() error {
	n.requestPort(n.tunA)
	n.requestPort(n.tunB)
	return nil
}

func (n *tunCopyNode) OnStop() {
	if n.p2p != nil {
		n.p2p.Stop()
	}
	n.currentA = nil
	n.currentB = nil
}

func (n *tunCopyNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if portDirection != "left" || (port != n.tunA && port != n.tunB) {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.w, port, linkType)
}

func (n *tunCopyNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun || portDirection != "left" {
		return nil
	}
	switch port {
	case n.tunA:
		n.linkA = link
	case n.tunB:
		n.linkB = link
	default:
		return nil
	}
	std.Request(n.w, n.id, link)
	return n.updateLabel()
}

func (n *tunCopyNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun || portDirection != "left" {
		return nil
	}
	switch port {
	case n.tunA:
		if link != n.linkA {
			return nil
		}
		n.linkA = 0
		n.currentA = nil
		n.p2p.SetA(nil)
	case n.tunB:
		if link != n.linkB {
			return nil
		}
		n.linkB = 0
		n.currentB = nil
		n.p2p.SetB(nil)
	default:
		return nil
	}
	return n.updateLabel()
}

func (n *tunCopyNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun || receiverPortDirection != "left" || std.IsRequest(event.Payload) {
		return nil
	}
	payload, ok := event.Payload.(TunPayload)
	if !ok || payload.Tun == nil {
		return nil
	}
	switch event.ReceiverPort {
	case n.tunA:
		if event.Link != n.linkA || SameTunInstance(n.currentA, payload.Tun) {
			return nil
		}
		BindTunResource(n.w, n.id, event.Link, payload.Tun)
		n.currentA = payload.Tun
		n.p2p.SetA(payload.Tun)
	case n.tunB:
		if event.Link != n.linkB || SameTunInstance(n.currentB, payload.Tun) {
			return nil
		}
		BindTunResource(n.w, n.id, event.Link, payload.Tun)
		n.currentB = payload.Tun
		n.p2p.SetB(payload.Tun)
	default:
		return nil
	}
	return n.updateLabel()
}

func (n *tunCopyNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *tunCopyNode) updateLabel() error {
	switch {
	case n.currentA == nil:
		return n.w.SetNodeLabel(n.id, "waiting for TunA")
	case n.currentB == nil:
		return n.w.SetNodeLabel(n.id, "waiting for TunB")
	default:
		return n.w.SetNodeLabel(n.id, "copying")
	}
}
