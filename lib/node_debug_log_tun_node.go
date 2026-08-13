package almagest

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type debugLogTunClass struct {
	engine *engine
}

func (debugLogTunClass) ClassName() string        { return NodeTypeDebugLogTun }
func (debugLogTunClass) ShortDescription() string { return "DEBUG TUN logger" }
func (debugLogTunClass) LongDescription() string {
	return "Passes almagest/tun operations through unchanged while debug logging reads, writes, and packet payload bytes."
}
func (debugLogTunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "in"),
		TunPort("right", "Tun"),
	}}
}
func (c debugLogTunClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugLogTunNode{pool: enginePool(c.engine), outputCfgs: TunConfigByLink{}}, nil
}

type debugLogTunNode struct {
	NodeBase

	pool bufpool.Pool

	in  uint64
	out uint64

	tun        tun.Tun
	outputTuns TunOutputCache

	outputCfgs TunConfigByLink
}

func (n *debugLogTunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
	n.in = restoredLeft(restored, 0)
	n.out = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	_ = n.SetLabel("tun log")
	return nil
}

func (n *debugLogTunNode) OnStop() {
	n.outputTuns.CloseAll()
	if n.tun != nil {
		_ = n.tun.Close()
		n.tun = nil
	}
	n.outputTuns.Wait()
	n.outputCfgs = TunConfigByLink{}
}

func (n *debugLogTunNode) OnReady() error {
	n.requestInput()
	n.sendAll()
	n.sendMergedTunConfig()
	return nil
}

func (n *debugLogTunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" {
		return TunPreLinkAdd(n.w, port, linkType)
	}
	if port == n.out && portDirection == "right" {
		return TunPreLinkAdd(n.w, port, linkType)
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *debugLogTunNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeTun {
		std.RequestLocked(n.w, n.id, link)
		n.sendMergedTunConfigToLink(link)
		return nil
	}
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.sendTunToLink(link)
	}
	return nil
}

func (n *debugLogTunNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.outputTuns.Close(link)
		n.outputCfgs.Delete(link)
		n.sendMergedTunConfig()
		return nil
	}
	if port == n.in && portDirection == "left" && linkType == TypeTun {
		if n.tun != nil {
			_ = n.tun.Close()
		}
		n.tun = nil
		n.outputTuns.CloseAll()
		n.outputTuns.Wait()
	}
	return nil
}

func (n *debugLogTunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "right" {
		if std.IsRequest(event.Payload) {
			n.refreshTunToLink(event.Link)
			return nil
		}
		if cfg, ok := event.Payload.(TunConfig); ok {
			n.outputCfgs.Set(event.Link, cfg)
			n.sendMergedTunConfig()
		}
		return nil
	}
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && std.IsRequest(event.Payload) {
		n.sendMergedTunConfigToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "left" {
		return nil
	}
	next, changed := AcceptTunPayload(n.w, n.id, event, 0, n.tun)
	if !changed {
		return nil
	}
	if n.tun != nil {
		_ = n.tun.Close()
	}
	n.outputTuns.CloseAll()
	n.outputTuns.Wait()
	n.tun = next
	n.sendAll()
	return nil
}

func (n *debugLogTunNode) requestInput() {
	n.RequestPort(n.in)
}

func (n *debugLogTunNode) sendAll() {
	n.ForEachPortLink(n.out, func(link uint64) {
		n.sendTunToLink(link)
	})
}

func (n *debugLogTunNode) sendTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	n.outputTuns.SendWrappedLocked(n.w, n.id, link, n.tun, func(t tun.Tun) tun.Tun {
		return &debugLogTun{Tun: t, logger: n.logger}
	}, n.pool)
}

func (n *debugLogTunNode) refreshTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	n.outputTuns.RefreshWrappedLocked(n.w, n.id, link, n.tun, func(t tun.Tun) tun.Tun {
		return &debugLogTun{Tun: t, logger: n.logger}
	}, n.pool)
}

func (n *debugLogTunNode) sendMergedTunConfig() {
	BroadcastMergedTunConfig(n.w, n.id, n.in, n.outputCfgs)
}

func (n *debugLogTunNode) sendMergedTunConfigToLink(link uint64) {
	EmitMergedTunConfig(n.w, n.id, link, n.outputCfgs)
}
