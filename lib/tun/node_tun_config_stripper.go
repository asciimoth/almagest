package tunnodes

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeTunConfigStripper is the node class name for a TUN middleware
	// that blocks upstream TunConfig suggestions.
	NodeTypeTunConfigStripper = "almagest/TunConfigStripper"
)

type TunConfigStripperClass struct {
	Runtime Runtime
}

func (TunConfigStripperClass) ClassName() string        { return NodeTypeTunConfigStripper }
func (TunConfigStripperClass) ShortDescription() string { return "TUN config stripper" }
func (TunConfigStripperClass) LongDescription() string {
	return "Passes almagest/tun events between one input and one output while dropping TunConfig suggestions."
}
func (TunConfigStripperClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		TunPort("right", "Tun"),
	}}
}
func (c TunConfigStripperClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunConfigStripperNode{pool: enginePool(c.Runtime)}, nil
}

// tunConfigStripperNode is intentionally a narrow TUN middleware. It keeps the
// current upstream TunPayload, emits a detached wrapper to its output link, and
// drops every TunConfig event so downstream config suggestions cannot reach the
// upstream provider. Request events are honored on the output side and relayed
// upstream when the node still has no current TUN.
type tunConfigStripperNode struct {
	NodeBase

	pool bufpool.Pool

	in  uint64
	out uint64

	inputLink  uint64
	outputLink uint64

	tun        tun.Tun
	outputTuns TunOutputCache
}

func (n *tunConfigStripperNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, nil, id)
	n.in = restoredLeft(restored, 0)
	n.out = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	return n.SetLabel("stripping config")
}

func (n *tunConfigStripperNode) OnReady() error {
	n.requestInput()
	n.sendTunToOutput()
	return nil
}

func (n *tunConfigStripperNode) OnStop() {
	n.outputTuns.CloseAll()
	if n.tun != nil {
		_ = n.tun.Close()
		n.tun = nil
	}
	n.outputTuns.Wait()
}

func (n *tunConfigStripperNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" {
		return TunPreLinkAdd(n.W, port, linkType)
	}
	if port == n.out && portDirection == "right" {
		return TunPreLinkAdd(n.W, port, linkType)
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
		n.outputTuns.CloseAll()
		n.outputTuns.Wait()
	case port == n.out && portDirection == "right" && link == n.outputLink:
		n.outputTuns.Close(link)
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
			n.refreshTunToOutput()
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
	n.outputTuns.CloseAll()
	n.outputTuns.Wait()
	BindTunResource(n.W, n.ID, event.Link, payload.Tun)
	n.tun = payload.Tun
	n.sendTunToOutput()
	return nil
}

func (n *tunConfigStripperNode) requestInput() {
	if n.inputLink != 0 {
		std.RequestLocked(n.W, n.ID, n.inputLink)
	}
}

func (n *tunConfigStripperNode) sendTunToOutput() {
	if n.outputLink == 0 || n.tun == nil {
		return
	}
	n.outputTuns.SendLocked(n.W, n.ID, n.outputLink, n.tun, n.pool)
}

func (n *tunConfigStripperNode) refreshTunToOutput() {
	if n.outputLink == 0 || n.tun == nil {
		return
	}
	n.outputTuns.RefreshLocked(n.W, n.ID, n.outputLink, n.tun, n.pool)
}

func (n *tunConfigStripperNode) forwardEvent(link uint64, payload any) {
	n.EmitIfLinked(link, payload)
}
