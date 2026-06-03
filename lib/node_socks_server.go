package lib

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
	"github.com/coder/websocket"
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

	socksServerRestartDelay = time.Second
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
	return &socksServerNode{engine: c.engine, config: config}, nil
}

// socksServerNode accepts SOCKS client connections on the listen network and
// executes client requested proxy operations on the proxy network.
//
// The node owns two right-directed network ports. The listen port is required
// to start the background worker. The proxy port is optional; when it is absent
// socksgo receives a rejecting network so clients get protocol-level failures
// instead of failing to connect to the SOCKS server itself.
type socksServerNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w      *pasta.Workspace
	id     uint64
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

type socksServerConfig struct {
	socksHost string
	socksPort int
	wsHost    string
	wsPort    int
	wsPath    string
	users     []socksServerUser
	commands  map[protocol.Cmd]bool
}

type socksServerUser struct {
	id       string
	username string
	password string
}

type socksServerWorker struct {
	stop chan struct{}
	done chan struct{}
}

type socksServerLog struct {
	level string
	text  string
}

type socksServerStart struct {
	id  uint64
	cfg socksServerWorkerConfig
}

type socksServerWorkerConfig struct {
	config        socksServerConfig
	listenNetwork NetworkCloser
	proxyNetwork  gonnect.Network
}

func defaultSocksServerConfig() socksServerConfig {
	return socksServerConfig{
		socksHost: DefaultSocksServerHost,
		socksPort: DefaultSocksServerPort,
		wsPath:    DefaultSocksServerWSPath,
		commands:  defaultSocksServerCommands(),
	}
}

func defaultSocksServerCommands() map[protocol.Cmd]bool {
	return map[protocol.Cmd]bool{
		protocol.CmdConnect:       true,
		protocol.CmdBind:          true,
		protocol.CmdUDPAssoc:      true,
		protocol.CmdTorResolve:    true,
		protocol.CmdTorResolvePtr: true,
		protocol.CmdGostMuxBind:   true,
		protocol.CmdGostUDPTun:    true,
	}
}

func (c *socksServerConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.socksHost = readConfigString(cfg, "socksHost", c.socksHost)
	c.socksPort = readConfigInt(cfg, "socksPort", c.socksPort)
	c.wsHost = readConfigString(cfg, "wsHost", c.wsHost)
	c.wsPort = readConfigInt(cfg, "wsPort", c.wsPort)
	c.wsPath = normalizeSocksServerPath(readConfigString(cfg, "wsPath", c.wsPath))
	if c.wsPath == "" {
		c.wsPath = DefaultSocksServerWSPath
	}
	if users, ok := readSocksServerUsers(cfg); ok {
		c.users = users
	}
	if commands, ok := readSocksServerCommands(cfg); ok {
		c.commands = commands
	}
}

func (n *socksServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
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

	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		n.logger.Errf("failed to set node primary type: %v", err)
		return err
	}

	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *socksServerNode) OnReady() error {
	n.requestNetwork(n.listen)
	n.requestNetwork(n.proxy)
	n.restartWorker()
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
		if n.portHasLinks(port) {
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
		n.restartWorker()
	case n.proxy:
		n.proxyNetwork = nil
		n.restartWorker()
	}
	return nil
}

