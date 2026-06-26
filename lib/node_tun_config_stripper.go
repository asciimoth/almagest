package almagest

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return tunConfigStripperClass{engine: e}
	}, false)
}

const (
	// NodeTypeTunConfigStripper is the node class name for a TUN middleware
	// that blocks upstream TunConfig suggestions.
	NodeTypeTunConfigStripper = "almagest/TunConfigStripper"
)

type tunConfigStripperClass struct {
	engine *engine
}

func (tunConfigStripperClass) ClassName() string        { return NodeTypeTunConfigStripper }
func (tunConfigStripperClass) ShortDescription() string { return "TUN config stripper" }
func (tunConfigStripperClass) LongDescription() string {
	return "Passes almagest/tun events between one input and one output while dropping TunConfig suggestions."
}
func (tunConfigStripperClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		TunPort("right", "Tun"),
	}}
}
func (c tunConfigStripperClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunConfigStripperNode{pool: enginePool(c.engine), sent: map[uint64]tun.Tun{}}, nil
}

// tunConfigStripperNode is intentionally a narrow TUN middleware. It keeps the
// current upstream TunPayload, emits a detached wrapper to its output link, and
// drops every TunConfig event so downstream config suggestions cannot reach the
// upstream provider. Request events are honored on the output side and relayed
// upstream when the node still has no current TUN.
type tunConfigStripperNode struct {
	pasta.BasicNode

	pool bufpool.Pool

	w  *pasta.Workspace
	id uint64

	in  uint64
	out uint64

	inputLink  uint64
	outputLink uint64

	tun  tun.Tun
	sent map[uint64]tun.Tun
	wait []tun.Tun
}

func (n *tunConfigStripperNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if n.sent == nil {
		n.sent = map[uint64]tun.Tun{}
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.in = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.out = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	return n.w.SetNodeLabel(n.id, "stripping config")
}

func (n *tunConfigStripperNode) OnReady() error {
	n.requestInput()
	n.sendTunToOutput()
	return nil
}

func (n *tunConfigStripperNode) OnStop() {
	n.closeSentTuns()
	if n.tun != nil {
		_ = n.tun.Close()
		n.tun = nil
	}
	waitTuns(n.wait)
	n.wait = nil
}

func (n *tunConfigStripperNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" {
		return TunPreLinkAdd(n.w, port, linkType)
	}
	if port == n.out && portDirection == "right" {
		return TunPreLinkAdd(n.w, port, linkType)
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *tunConfigStripperNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	switch {
	case port == n.in && portDirection == "left":
		n.inputLink = link
		n.requestInput()
	case port == n.out && portDirection == "right":
		n.outputLink = link
		n.sendTunToOutput()
		if n.tun == nil {
			n.requestInput()
		}
	}
	return nil
}

func (n *tunConfigStripperNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	switch {
	case port == n.in && portDirection == "left" && link == n.inputLink:
		n.inputLink = 0
		if n.tun != nil {
			_ = n.tun.Close()
			n.tun = nil
		}
		n.closeSentTuns()
		waitTuns(n.wait)
		n.wait = nil
	case port == n.out && portDirection == "right" && link == n.outputLink:
		n.closeSentTun(link)
		n.outputLink = 0
	}
	return nil
}

func (n *tunConfigStripperNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	if _, ok := event.Payload.(TunConfig); ok {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "right" {
		if std.IsRequest(event.Payload) {
			if n.tun == nil {
				n.requestInput()
			}
			n.sendTunToOutput()
			return nil
		}
		n.forwardEvent(n.inputLink, event.Payload)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "left" {
		return nil
	}
	if std.IsRequest(event.Payload) {
		n.forwardEvent(n.outputLink, event.Payload)
		return nil
	}
	payload, ok := event.Payload.(TunPayload)
	if !ok {
		n.forwardEvent(n.outputLink, event.Payload)
		return nil
	}
	if payload.Tun == nil || event.Link != n.inputLink {
		return nil
	}
	if SameTunInstance(n.tun, payload.Tun) {
		return nil
	}
	if n.tun != nil {
		_ = n.tun.Close()
	}
	n.closeSentTuns()
	waitTuns(n.wait)
	n.wait = nil
	BindTunResource(n.w, n.id, event.Link, payload.Tun)
	n.tun = payload.Tun
	n.sendTunToOutput()
	return nil
}

func (n *tunConfigStripperNode) requestInput() {
	if n.inputLink != 0 {
		std.Request(n.w, n.id, n.inputLink)
	}
}

func (n *tunConfigStripperNode) sendTunToOutput() {
	if n.outputLink == 0 || n.tun == nil {
		return
	}
	wrapper := n.sent[n.outputLink]
	if wrapper == nil {
		wrapper = DetachTunForLink(n.w, n.id, n.outputLink, n.tun, n.pool)
		n.sent[n.outputLink] = wrapper
	}
	n.w.EmitEvent(n.id, n.outputLink, TunPayload{Tun: wrapper})
}

func (n *tunConfigStripperNode) forwardEvent(link uint64, payload any) {
	if link != 0 {
		n.w.EmitEvent(n.id, link, payload)
	}
}

func (n *tunConfigStripperNode) closeSentTun(link uint64) {
	t := n.sent[link]
	delete(n.sent, link)
	closeTunForLaterWait(&n.wait, t)
}

func (n *tunConfigStripperNode) closeSentTuns() {
	for link := range n.sent {
		n.closeSentTun(link)
	}
}
