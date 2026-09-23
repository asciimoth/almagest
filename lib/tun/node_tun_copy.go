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

	currentA    tun.Tun
	currentB    tun.Tun
	attachedA   *tunInputFailureObserver
	attachedB   *tunInputFailureObserver
	generationA uint64
	generationB uint64

	p2p *tun.Point2Point
}

type tunCopyInputFailed struct {
	port       uint64
	link       uint64
	generation uint64
	attachment *tunInputFailureObserver
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
	n.generationA++
	n.generationB++
	n.detachA()
	n.detachB()
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
		n.generationA++
		n.detachA()
	case n.tunB:
		if link != n.linkB {
			return nil
		}
		n.linkB = 0
		n.generationB++
		n.detachB()
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
		n.attachA(event.Link, next)
	case n.tunB:
		next, changed := AcceptTunPayload(n.W, n.ID, event, n.linkB, n.currentB)
		if !changed {
			return nil
		}
		n.attachB(event.Link, next)
	default:
		return nil
	}
	return n.updateLabel()
}

func (n *tunCopyNode) OnInbox(message pasta.InboxMessage) error {
	failed, ok := message.Payload.(tunCopyInputFailed)
	if !ok {
		return nil
	}
	switch failed.port {
	case n.tunA:
		if failed.link != n.linkA || failed.generation != n.generationA || failed.attachment != n.attachedA {
			return nil
		}
		n.generationA++
		n.detachA()
		std.RequestLocked(n.W, n.ID, failed.link)
	case n.tunB:
		if failed.link != n.linkB || failed.generation != n.generationB || failed.attachment != n.attachedB {
			return nil
		}
		n.generationB++
		n.detachB()
		std.RequestLocked(n.W, n.ID, failed.link)
	default:
		return nil
	}
	return n.updateLabel()
}

func (n *tunCopyNode) attachA(link uint64, source tun.Tun) {
	n.generationA++
	n.detachA()
	n.currentA = source
	n.attachedA = n.observeInput(n.tunA, link, n.generationA, source)
	n.p2p.SetA(n.attachedA)
}

func (n *tunCopyNode) attachB(link uint64, source tun.Tun) {
	n.generationB++
	n.detachB()
	n.currentB = source
	n.attachedB = n.observeInput(n.tunB, link, n.generationB, source)
	n.p2p.SetB(n.attachedB)
}

func (n *tunCopyNode) observeInput(port, link, generation uint64, source tun.Tun) *tunInputFailureObserver {
	var attachment *tunInputFailureObserver
	attachment = newTunInputFailureObserver(source, func(error) {
		// SendInbox takes the workspace lock. Point2Point can report while a
		// graph callback already owns that lock, so dispatch the report
		// separately.
		go n.W.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.ID,
			Payload: tunCopyInputFailed{
				port:       port,
				link:       link,
				generation: generation,
				attachment: attachment,
			},
		})
	})
	return attachment
}

func (n *tunCopyNode) detachA() {
	if n.p2p != nil {
		n.p2p.SetA(nil)
	}
	if n.attachedA != nil {
		_ = n.attachedA.Close()
	} else if n.currentA != nil {
		_ = n.currentA.Close()
	}
	n.currentA = nil
	n.attachedA = nil
}

func (n *tunCopyNode) detachB() {
	if n.p2p != nil {
		n.p2p.SetB(nil)
	}
	if n.attachedB != nil {
		_ = n.attachedB.Close()
	} else if n.currentB != nil {
		_ = n.currentB.Close()
	}
	n.currentB = nil
	n.attachedB = nil
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