func (n *socksServerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok || payload.Network == nil {
		return nil
	}

	switch event.ReceiverPort {
	case n.listen:
		if NetworkIsReady(n.listenNetwork) && SameNetworkSource(n.listenNetwork, payload.Network) {
			if !SameNetworkInstance(n.listenNetwork, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.listenNetwork = payload.Network
		n.restartWorker()
	case n.proxy:
		if NetworkIsReady(n.proxyNetwork) && SameNetworkSource(n.proxyNetwork, payload.Network) {
			if !SameNetworkInstance(n.proxyNetwork, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.proxyNetwork = payload.Network
		n.restartWorker()
	}
	return nil
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

func (n *socksServerNode) applyConfigForm(values map[string]any) error {
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
	n.sendConfigBlock()
	n.restartWorker()
	return nil
}

func (n *socksServerNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
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

func (n *socksServerNode) restartWorker() {
	n.restartID++
	id := n.restartID
	n.logger.Debug("socks worker restarting")
	n.appendLog(formular.LogDebug, "worker restarting")
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		n.worker = nil
		close(old.stop)
	}

	if n.listenNetwork == nil {
		n.logger.Debug("socks worker waiting for listen network")
		n.appendLog(formular.LogWarn, "waiting for listen network")
		n.sendLogsBlock()
		return
	}

	proxy := gonnect.Network(&gonnect.RejectNetwork{})
	if n.proxyNetwork != nil {
		proxy = n.proxyNetwork
	}
	cfg := socksServerWorkerConfig{
		config:        n.config,
		listenNetwork: n.listenNetwork,
		proxyNetwork:  proxy,
	}
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: socksServerStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

func (n *socksServerNode) startWorker(cfg socksServerWorkerConfig) {
	stop := make(chan struct{})
	worker := &socksServerWorker{stop: stop, done: make(chan struct{})}
	n.worker = worker
	n.logger.Debug("socks worker starting")
	go func() {
		defer close(worker.done)
		runSocksServerWorker(n.engine.ctx, n.logger, n.w, n.id, stop, cfg)
	}()
}

func (n *socksServerNode) stopWorker() {
	if n.worker != nil {
		n.logger.Debug("socks worker stopping")
		close(n.worker.stop)
		n.worker = nil
	}
}

func runSocksServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg socksServerWorkerConfig) {
	for {
		select {
		case <-stop:
			logger.Debug("socks worker stopped")
			return
		default:
		}

		if !cfg.config.hasListener() {
			sendSocksServerLog(w, node, formular.LogWarn, "no SOCKS listener configured")
			if !sleepOrStopped(stop, socksServerRestartDelay) {
				logger.Debug("socks worker stopped")
				return
			}
			continue
		}

		err := runSocksServerOnce(ctx, logger, w, node, stop, cfg)
		if err != nil {
			logger.Debugf("socks server stopped: %v", err)
			sendSocksServerLog(w, node, formular.LogDebug, "server stopped: "+err.Error())
		}
		if !sleepOrStopped(stop, socksServerRestartDelay) {
			logger.Debug("socks worker stopped")
			return
		}
	}
}

func runSocksServerOnce(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg socksServerWorkerConfig) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	server := buildSocksGoServer(logger, cfg)
	errs := make(chan error, 2)
	started := 0
	closers := make([]func(), 0, 2)

	if cfg.config.socksPort > 0 {
		addr := net.JoinHostPort(hostOrDefault(cfg.config.socksHost), strconv.Itoa(cfg.config.socksPort))
		ln, err := cfg.listenNetwork.Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen failed on %s: %w", addr, err)
		}
		closers = append(closers, func() { _ = ln.Close() })
		started++
		logger.Debugf("socks listening on %s", addr)
		sendSocksServerLog(w, node, formular.LogInfo, "socks listening on "+addr)
		go serveSocksListener(ctx, logger, server, ln, errs)
	}

	if cfg.config.wsPort > 0 {
		addr := net.JoinHostPort(hostOrDefault(cfg.config.wsHost), strconv.Itoa(cfg.config.wsPort))
		ln, err := cfg.listenNetwork.Listen(ctx, "tcp", addr)
		if err != nil {
			for _, closeFn := range closers {
				closeFn()
			}
			return fmt.Errorf("socks+ws listen failed on %s: %w", addr, err)
		}
		httpServer := &http.Server{
			Handler:           socksWebSocketHandler(ctx, logger, server, cfg.config.wsPath),
			ReadHeaderTimeout: 5 * time.Second,
		}
		closers = append(closers, func() {
			_ = ln.Close()
			_ = httpServer.Shutdown(context.Background())
		})
		started++
		logger.Debugf("socks+ws listening on %s%s", addr, cfg.config.wsPath)
		sendSocksServerLog(w, node, formular.LogInfo, "socks+ws listening on "+addr+cfg.config.wsPath)
		go func() {
			err := httpServer.Serve(ln)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}

	if started == 0 {
		return errors.New("no SOCKS listener configured")
	}

	select {
	case <-stop:
		for _, closeFn := range closers {
			closeFn()
		}
		logger.Debug("socks worker stopped")
		return nil
	case <-ctx.Done():
		for _, closeFn := range closers {
			closeFn()
		}
		return ctx.Err()
	case err := <-errs:
		for _, closeFn := range closers {
			closeFn()
		}
		return err
	}
}

func buildSocksGoServer(logger pasta.Logger, cfg socksServerWorkerConfig) *socksgo.Server {
	listen := cfg.listenNetwork
	proxy := cfg.proxyNetwork
	server := &socksgo.Server{
		Auth:           socksServerAuth(cfg.config.users),
		Handlers:       socksServerHandlers(cfg.config.commands),
		Dialer:         proxy.Dial,
		PacketDialer:   proxy.PacketDial,
		Listener:       proxy.Listen,
		PacketListener: proxy.ListenPacket,
		AssocListener: func(ctx context.Context, ctrl net.Conn) (gonnect.PacketConn, error) {
			return listenSocksUDPAssoc(ctx, listen, ctrl)
		},
		Resolver: proxy,
		PreCmd: func(_ context.Context, conn net.Conn, ver string, info protocol.AuthInfo, cmd protocol.Cmd, addr protocol.Addr) (protocol.ReplyStatus, error) {
			logger.Debugf("socks %s %s %s auth=%s", ver, cmd.String(), addr.String(), socksAuthSummary(info))
			return 0, nil
		},
		HandshakeTimeout: 10 * time.Second,
	}
	return server
}

func listenSocksUDPAssoc(ctx context.Context, network gonnect.Network, ctrl net.Conn) (gonnect.PacketConn, error) {
	la := ctrl.LocalAddr()
	if la == nil {
		return nil, errors.New("failed to guess default local addr for incoming UDP assoc connections")
	}
	host := la.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return network.ListenPacket(ctx, "udp", net.JoinHostPort(host, "0"))
}

func serveSocksListener(ctx context.Context, logger pasta.Logger, server *socksgo.Server, ln net.Listener, errs chan<- error) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				errs <- err
			}
			return
		}
		logger.Debugf("socks client connected from %s", conn.RemoteAddr())
		go func() {
			err := server.Accept(ctx, conn, false)
			if err != nil {
				logger.Debugf("socks client %s closed: %v", conn.RemoteAddr(), err)
			} else {
				logger.Debugf("socks client %s closed", conn.RemoteAddr())
			}
		}()
	}
}

