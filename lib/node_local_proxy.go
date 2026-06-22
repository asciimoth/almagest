package almagest

import (
	_ "embed"
	"maps"
	"net"
	"strconv"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo/protocol"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return localProxyClass{engine: e}
	}, false)
}

const (
	// NodeTypeLocalProxy is the node class name for a local SOCKS proxy backed
	// by system LocalNet for incoming client connections.
	NodeTypeLocalProxy = "almagest/LocalProxy"
)

var (
	//go:embed node_local_proxy.md
	localProxyClassLongDescription string
)

type localProxyClass struct {
	engine *engine
}

func (localProxyClass) ClassName() string        { return NodeTypeLocalProxy }
func (localProxyClass) ShortDescription() string { return "Local proxy" }
func (localProxyClass) LongDescription() string  { return localProxyClassLongDescription }
func (localProxyClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "proxy"),
	}}
}
func (c localProxyClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSocksServerConfig()
	config.read(cfg)
	return &localProxyNode{engine: c.engine, config: config}, nil
}

// localProxyNode accepts SOCKS client connections on sysnet.System.LocalNet and
// executes requested proxy operations on the connected proxy Network port.
type localProxyNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w     *pasta.Workspace
	id    uint64
	proxy uint64

	config       socksServerConfig
	localNetwork NetworkCloser
	proxyNetwork NetworkCloser

	worker    *socksServerWorker
	restartID uint64
	logs      []formular.LogLine
	formError string
}

type localProxyStart struct {
	id  uint64
	cfg socksServerWorkerConfig
}

func (n *localProxyNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	if n.config.commands == nil {
		n.config.commands = defaultSocksServerCommands()
	}
	if n.config.wsPath == "" {
		n.config.wsPath = DefaultSocksServerWSPath
	}

	if restored != nil && len(restored.RightPorts) > 0 {
		n.proxy = restored.RightPorts[0]
	}
	if n.engine != nil && n.engine.sys != nil {
		n.localNetwork = gonnect.DetachNetwork(n.engine.sys.LocalNet(), nil)
		BindNetworkResource(n.w, n.id, 0, n.localNetwork)
	}

	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		n.logger.Errf("failed to set node primary type: %v", err)
		return err
	}

	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *localProxyNode) OnReady() error {
	n.requestNetwork(n.proxy)
	n.restartWorker()
	return nil
}

func (n *localProxyNode) OnStop() {
	n.stopWorker()
}

func (n *localProxyNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.proxy || portDirection != "right" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	if n.portHasLinks(port) {
		return pasta.ErrLinkDup
	}
	return nil
}

func (n *localProxyNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.proxy {
		std.Request(n.w, n.id, link)
	}
	return nil
}

func (n *localProxyNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	if port == n.proxy {
		n.proxyNetwork = nil
		n.restartWorker()
	}
	return nil
}

func (n *localProxyNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.proxy || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok || payload.Network == nil {
		return nil
	}
	if NetworkIsReady(n.proxyNetwork) && SameNetworkSource(n.proxyNetwork, payload.Network) {
		if !SameNetworkInstance(n.proxyNetwork, payload.Network) {
			_ = payload.Network.Close()
		}
		return nil
	}
	BindNetworkResource(n.w, n.id, event.Link, payload.Network)
	n.proxyNetwork = payload.Network
	n.restartWorker()
	return nil
}

func (n *localProxyNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case socksServerLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	case localProxyStart:
		if payload.id == n.restartID && n.worker == nil && n.localNetwork != nil {
			n.startWorker(payload.cfg)
		}
	}
	return nil
}

