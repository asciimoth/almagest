package tunnodes

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const NodeTypeDebugLogTun = "almagest/LogTun"

type DebugLogTunClass struct {
	Runtime Runtime
}

func (DebugLogTunClass) ClassName() string        { return NodeTypeDebugLogTun }
func (DebugLogTunClass) ShortDescription() string { return "DEBUG TUN logger" }
func (DebugLogTunClass) LongDescription() string {
	return "Passes almagest/tun operations and configuration suggestions through unchanged while debug logging reads, writes, packet payload bytes, and TunConfig values."
}
func (DebugLogTunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "in"),
		TunPort("right", "Tun"),
	}}
}
func (c DebugLogTunClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugLogTunNode{pool: enginePool(c.Runtime), outputCfgs: TunConfigByLink{}}, nil
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
		return TunPreLinkAdd(n.W, port, linkType)
	}
	if port == n.out && portDirection == "right" {
		return TunPreLinkAdd(n.W, port, linkType)
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *debugLogTunNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeTun {
		std.RequestLocked(n.W, n.ID, link)
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
		n.outputTuns.InvalidateAllLocked(n.W, n.ID)
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
			n.logTunConfig(event.Link, cfg)
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
	next, changed := AcceptTunPayload(n.W, n.ID, event, 0, n.tun)
	if !changed {
		return nil
	}
	if n.tun != nil {
		_ = n.tun.Close()
	}
	n.outputTuns.InvalidateAllLocked(n.W, n.ID)
	n.outputTuns.Wait()
	n.tun = next
	n.sendAll()
	return nil
}

// logTunConfig records all suggestion fields, including DNS, before the value
// is forwarded to the upstream TUN provider.
func (n *debugLogTunNode) logTunConfig(link uint64, cfg TunConfig) {
	if n.Logger == nil {
		return
	}
	n.Logger.Debugf(
		"tun.TunConfig link=%d addrs=%v routes=%v sourceRoutes=%v mtu=%d dns=%q",
		link, cfg.Addrs, cfg.Routes, cfg.SourceRoutes, cfg.MTU, cfg.DNS,
	)
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
	n.outputTuns.SendWrappedLocked(n.W, n.ID, link, n.tun, func(t tun.Tun) tun.Tun {
		return &debugLogTun{Tun: t, logger: n.Logger}
	}, n.pool)
}

func (n *debugLogTunNode) refreshTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	n.outputTuns.RefreshWrappedLocked(n.W, n.ID, link, n.tun, func(t tun.Tun) tun.Tun {
		return &debugLogTun{Tun: t, logger: n.Logger}
	}, n.pool)
}

func (n *debugLogTunNode) sendMergedTunConfig() {
	BroadcastMergedTunConfig(n.W, n.ID, n.in, n.outputCfgs)
}

func (n *debugLogTunNode) sendMergedTunConfigToLink(link uint64) {
	EmitMergedTunConfig(n.W, n.ID, link, n.outputCfgs)
}
