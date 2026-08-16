package httpnodes

import (
	_ "embed"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	NodeTypeHTTPServer = "almagest/HttpServer"

	DefaultHTTPServerHost = "127.0.0.1"
	DefaultHTTPServerPort = 8080
)

var (
	//go:embed node_http_server.md
	HTTPServerClassLongDescription string
)

type HTTPServerClass struct {
	Runtime Runtime
}

func (HTTPServerClass) ClassName() string        { return NodeTypeHTTPServer }
func (HTTPServerClass) ShortDescription() string { return "Debug HTTP server" }
func (HTTPServerClass) LongDescription() string  { return HTTPServerClassLongDescription }
func (HTTPServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "Response", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Host", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Port", Types: []string{std.TypeString}},
	}}
}
func (c HTTPServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &httpServerNode{
		NodeBase: NodeBase{Runtime: c.Runtime},
		host:     configer.String(cfg, configer.Path{"host"}, DefaultHTTPServerHost),
		port:     configer.Int(cfg, configer.Path{"port"}, DefaultHTTPServerPort),
		response: configer.String(cfg, configer.Path{"response"}, ""),
	}, nil
}

type httpServerNode struct {
	NodeBase

	netp  uint64
	in    uint64
	hostp uint64
	portp uint64

	host     string
	port     int
	response string
	network  NetworkCloser

	worker    *httpServerWorker
	restartID uint64
	logs      []formular.LogLine
}

func (n *httpServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)

	if n.host == "" {
		n.host = DefaultHTTPServerHost
	}
	if n.port == 0 {
		n.port = DefaultHTTPServerPort
	}

	n.netp = restoredRight(restored, 0)
	n.in = restoredLeft(restored, 0)
	n.hostp = restoredLeft(restored, 1)
	n.portp = restoredLeft(restored, 2)

	if err := n.SetPrimary(TypeNetwork); err != nil {
		n.Logger.Errf("failed to set node primary type: %v", err)
		return err
	}

	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *httpServerNode) OnReady() error {
	n.requestNetwork()
	n.requestResponse()
	n.requestHost()
	n.requestListenPort()
	n.restartWorker()
	return nil
}

func (n *httpServerNode) OnStop() {
	n.stopWorker()
}

func (n *httpServerNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.netp:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}

	case n.in:
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}

	case n.hostp, n.portp:
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}

	default:
		return pasta.LinkTypeErr(linkType)
	}

	return nil
}

func (n *httpServerNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.netp, n.in:
		std.RequestLocked(n.W, n.ID, link)

	case n.hostp, n.portp:
		n.sendListenBlock()
		std.RequestLocked(n.W, n.ID, link)
	}

	return nil
}

func (n *httpServerNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	switch port {
	case n.netp:
		n.network = nil
		n.restartWorker()

	case n.hostp, n.portp:
		n.sendListenBlock()
	}

	return nil
}

func (n *httpServerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == TypeNetwork {
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if !changed {
			return nil
		}
		n.network = next
		n.restartWorker()
		return nil
	}

	if receiverPortDirection == "left" && linkType == std.TypeString {
		switch event.ReceiverPort {
		case n.in:
			if value, ok := std.StringFromPayload(event.Payload); ok {
				n.response = value
				n.restartWorker()
			}

		case n.hostp:
			if value, ok := std.StringFromPayload(event.Payload); ok {
				n.setHost(value)
			}

		case n.portp:
			if value, ok := std.StringFromPayload(event.Payload); ok {
				n.setPortString(value)
			}
		}
	}

	return nil
}

func (n *httpServerNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case httpServerLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	case httpServerStart:
		if payload.id == n.restartID && n.worker == nil && n.network != nil {
			n.startWorker(payload.cfg)
		}
	}
	return nil
}

func (n *httpServerNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FormApplyMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "listen" {
		return nil
	}

	if n.listenReadonly() {
		n.sendListenBlock()
		return nil
	}

	changed := false

	if value, ok := std.StringFromPayload(msg.Values["host"]); ok {
		changed = n.setHostNoRefresh(value) || changed
	}

	if value, ok := std.IntFromPayload(msg.Values["port"]); ok {
		changed = n.setPortNoRefresh(value) || changed
	}

	if changed {
		_ = n.updateLabel()
		n.sendListenBlock()
		n.restartWorker()
	} else {
		n.sendListenBlock()
	}

	return nil
}