func (n *localProxyNode) OnFormularMsg(message any) error {
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

func (n *localProxyNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := map[string]any{
		"socksHost": n.config.socksHost,
		"socksPort": n.config.socksPort,
		"wsHost":    n.config.wsHost,
		"wsPort":    n.config.wsPort,
		"wsPath":    n.config.wsPath,
		"users":     socksServerUsersConfig(n.config.users),
		"commands":  socksServerCommandsConfig(n.config.commands),
	}
	for key, value := range values {
		if err := cfg.Set(configer.Path{key}, value); err != nil {
			return err
		}
	}
	return nil
}

func (n *localProxyNode) applyConfigForm(values map[string]any) error {
	next := n.config
	next.commands = maps.Clone(n.config.commands)
	if value, ok := stringFromAny(values["socksHost"]); ok {
		next.socksHost = strings.TrimSpace(value)
	}
	if value, ok := intFromAny(values["socksPort"]); ok {
		next.socksPort = validPort(value)
	}
	if value, ok := stringFromAny(values["wsHost"]); ok {
		next.wsHost = strings.TrimSpace(value)
	}
	if value, ok := intFromAny(values["wsPort"]); ok {
		next.wsPort = validPort(value)
	}
	if value, ok := stringFromAny(values["wsPath"]); ok {
		next.wsPath = normalizeSocksServerPath(value)
	}
	if users, ok := parseSocksServerUsers(values["users"]); ok {
		next.users = users
	}
	for cmd, field := range socksServerCommandFields() {
		if value, ok := boolFromAny(values[field]); ok {
			next.commands[cmd] = value
		}
	}
	if next.wsPath == "" {
		next.wsPath = DefaultSocksServerWSPath
	}

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
	n.restartWorker()
	return nil
}

func (n *localProxyNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	next := n.config
	switch msg.Field.FieldID {
	case "socksHost":
		if value, ok := stringFromAny(msg.Value); ok {
			next.socksHost = strings.TrimSpace(value)
		}
	case "socksPort":
		if value, ok := intFromAny(msg.Value); ok {
			next.socksPort = value
		}
	case "wsHost":
		if value, ok := stringFromAny(msg.Value); ok {
			next.wsHost = strings.TrimSpace(value)
		}
	case "wsPort":
		if value, ok := intFromAny(msg.Value); ok {
			next.wsPort = value
		}
	case "wsPath":
		if value, ok := stringFromAny(msg.Value); ok {
			next.wsPath = normalizeSocksServerPath(value)
		}
	default:
		return
	}
	if next.wsPath == "" {
		next.wsPath = DefaultSocksServerWSPath
	}

	status := formular.StatusOK
	statusText := ""
	if err := next.validate(); err != nil {
		status = formular.StatusError
		statusText = err.Error()
	}
	n.w.SendNodeMenuMsg(n.id, formular.FieldStatusMessage{
		MessageBase: formular.MessageBase{
			Type:            formular.MessageFieldStatus,
			MenuID:          pasta.NodeMenuID(n.id),
			MenuGeneration:  1,
			BlockGeneration: 1,
		},
		Field:      msg.Field,
		Status:     status,
		StatusText: statusText,
	})
}

func (n *localProxyNode) restartWorker() {
	n.restartID++
	id := n.restartID
	n.logger.Debug("local proxy worker restarting")
	n.appendLog(formular.LogDebug, "worker restarting")
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		n.worker = nil
		close(old.stop)
	}

	if n.localNetwork == nil {
		n.logger.Debug("local proxy worker waiting for local network")
		n.appendLog(formular.LogWarn, "waiting for local network")
		n.sendLogsBlock()
		return
	}

	proxy := gonnect.Network(&gonnect.RejectNetwork{})
	if n.proxyNetwork != nil {
		proxy = n.proxyNetwork
	}
	cfg := socksServerWorkerConfig{
		config:        n.config,
		listenNetwork: n.localNetwork,
		proxyNetwork:  proxy,
	}
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: localProxyStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

func (n *localProxyNode) startWorker(cfg socksServerWorkerConfig) {
	stop := make(chan struct{})
	worker := &socksServerWorker{stop: stop, done: make(chan struct{})}
	n.worker = worker
	n.logger.Debug("local proxy worker starting")
	go func() {
		defer close(worker.done)
		runSocksServerWorker(n.engine.ctx, n.logger, n.w, n.id, stop, cfg)
	}()
}

func (n *localProxyNode) stopWorker() {
	if n.worker != nil {
		n.logger.Debug("local proxy worker stopping")
		close(n.worker.stop)
		n.worker = nil
	}
}

func (n *localProxyNode) requestNetwork(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *localProxyNode) updateLabel() error {
	if n.config.socksPort > 0 {
		return n.w.SetNodeLabel(n.id, net.JoinHostPort(hostOrDefault(n.config.socksHost), strconv.Itoa(n.config.socksPort)))
	}
	if n.config.wsPort > 0 {
		return n.w.SetNodeLabel(n.id, "ws "+net.JoinHostPort(hostOrDefault(n.config.wsHost), strconv.Itoa(n.config.wsPort)))
	}
	return n.w.SetNodeLabel(n.id, "disabled")
}

func (n *localProxyNode) portHasLinks(port uint64) bool {
	if n.w == nil || port == 0 {
		return false
	}
	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

func (n *localProxyNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock(), n.logsBlock()},
	})
}

func (n *localProxyNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      []formular.Block{n.configBlock(), n.logsBlock()},
	})
}

func (n *localProxyNode) sendConfigBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.configBlock(),
	})
}

func (n *localProxyNode) sendLogsBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.logsBlock(),
	})
}

func (n *localProxyNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		socksServerTextField("socksHost", "SOCKS host", n.config.socksHost, statusErr, n.formError),
		socksServerIntField("socksPort", "SOCKS port", n.config.socksPort, statusErr, n.formError),
		socksServerTextField("wsHost", "SOCKS+WS host", n.config.wsHost, statusErr, n.formError),
		socksServerIntField("wsPort", "SOCKS+WS port", n.config.wsPort, statusErr, n.formError),
		socksServerTextField("wsPath", "SOCKS+WS path", n.config.wsPath, false, ""),
		socksServerUsersField(n.config.users),
		socksServerCheckbox("cmdConnect", "CONNECT", n.config.commands[protocol.CmdConnect]),
		socksServerCheckbox("cmdBind", "BIND", n.config.commands[protocol.CmdBind]),
		socksServerCheckbox("cmdUDPAssoc", "UDP ASSOC", n.config.commands[protocol.CmdUDPAssoc]),
		socksServerCheckbox("cmdTorResolve", "Tor RESOLVE", n.config.commands[protocol.CmdTorResolve]),
		socksServerCheckbox("cmdTorResolvePtr", "Tor RESOLVE_PTR", n.config.commands[protocol.CmdTorResolvePtr]),
		socksServerCheckbox("cmdGostMuxBind", "Gost MBIND", n.config.commands[protocol.CmdGostMuxBind]),
		socksServerCheckbox("cmdGostUDPTun", "Gost UDPTUN", n.config.commands[protocol.CmdGostUDPTun]),
	}}
}

func (n *localProxyNode) logsBlock() formular.Block {
	return formular.Block{ID: "logs", Order: 20, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLogs, ID: "worker", Label: "Worker", Logs: append([]formular.LogLine(nil), n.logs...)},
	}}
}

func (n *localProxyNode) appendLog(level, text string) {
	n.logs = append(n.logs, formular.LogLine{Level: level, Text: text})
	if len(n.logs) > 60 {
		n.logs = append([]formular.LogLine(nil), n.logs[len(n.logs)-60:]...)
	}
}
