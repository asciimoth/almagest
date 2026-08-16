package debugnodes

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type UDPPingClientClass struct {
	Runtime Runtime
}

func (UDPPingClientClass) ClassName() string        { return NodeTypeUDPPingClient }
func (UDPPingClientClass) ShortDescription() string { return "DEBUG UDP ping client" }
func (UDPPingClientClass) LongDescription() string  { return udpPingClassLongDescription }
func (UDPPingClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "Network", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Socket Mode", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Local Host", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Local Port", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Remote Host", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Remote Port", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Payload", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Timeout MS", Types: []string{std.TypeString}},
	}}
}
func (c UDPPingClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &udpPingClientNode{
		NodeBase:   NodeBase{Runtime: c.Runtime},
		cfg:        readUDPPingConfig(cfg, false),
		statusText: udpPingStatusReady,
	}, nil
}

type udpPingClientNode struct {
	NodeBase

	netp uint64
	cfgp udpPingConfigPorts

	cfg        udpPingConfig
	net        NetworkCloser
	logs       []formular.LogLine
	requestID  uint64
	pending    bool
	statusText string
	resp       string
	errText    string

	jobs chan udpPingClientRequest
	stop chan struct{}
}

func (n *udpPingClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.cfg.normalize(false)
	if n.statusText == "" {
		n.statusText = udpPingStatusReady
	}
	n.restorePorts(restored)
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.startWorker()
	n.sendMenuSnapshot()
	return nil
}

func (n *udpPingClientNode) restorePorts(restored *pasta.NodeInitData) {
	n.netp = restoredRight(restored, 0)
	n.cfgp.network = restoredLeft(restored, 0)
	n.cfgp.socketMode = restoredLeft(restored, 1)
	n.cfgp.localHost = restoredLeft(restored, 2)
	n.cfgp.localPort = restoredLeft(restored, 3)
	n.cfgp.remoteHost = restoredLeft(restored, 4)
	n.cfgp.remotePort = restoredLeft(restored, 5)
	n.cfgp.payload = restoredLeft(restored, 6)
	n.cfgp.timeout = restoredLeft(restored, 7)
}

func (n *udpPingClientNode) OnReady() error {
	n.requestConfig()
	return nil
}

func (n *udpPingClientNode) OnStop() {
	n.stopWorker()
}

func (n *udpPingClientNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
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

func (n *udpPingClientNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.netp || n.isConfigPort(port) {
		std.RequestLocked(n.W, n.ID, link)
	}
	if n.isConfigPort(port) {
		n.sendConfigBlock()
	}
	return nil
}

func (n *udpPingClientNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	if port == n.netp {
		n.net = nil
		n.resetStatusReady()
		return nil
	}
	if n.isConfigPort(port) {
		n.sendConfigBlock()
	}
	return nil
}

func (n *udpPingClientNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == TypeNetwork {
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.net)
		if !changed {
			return nil
		}
		n.net = next
		n.resetStatusReady()
		return nil
	}
	if receiverPortDirection == "left" && linkType == std.TypeString {
		if value, ok := std.StringFromPayload(event.Payload); ok && n.applyConfigString(event.ReceiverPort, value) {
			n.afterConfigChanged()
		}
	}
	return nil
}

func (n *udpPingClientNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case udpPingLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	case udpPingClientResult:
		if payload.id != n.requestID {
			return nil
		}
		n.pending = false
		n.resp = payload.resp
		n.errText = payload.err
		if payload.err != "" {
			n.statusText = udpPingStatusFail
		} else if payload.status != "" {
			n.statusText = payload.status
		} else {
			n.statusText = udpPingStatusReady
		}
		_ = n.updateLabel()
		n.sendConfigBlock()
		n.sendResultBlock()
	}
	return nil
}

func (n *udpPingClientNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		if n.configReadonly() {
			n.sendConfigBlock()
			return nil
		}
		if applyUDPPingForm(&n.cfg, msg.Values, false) {
			n.afterConfigChanged()
			return nil
		}
		n.sendConfigBlock()
	case formular.ButtonPressMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == "config" && msg.ButtonID == "send" {
			n.sendPing()
		}
	}
	return nil
}

func (n *udpPingClientNode) OnSave(cfg configer.Config) error {
	return saveUDPPingConfig(cfg, n.cfg)
}

func (n *udpPingClientNode) afterConfigChanged() {
	n.cfg.normalize(false)
	n.resetStatusReady()
}

func (n *udpPingClientNode) sendPing() {
	if n.pending {
		n.Logger.Debug("udp ping ignored: already pending")
		return
	}
	n.requestID++
	req := udpPingClientRequest{id: n.requestID, cfg: n.cfg, net: n.net}
	n.pending = true
	n.statusText = udpPingStatusRunning
	n.resp = ""
	n.errText = ""
	_ = n.updateLabel()
	n.sendConfigBlock()
	n.sendResultBlock()
	if n.jobs == nil {
		n.startWorker()
	}
	select {
	case n.jobs <- req:
	default:
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: udpPingClientResult{id: req.id, err: "worker is busy"}})
	}
}

func (n *udpPingClientNode) startWorker() {
	if n.jobs != nil || n.stop != nil {
		return
	}
	jobs := make(chan udpPingClientRequest, 1)
	stop := make(chan struct{})
	n.jobs = jobs
	n.stop = stop
	_ = n.SpawnWorker("udp ping client", func() {
		runUDPPingClientWorker(n.Context(), n.Logger, n.W, n.ID, jobs, stop)
	})
}

func (n *udpPingClientNode) stopWorker() {
	if n.stop != nil {
		close(n.stop)
		n.stop = nil
	}
	n.jobs = nil
}

func (n *udpPingClientNode) restartWorker() {
	n.stopWorker()
	n.startWorker()
}

func (n *udpPingClientNode) resetStatusReady() {
	n.requestID++
	n.pending = false
	n.statusText = udpPingStatusReady
	n.resp = ""
	n.errText = ""
	n.restartWorker()
	_ = n.updateLabel()
	n.sendConfigBlock()
	n.sendResultBlock()
}