func socksWebSocketHandler(ctx context.Context, logger pasta.Logger, server *socksgo.Server, path string) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(rw, r)
			return
		}
		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			logger.Debugf("socks+ws upgrade failed from %s: %v", r.RemoteAddr, err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusInternalError, "") }()
		logger.Debugf("socks+ws client connected from %s", r.RemoteAddr)
		if err := server.AcceptWS(ctx, conn, r.TLS != nil); err != nil {
			logger.Debugf("socks+ws client %s closed: %v", r.RemoteAddr, err)
		} else {
			logger.Debugf("socks+ws client %s closed", r.RemoteAddr)
		}
	})
}

func sendSocksServerLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: socksServerLog{level: level, text: text}})
}

func socksServerAuth(users []socksServerUser) *protocol.AuthHandlers {
	if len(users) == 0 {
		return nil
	}
	allowed := make(map[string]string, len(users))
	for _, user := range users {
		allowed[user.username] = user.password
	}
	return (&protocol.AuthHandlers{}).Add(&protocol.PassAuthHandler{VerifyFn: func(user, pass string) bool {
		want, ok := allowed[user]
		return ok && want == pass
	}})
}

func socksServerHandlers(enabled map[protocol.Cmd]bool) map[protocol.Cmd]socksgo.CommandHandler {
	handlers := make(map[protocol.Cmd]socksgo.CommandHandler)
	for cmd, handler := range socksgo.DefaultCommandHandlers {
		if enabled[cmd] {
			handlers[cmd] = handler
		}
	}
	return handlers
}

func (n *socksServerNode) requestNetwork(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *socksServerNode) updateLabel() error {
	if n.config.socksPort > 0 {
		return n.w.SetNodeLabel(n.id, net.JoinHostPort(hostOrDefault(n.config.socksHost), strconv.Itoa(n.config.socksPort)))
	}
	if n.config.wsPort > 0 {
		return n.w.SetNodeLabel(n.id, "ws "+net.JoinHostPort(hostOrDefault(n.config.wsHost), strconv.Itoa(n.config.wsPort)))
	}
	return n.w.SetNodeLabel(n.id, "disabled")
}

func (n *socksServerNode) portHasLinks(port uint64) bool {
	if n.w == nil || port == 0 {
		return false
	}
	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

func (n *socksServerNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock(), n.logsBlock()},
	})
}

