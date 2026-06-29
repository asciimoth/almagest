package almagest

import (
	_ "embed"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return socksServerClass{engine: e}
	}, false)
}

const (
	// NodeTypeSocksServer is the node class name for a SOCKS proxy server.
	NodeTypeSocksServer = "almagest/SocksServer"

	// DefaultSocksServerHost is the default host for the plain SOCKS listener.
	DefaultSocksServerHost = "127.0.0.1"
	// DefaultSocksServerPort is the default port for the plain SOCKS listener.
	DefaultSocksServerPort = 1080
	// DefaultSocksServerWSPath is the default HTTP path for SOCKS-over-WebSocket.
	DefaultSocksServerWSPath = "/socks"
)

var (
	//go:embed node_socks_server.md
	socksServerClassLongDescription string
)

type socksServerClass struct {
	engine *engine
}

func (socksServerClass) ClassName() string        { return NodeTypeSocksServer }
func (socksServerClass) ShortDescription() string { return "SOCKS server" }
func (socksServerClass) LongDescription() string  { return socksServerClassLongDescription }
func (socksServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "listen"),
		NetworkPort("right", "proxy"),
	}}
}
func (c socksServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSocksServerConfig()
	config.read(cfg)
	return &socksServerNode{NodeBase: NodeBase{engine: c.engine}, config: config}, nil
}

// socksServerNode accepts SOCKS client connections on the listen network and
// executes client requested proxy operations on the proxy network.
//
// The node owns two right-directed network ports. The listen port is required
// to start the background worker. The proxy port is optional; when it is absent
// socksgo receives a rejecting network so clients get protocol-level failures
// instead of failing to connect to the SOCKS server itself.
type socksServerNode struct {
	NodeBase

	listen uint64
	proxy  uint64

	config        socksServerConfig
	listenNetwork NetworkCloser
	proxyNetwork  NetworkCloser

	worker    *socksServerWorker
	restartID uint64
	logs      []formular.LogLine
	formError string
}

func (n *socksServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.config.commands == nil {
		n.config.commands = defaultSocksServerCommands()
	}
	if n.config.wsPath == "" {
		n.config.wsPath = DefaultSocksServerWSPath
	}

	if restored != nil {
		if len(restored.RightPorts) > 0 {
			n.listen = restored.RightPorts[0]
		}
		if len(restored.RightPorts) > 1 {
			n.proxy = restored.RightPorts[1]
		}
	}

	if err := n.SetPrimary(TypeNetwork); err != nil {
		n.logger.Errf("failed to set node primary type: %v", err)
		return err
	}

	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *socksServerNode) OnReady() error {
	n.RequestPort(n.listen)
	n.RequestPort(n.proxy)
	n.restartWorker("ready")
	return nil
}

func (n *socksServerNode) OnStop() {
	n.stopWorker()
}

func (n *socksServerNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.listen, n.proxy:
		if portDirection != "right" || linkType != TypeNetwork {
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

func (n *socksServerNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.listen || port == n.proxy {
		std.Request(n.w, n.id, link)
	}
	return nil
}

func (n *socksServerNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	switch port {
	case n.listen:
		n.listenNetwork = nil
		n.restartWorker("listen link removed")
	case n.proxy:
		n.proxyNetwork = nil
		n.restartWorker("proxy link removed")
	}
	return nil
}

func (n *socksServerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}

	switch event.ReceiverPort {
	case n.listen:
		next, changed := acceptSocksServerNetworkPayload(n.w, n.id, event, n.listenNetwork)
		if !changed {
			return nil
		}
		n.listenNetwork = next
		n.restartWorker("listen network updated")
	case n.proxy:
		next, changed := acceptSocksServerNetworkPayload(n.w, n.id, event, n.proxyNetwork)
		if !changed {
			return nil
		}
		n.proxyNetwork = next
		n.restartWorker("proxy network updated")
	}
	return nil
}

func acceptSocksServerNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok || !NetworkIsReady(payload.Network) {
		return current, false
	}
	if current != nil && SameNetworkSource(current, payload.Network) {
		if !SameNetworkInstance(current, payload.Network) {
			_ = payload.Network.Close()
		}
		return current, false
	}
	BindNetworkResource(w, node, event.Link, payload.Network)
	return payload.Network, true
}

func (n *socksServerNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case socksServerLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	case socksServerStart:
		if payload.id == n.restartID && n.worker == nil && n.listenNetwork != nil {
			n.startWorker(payload.cfg)
		}
	case socksServerDeferredStart:
		if payload.id == n.restartID && n.worker == nil {
			n.startWorkerIfReady(payload.id, nil)
		}
	}
	return nil
}

func (n *socksServerNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *socksServerNode) OnSave(cfg configer.Config) error {
	return saveSocksServerConfig(cfg, n.config)
}

func (n *socksServerNode) applyConfigForm(values map[string]any) error {
	next := socksServerConfigWithValues(n.config, values)
	if err := next.validate(); err != nil {
		n.formError = err.Error()
		n.appendLog(formular.LogWarn, err.Error())
		n.sendConfigBlock()
		n.sendLogsBlock()
		return nil
	}

	n.formError = ""
	n.config = next
	_ = n.updateLabel()
	n.sendForcedMenuSnapshot()
	n.restartWorker("config updated")
	return nil
}

func (n *socksServerNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	next, ok := socksServerConfigWithFieldValue(n.config, msg)
	if !ok {
		return
	}
	sendNodeFieldStatus(n.w, n.id, msg.Field, next.validate())
}
