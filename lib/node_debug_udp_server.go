package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type udpPingServerClass struct {
	engine *engine
}

func (udpPingServerClass) ClassName() string        { return NodeTypeUDPPingServer }
func (udpPingServerClass) ShortDescription() string { return "DEBUG UDP pong server" }
func (udpPingServerClass) LongDescription() string  { return udpPingClassLongDescription }
func (udpPingServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "Network", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Socket Mode", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Local Host", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Local Port", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Remote Host", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Remote Port", Types: []string{std.TypeString}},
	}}
}
func (c udpPingServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &udpPingServerNode{
		NodeBase: NodeBase{engine: c.engine},
		cfg:      readUDPPingConfig(cfg, true),
	}, nil
}

type udpPingServerNode struct {
	NodeBase

	netp uint64
	cfgp udpPingConfigPorts
	cfg  udpPingConfig
	net  NetworkCloser
	logs []formular.LogLine

	worker    *udpPingServerWorker
	restartID uint64
}

func (n *udpPingServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.cfg.normalize(true)
	n.restorePorts(restored)
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *udpPingServerNode) restorePorts(restored *pasta.NodeInitData) {
	n.netp = restoredRight(restored, 0)
	n.cfgp.network = restoredLeft(restored, 0)
	n.cfgp.socketMode = restoredLeft(restored, 1)
	n.cfgp.localHost = restoredLeft(restored, 2)
	n.cfgp.localPort = restoredLeft(restored, 3)
	n.cfgp.remoteHost = restoredLeft(restored, 4)
	n.cfgp.remotePort = restoredLeft(restored, 5)
}

func (n *udpPingServerNode) OnReady() error {
	n.requestConfig()
	n.restartWorker()
	return nil
}

func (n *udpPingServerNode) OnStop() {
	n.stopWorker()
}

func (n *udpPingServerNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.netp {
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	if n.isConfigPort(port) {
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *udpPingServerNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.netp || n.isConfigPort(port) {
		std.Request(n.w, n.id, link)
	}
	if n.isConfigPort(port) {
		n.sendConfigBlock()
	}
	return nil
}

func (n *udpPingServerNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	if port == n.netp {
		n.net = nil
		n.restartWorker()
		return nil
	}
	if n.isConfigPort(port) {
		n.sendConfigBlock()
	}
	return nil
}

func (n *udpPingServerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == TypeNetwork {
		next, changed := AcceptNetworkPayload(n.w, n.id, event, n.net)
		if !changed {
			return nil
		}
		n.net = next
		n.restartWorker()
		return nil
	}
	if receiverPortDirection == "left" && linkType == std.TypeString {
		if value, ok := std.StringFromPayload(event.Payload); ok && n.applyConfigString(event.ReceiverPort, value) {
			n.afterConfigChanged()
		}
	}
	return nil
}

func (n *udpPingServerNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case udpPingLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	case udpPingServerStart:
		if payload.id == n.restartID && n.worker == nil && payload.net != nil {
			n.startWorker(payload.cfg, payload.net)
		}
	}
	return nil
}

func (n *udpPingServerNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FormApplyMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
		return nil
	}
	if n.configReadonly() {
		n.sendConfigBlock()
		return nil
	}
	changed := applyUDPPingForm(&n.cfg, msg.Values, true)
	if changed {
		n.afterConfigChanged()
		return nil
	}
	n.sendConfigBlock()
	return nil
}

func (n *udpPingServerNode) OnSave(cfg configer.Config) error {
	return saveUDPPingConfig(cfg, n.cfg)
}

func (n *udpPingServerNode) afterConfigChanged() {
	n.cfg.normalize(true)
	_ = n.updateLabel()
	n.sendConfigBlock()
	n.restartWorker()
}

func (n *udpPingServerNode) restartWorker() {
	n.restartID++
	id := n.restartID
	old := n.worker
	if old != nil {
		n.worker = nil
		old.Stop()
	}
	if n.net == nil {
		n.log(formular.LogWarn, "waiting for network")
		return
	}
	cfg := n.cfg
	netw := n.net
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: udpPingServerStart{id: id, cfg: cfg, net: netw}})
		}()
		return
	}
	n.startWorker(cfg, netw)
}

func (n *udpPingServerNode) startWorker(cfg udpPingConfig, netw NetworkCloser) {
	worker, err := n.NewRestartableWorker("udp ping server", func(stop <-chan struct{}) {
		runUDPPingServerWorker(n.engine.ctx, n.logger, n.w, n.id, stop, cfg, netw)
	})
	if err != nil {
		return
	}
	n.worker = worker
}

func (n *udpPingServerNode) stopWorker() {
	if n.worker != nil {
		n.worker.Stop()
		n.worker = nil
	}
}