func (n *socksServerNode) sendConfigBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.configBlock(),
	})
}

func (n *socksServerNode) sendLogsBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.logsBlock(),
	})
}

func (n *socksServerNode) configBlock() formular.Block {
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

func (n *socksServerNode) logsBlock() formular.Block {
	return formular.Block{ID: "logs", Order: 20, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLogs, ID: "worker", Label: "Worker", Logs: append([]formular.LogLine(nil), n.logs...)},
	}}
}

func (n *socksServerNode) appendLog(level, text string) {
	n.logs = append(n.logs, formular.LogLine{Level: level, Text: text})
	if len(n.logs) > 60 {
		n.logs = append([]formular.LogLine(nil), n.logs[len(n.logs)-60:]...)
	}
}

func socksServerTextField(id, label, value string, statusErr bool, statusText string) formular.Item {
	field := &formular.Field{Kind: formular.FieldText, Value: value, Validation: true}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = statusText
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func socksServerIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	field := &formular.Field{Kind: formular.FieldInt, Value: value, Validation: true}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = statusText
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func socksServerCheckbox(id, label string, value bool) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{Kind: formular.FieldCheckbox, Value: value}}
}

func socksServerUsersField(users []socksServerUser) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: "users", Label: "Users", Field: &formular.Field{
		Kind: formular.FieldArray,
		Templates: []formular.ArrayTemplate{{
			Name:  "user",
			Label: "User",
			Items: []formular.Item{
				{Type: formular.ItemField, ID: "username", Label: "Username", Field: &formular.Field{Kind: formular.FieldText}},
				{Type: formular.ItemField, ID: "password", Label: "Password", Field: &formular.Field{Kind: formular.FieldText, Secret: true}},
			},
		}},
		Elements: socksServerUserElements(users),
	}}
}

func socksServerUserElements(users []socksServerUser) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(users))
	for _, user := range users {
		out = append(out, formular.ArrayElement{
			ID:       user.id,
			Template: "user",
			Items: []formular.Item{
				{Type: formular.ItemField, ID: "username", Label: "Username", Field: &formular.Field{Kind: formular.FieldText, Value: user.username}},
				{Type: formular.ItemField, ID: "password", Label: "Password", Field: &formular.Field{Kind: formular.FieldText, Value: user.password, Secret: true}},
			},
		})
	}
	return out
}

func (c socksServerConfig) validate() error {
	if c.socksPort < 0 || c.socksPort > 65535 {
		return errors.New("SOCKS port must be between 0 and 65535")
	}
	if c.wsPort < 0 || c.wsPort > 65535 {
		return errors.New("SOCKS+WS port must be between 0 and 65535")
	}
	if c.sameListenAddress() {
		return errors.New("SOCKS and SOCKS+WS listeners must not use the same host and port")
	}
	return nil
}

func (c socksServerConfig) sameListenAddress() bool {
	return c.socksPort > 0 && c.wsPort > 0 && c.socksPort == c.wsPort && hostOrDefault(c.socksHost) == hostOrDefault(c.wsHost)
}

func (c socksServerConfig) hasListener() bool {
	return c.socksPort > 0 || c.wsPort > 0
}

func socksServerCommandFields() map[protocol.Cmd]string {
	return map[protocol.Cmd]string{
		protocol.CmdConnect:       "cmdConnect",
		protocol.CmdBind:          "cmdBind",
		protocol.CmdUDPAssoc:      "cmdUDPAssoc",
		protocol.CmdTorResolve:    "cmdTorResolve",
		protocol.CmdTorResolvePtr: "cmdTorResolvePtr",
		protocol.CmdGostMuxBind:   "cmdGostMuxBind",
		protocol.CmdGostUDPTun:    "cmdGostUDPTun",
	}
}

func parseSocksServerUsers(raw any) ([]socksServerUser, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return socksServerUsersFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := arrayElementValueFromAny(rawElement)
			if !ok {
				continue
			}
			array = append(array, element)
		}
		return socksServerUsersFromArray(array), true
	default:
		return nil, false
	}
}

