package socksnodes

import (
	_ "embed"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

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

type SocksServerClass struct {
	Runtime Runtime
}

func (SocksServerClass) ClassName() string        { return NodeTypeSocksServer }
func (SocksServerClass) ShortDescription() string { return "SOCKS server" }
func (SocksServerClass) LongDescription() string  { return socksServerClassLongDescription }
func (SocksServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "listen"),
		NetworkPort("right", "proxy"),
	}}
}
func (c SocksServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSocksServerConfig()
	config.read(cfg)
	return &socksServerNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: config}, nil
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
		n.Logger.Errf("failed to set node primary type: %v", err)
		return err
	}

	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *socksServerNode) OnReady() error {
	n.RequestPort(n.listen)
	n.RequestPort(n.proxy)
	n.ensureWorkerStarted("ready")
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
		std.RequestLocked(n.W, n.ID, link)
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
		next, changed := acceptSocksServerNetworkPayload(n.W, n.ID, event, n.listenNetwork)
		if !changed {
			return nil
		}
		n.listenNetwork = next
		n.restartWorker("listen network updated")
	case n.proxy:
		next, changed := acceptSocksServerNetworkPayload(n.W, n.ID, event, n.proxyNetwork)
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
	if SameNetworkInstance(current, payload.Network) {
		return current, false
	}
	_ = closeNetwork(current)
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
	case SecretValueChanged:
		n.handlePasswordSecretChanged(payload)
	}
	return nil
}

// handlePasswordSecretChanged reloads each affected runtime password. A rename
// also updates the persisted selector while an unavailable or deleted secret
// leaves the inline password as the fallback.
func (n *socksServerNode) handlePasswordSecretChanged(msg SecretValueChanged) {
	relevant := false
	prev := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	for i := range n.config.users {
		if !socksSecretChangeMatches(n.config.users[i].passwordSecret, msg) {
			continue
		}
		relevant = true
		if n.config.users[i].passwordSecret == prev && next != "" && next != prev {
			n.config.users[i].passwordSecret = next
		}
	}
	if !relevant {
		return
	}
	n.sendConfigBlock()
	n.restartWorker("password secret updated")
}

func (n *socksServerNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *socksServerNode) OnSave(cfg configer.Config) error {
	return saveSocksServerConfig(cfg, n.config)
}

func (n *socksServerNode) ensureWorkerStarted(reason string) {
	if n.worker != nil {
		return
	}
	n.restartWorker(reason)
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
	sendNodeFieldStatus(n.W, n.ID, msg.Field, next.validate())
}
