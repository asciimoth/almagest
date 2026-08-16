package tunnodes

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeTunCopy is the node class name for a bidirectional TUN packet
	// copier.
	NodeTypeTunCopy = "almagest/TunCopy"
)

type TunCopyClass struct {
	Runtime Runtime
}

func (TunCopyClass) ClassName() string        { return NodeTypeTunCopy }
func (TunCopyClass) ShortDescription() string { return "TUN copy" }
func (TunCopyClass) LongDescription() string {
	return "Copies packets bidirectionally between two attached almagest/tun endpoints using gonnect/tun.Point2Point."
}
func (TunCopyClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "TunA"),
		TunPort("left", "TunB"),
	}}
}
func (c TunCopyClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	pool := enginePool(c.Runtime)
	return &tunCopyNode{pool: pool}, nil
}

// tunCopyNode owns one Point2Point bridge. Both ports are left-directed
// TypeTun inputs: attaching a TUN payload to TunA calls Point2Point.SetA, and
// attaching a TUN payload to TunB calls Point2Point.SetB. Detaching either link
// calls the matching setter with nil so the bridge immediately stops using the
// removed endpoint.
type tunCopyNode struct {
	NodeBase

	pool bufpool.Pool

	tunA uint64
	tunB uint64

	linkA uint64
	linkB uint64

	currentA tun.Tun
	currentB tun.Tun

	p2p *tun.Point2Point
}

func (n *tunCopyNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, nil, id)
	if n.p2p == nil {
		// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
		n.p2p = tun.NewP2P(n.pool, nil)
	}
	n.tunA = restoredLeft(restored, 0)
	n.tunB = restoredLeft(restored, 1)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *tunCopyNode) OnReady() error {
	n.RequestPort(n.tunA)
	n.RequestPort(n.tunB)
	return nil
}

func (n *tunCopyNode) OnStop() {
	if n.currentA != nil {
		_ = n.currentA.Close()
		n.currentA = nil
	}
	if n.currentB != nil {
		_ = n.currentB.Close()
		n.currentB = nil
	}
	if n.p2p != nil {
		n.p2p.Stop()
	}
}

func (n *tunCopyNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if portDirection != "left" || (port != n.tunA && port != n.tunB) {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.W, port, linkType)
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
	std.RequestLocked(n.W, n.ID, link)
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
	if linkType != TypeTun || receiverPortDirection != "left" {
		return nil
	}
	switch event.ReceiverPort {
	case n.tunA:
		next, changed := AcceptTunPayload(n.W, n.ID, event, n.linkA, n.currentA)
		if !changed {
			return nil
		}
		n.currentA = next
		n.p2p.SetA(next)
	case n.tunB:
		next, changed := AcceptTunPayload(n.W, n.ID, event, n.linkB, n.currentB)
		if !changed {
			return nil
		}
		n.currentB = next
		n.p2p.SetB(next)
	default:
		return nil
	}
	return n.updateLabel()
}

func (n *tunCopyNode) updateLabel() error {
	switch {
	case n.currentA == nil:
		return n.SetLabel("waiting for TunA")
	case n.currentB == nil:
		return n.SetLabel("waiting for TunB")
	default:
		return n.SetLabel("copying")
	}
}