func socksServerUsersFromArray(values []formular.ArrayElementValue) []socksServerUser {
	out := make([]socksServerUser, 0, len(values))
	seen := map[string]struct{}{}
	for i, value := range values {
		if value.Template != "" && value.Template != "user" {
			continue
		}
		username, _ := stringFromAny(value.Values["username"])
		password, _ := stringFromAny(value.Values["password"])
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		id := value.ID
		if id == "" {
			id = fmt.Sprintf("user-%d", i+1)
		}
		if _, ok := seen[id]; ok {
			id = fmt.Sprintf("%s-%d", id, i+1)
		}
		seen[id] = struct{}{}
		out = append(out, socksServerUser{id: id, username: username, password: password})
	}
	return out
}

func arrayElementValueFromAny(raw any) (formular.ArrayElementValue, bool) {
	switch value := raw.(type) {
	case formular.ArrayElementValue:
		return value, true
	case map[string]any:
		out := formular.ArrayElementValue{Values: map[string]any{}}
		out.ID, _ = stringFromAny(value["id"])
		out.Template, _ = stringFromAny(value["template"])
		if values, ok := value["values"].(map[string]any); ok {
			out.Values = values
		}
		return out, true
	default:
		return formular.ArrayElementValue{}, false
	}
}

func socksServerUsersConfig(users []socksServerUser) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		out = append(out, map[string]any{"id": user.id, "username": user.username, "password": user.password})
	}
	return out
}

func readSocksServerUsers(cfg configer.Config) ([]socksServerUser, bool) {
	raw, err := cfg.Get(configer.Path{"users"})
	if err != nil {
		return nil, false
	}
	switch values := raw.(type) {
	case []map[string]any:
		out := make([]socksServerUser, 0, len(values))
		for i, value := range values {
			out = append(out, socksServerUserFromConfigMap(i, value))
		}
		return compactSocksServerUsers(out), true
	case []any:
		out := make([]socksServerUser, 0, len(values))
		for i, rawValue := range values {
			if value, ok := rawValue.(map[string]any); ok {
				out = append(out, socksServerUserFromConfigMap(i, value))
			}
		}
		return compactSocksServerUsers(out), true
	default:
		return nil, false
	}
}

func socksServerUserFromConfigMap(index int, value map[string]any) socksServerUser {
	id, _ := stringFromAny(value["id"])
	username, _ := stringFromAny(value["username"])
	password, _ := stringFromAny(value["password"])
	if id == "" {
		id = fmt.Sprintf("user-%d", index+1)
	}
	return socksServerUser{id: id, username: strings.TrimSpace(username), password: password}
}

func compactSocksServerUsers(users []socksServerUser) []socksServerUser {
	out := users[:0]
	for _, user := range users {
		if user.username != "" {
			out = append(out, user)
		}
	}
	return out
}

func socksServerCommandsConfig(commands map[protocol.Cmd]bool) map[string]bool {
	out := make(map[string]bool, len(commands))
	for cmd, field := range socksServerCommandFields() {
		out[field] = commands[cmd]
	}
	return out
}

func readSocksServerCommands(cfg configer.Config) (map[protocol.Cmd]bool, bool) {
	raw, err := cfg.Get(configer.Path{"commands"})
	if err != nil {
		return nil, false
	}
	values, ok := raw.(map[string]any)
	if ok {
		out := defaultSocksServerCommands()
		for cmd, field := range socksServerCommandFields() {
			if value, ok := boolFromAny(values[field]); ok {
				out[cmd] = value
			}
		}
		return out, true
	}
	boolValues, ok := raw.(map[string]bool)
	if !ok {
		return nil, false
	}
	out := defaultSocksServerCommands()
	for cmd, field := range socksServerCommandFields() {
		if value, ok := boolValues[field]; ok {
			out[cmd] = value
		}
	}
	return out, true
}

func normalizeSocksServerPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return value
}

func hostOrDefault(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return DefaultSocksServerHost
	}
	return host
}

func validPort(value int) int {
	if value < 0 || value > 65535 {
		return 0
	}
	return value
}

func stringFromAny(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	default:
		return "", false
	}
}

func intFromAny(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func boolFromAny(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	default:
		return false, false
	}
}

func socksAuthSummary(info protocol.AuthInfo) string {
	switch info.Code {
	case protocol.NoAuthCode:
		return "none"
	case protocol.PassAuthCode:
		if user, ok := info.Info["user"].(string); ok {
			return "user:" + user
		}
		return "userpass"
	default:
		return strconv.Itoa(int(info.Code))
	}
}
