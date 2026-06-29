package almagest

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return udpPingServerClass{engine: e}
	}, true)
	regClass(func(e *engine) pasta.NodeClass {
		return udpPingClientClass{engine: e}
	}, true)
}

const (
	NodeTypeUDPPingServer = "debug.almagest/UDPPingServer"
	NodeTypeUDPPingClient = "debug.almagest/UDPPingClient"

	udpSocketListen = "listen"
	udpSocketDial   = "dial"

	defaultUDPPingNetwork    = "udp4"
	defaultUDPPingHost       = "127.0.0.1"
	defaultUDPPingPort       = 9099
	defaultUDPPingClientPort = 0
	defaultUDPPingPayload    = "ping"
	defaultUDPPingTimeoutMS  = 1000

	udpPingStatusReady   = "ready"
	udpPingStatusRunning = "running..."
	udpPingStatusFail    = "fail"
)

var (
	//go:embed node_debug_udp.md
	udpPingClassLongDescription string
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
		engine: c.engine,
		cfg:    readUDPPingConfig(cfg, true),
	}, nil
}

type udpPingClientClass struct {
	engine *engine
}

func (udpPingClientClass) ClassName() string        { return NodeTypeUDPPingClient }
func (udpPingClientClass) ShortDescription() string { return "DEBUG UDP ping client" }
func (udpPingClientClass) LongDescription() string  { return udpPingClassLongDescription }
func (udpPingClientClass) DefaultNodeParams() pasta.NodeClassParams {
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
func (c udpPingClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &udpPingClientNode{
		engine:     c.engine,
		cfg:        readUDPPingConfig(cfg, false),
		statusText: udpPingStatusReady,
	}, nil
}

type udpPingConfig struct {
	network    string
	socketMode string
	localHost  string
	localPort  int
	remoteHost string
	remotePort int
	payload    string
	timeout    time.Duration
}

type udpPingLog struct {
	level string
	text  string
}

func readUDPPingConfig(cfg configer.Config, server bool) udpPingConfig {
	localPort := defaultUDPPingClientPort
	remotePort := defaultUDPPingPort
	if server {
		localPort = defaultUDPPingPort
		remotePort = defaultUDPPingClientPort
	}
	out := udpPingConfig{
		network:    normalizeUDPPingNetwork(configer.String(cfg, configer.Path{"network"}, defaultUDPPingNetwork)),
		socketMode: normalizeUDPPingSocketMode(configer.String(cfg, configer.Path{"socket_mode"}, udpSocketListen)),
		localHost:  configer.String(cfg, configer.Path{"local_host"}, defaultUDPPingHost),
		localPort:  configer.Int(cfg, configer.Path{"local_port"}, localPort),
		remoteHost: configer.String(cfg, configer.Path{"remote_host"}, defaultUDPPingHost),
		remotePort: configer.Int(cfg, configer.Path{"remote_port"}, remotePort),
		payload:    configer.String(cfg, configer.Path{"payload"}, defaultUDPPingPayload),
		timeout:    time.Duration(configer.Int(cfg, configer.Path{"timeout_ms"}, defaultUDPPingTimeoutMS)) * time.Millisecond,
	}
	out.normalize(server)
	return out
}

func (c *udpPingConfig) normalize(server bool) {
	c.network = normalizeUDPPingNetwork(c.network)
	c.socketMode = normalizeUDPPingSocketMode(c.socketMode)
	if c.localHost == "" {
		c.localHost = hostForUDPNetwork(c.network)
	}
	if c.remoteHost == "" {
		c.remoteHost = hostForUDPNetwork(c.network)
	}
	if c.localPort < 0 || c.localPort > 65535 {
		if server {
			c.localPort = defaultUDPPingPort
		} else {
			c.localPort = defaultUDPPingClientPort
		}
	}
	if c.remotePort < 0 || c.remotePort > 65535 {
		c.remotePort = defaultUDPPingPort
	}
	if c.payload == "" {
		c.payload = defaultUDPPingPayload
	}
	if c.timeout <= 0 {
		c.timeout = defaultUDPPingTimeoutMS * time.Millisecond
	}
}

func normalizeUDPPingNetwork(value string) string {
	switch strings.TrimSpace(value) {
	case "udp", "udp4", "udp6":
		return strings.TrimSpace(value)
	default:
		return defaultUDPPingNetwork
	}
}

func normalizeUDPPingSocketMode(value string) string {
	switch strings.TrimSpace(value) {
	case udpSocketDial:
		return udpSocketDial
	default:
		return udpSocketListen
	}
}

func hostForUDPNetwork(network string) string {
	if network == "udp6" {
		return "::1"
	}
	return defaultUDPPingHost
}

func (c udpPingConfig) localAddr() string {
	return net.JoinHostPort(c.localHost, strconv.Itoa(c.localPort))
}

func (c udpPingConfig) remoteAddr() string {
	return net.JoinHostPort(c.remoteHost, strconv.Itoa(c.remotePort))
}

func (c udpPingConfig) label() string {
	return c.network + "/" + c.socketMode + " " + c.localAddr()
}

type udpPingServerNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w    *pasta.Workspace
	id   uint64
	netp uint64
	cfgp udpPingConfigPorts
	cfg  udpPingConfig
	net  NetworkCloser
	logs []formular.LogLine

	worker    *udpPingServerWorker
	restartID uint64
}

type udpPingConfigPorts struct {
	network    uint64
	socketMode uint64
	localHost  uint64
	localPort  uint64
	remoteHost uint64
	remotePort uint64
	payload    uint64
	timeout    uint64
}

type udpPingServerWorker struct {
	stop chan struct{}
	done chan struct{}
}

type udpPingServerStart struct {
	id  uint64
	cfg udpPingConfig
	net NetworkCloser
}

func (n *udpPingServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.cfg.normalize(true)
	if restored != nil {
		n.restorePorts(restored)
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *udpPingServerNode) restorePorts(restored *pasta.NodeInitData) {
	if len(restored.RightPorts) > 0 {
		n.netp = restored.RightPorts[0]
	}
	if len(restored.LeftPorts) > 0 {
		n.cfgp.network = restored.LeftPorts[0]
	}
	if len(restored.LeftPorts) > 1 {
		n.cfgp.socketMode = restored.LeftPorts[1]
	}
	if len(restored.LeftPorts) > 2 {
		n.cfgp.localHost = restored.LeftPorts[2]
	}
	if len(restored.LeftPorts) > 3 {
		n.cfgp.localPort = restored.LeftPorts[3]
	}
	if len(restored.LeftPorts) > 4 {
		n.cfgp.remoteHost = restored.LeftPorts[4]
	}
	if len(restored.LeftPorts) > 5 {
		n.cfgp.remotePort = restored.LeftPorts[5]
	}
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
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	if n.isConfigPort(port) {
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
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
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil {
			return nil
		}
		if NetworkIsReady(n.net) && SameNetworkSource(n.net, payload.Network) {
			if !SameNetworkInstance(n.net, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.net = payload.Network
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
		close(old.stop)
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
	worker := &udpPingServerWorker{stop: make(chan struct{}), done: make(chan struct{})}
	n.worker = worker
	go func() {
		defer close(worker.done)
		runUDPPingServerWorker(n.engine.ctx, n.logger, n.w, n.id, worker.stop, cfg, netw)
	}()
}

func (n *udpPingServerNode) stopWorker() {
	if n.worker != nil {
		close(n.worker.stop)
		n.worker = nil
	}
}

func runUDPPingServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg udpPingConfig, netw NetworkCloser) {
	conn, err := openUDPConn(ctx, netw, cfg)
	if err != nil {
		logger.Debugf("udp server open failed: %v", err)
		sendUDPPingLog(w, node, formular.LogError, "open failed: "+err.Error())
		return
	}
	defer conn.Close() //nolint:errcheck

	logger.Debugf("udp server listening on %s via %s %s", conn.LocalAddr(), cfg.network, cfg.socketMode)
	sendUDPPingLog(w, node, formular.LogInfo, "listening on "+conn.LocalAddr().String())

	go func() {
		select {
		case <-stop:
			_ = conn.Close()
		case <-ctx.Done():
			_ = conn.Close()
		}
	}()

	buf := make([]byte, 64*1024)
	for {
		n, from, err := readUDP(conn, cfg.socketMode, buf)
		if err != nil {
			if stopped(stop) || errors.Is(err, net.ErrClosed) {
				logger.Debug("udp server stopped")
				return
			}
			logger.Debugf("udp server read failed: %v", err)
			sendUDPPingLog(w, node, formular.LogWarn, "read failed: "+err.Error())
			return
		}
		request := string(buf[:n])
		response := "pong:" + request
		if _, err := writeUDP(conn, cfg.socketMode, []byte(response), from); err != nil {
			if stopped(stop) || errors.Is(err, net.ErrClosed) {
				logger.Debug("udp server stopped")
				return
			}
			logger.Debugf("udp server write failed: %v", err)
			sendUDPPingLog(w, node, formular.LogWarn, "write failed: "+err.Error())
			return
		}
		peer := "<connected>"
		if from != nil {
			peer = from.String()
		}
		logger.Debugf("udp pong to %s: %q", peer, response)
		sendUDPPingLog(w, node, formular.LogInfo, fmt.Sprintf("pong to %s: %q", peer, response))
	}
}

type udpPingClientNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w    *pasta.Workspace
	id   uint64
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

type udpPingClientRequest struct {
	id  uint64
	cfg udpPingConfig
	net NetworkCloser
}

type udpPingClientResult struct {
	id     uint64
	status string
	resp   string
	err    string
}

func (n *udpPingClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.cfg.normalize(false)
	if n.statusText == "" {
		n.statusText = udpPingStatusReady
	}
	if restored != nil {
		n.restorePorts(restored)
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.startWorker()
	n.sendMenuSnapshot()
	return nil
}

func (n *udpPingClientNode) restorePorts(restored *pasta.NodeInitData) {
	if len(restored.RightPorts) > 0 {
		n.netp = restored.RightPorts[0]
	}
	if len(restored.LeftPorts) > 0 {
		n.cfgp.network = restored.LeftPorts[0]
	}
	if len(restored.LeftPorts) > 1 {
		n.cfgp.socketMode = restored.LeftPorts[1]
	}
	if len(restored.LeftPorts) > 2 {
		n.cfgp.localHost = restored.LeftPorts[2]
	}
	if len(restored.LeftPorts) > 3 {
		n.cfgp.localPort = restored.LeftPorts[3]
	}
	if len(restored.LeftPorts) > 4 {
		n.cfgp.remoteHost = restored.LeftPorts[4]
	}
	if len(restored.LeftPorts) > 5 {
		n.cfgp.remotePort = restored.LeftPorts[5]
	}
	if len(restored.LeftPorts) > 6 {
		n.cfgp.payload = restored.LeftPorts[6]
	}
	if len(restored.LeftPorts) > 7 {
		n.cfgp.timeout = restored.LeftPorts[7]
	}
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
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	if n.isConfigPort(port) {
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *udpPingClientNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.netp || n.isConfigPort(port) {
		std.Request(n.w, n.id, link)
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
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil {
			return nil
		}
		if NetworkIsReady(n.net) && SameNetworkSource(n.net, payload.Network) {
			if !SameNetworkInstance(n.net, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.net = payload.Network
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
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
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
		if msg.MenuID == pasta.NodeMenuID(n.id) && msg.BlockID == "config" && msg.ButtonID == "send" {
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
		n.logger.Debug("udp ping ignored: already pending")
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
		n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: udpPingClientResult{id: req.id, err: "worker is busy"}})
	}
}

func (n *udpPingClientNode) startWorker() {
	if n.jobs != nil || n.stop != nil {
		return
	}
	n.jobs = make(chan udpPingClientRequest, 1)
	n.stop = make(chan struct{})
	go runUDPPingClientWorker(n.engine.ctx, n.logger, n.w, n.id, n.jobs, n.stop)
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

func runUDPPingClientWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, jobs <-chan udpPingClientRequest, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case req := <-jobs:
			result := doUDPPing(ctx, req)
			if result.err != "" {
				logger.Debugf("udp ping %d failed: %s", req.id, result.err)
				sendUDPPingLog(w, node, formular.LogWarn, "ping failed: "+result.err)
			} else {
				logger.Debugf("udp ping %d complete: %s", req.id, result.resp)
				sendUDPPingLog(w, node, formular.LogInfo, "ping complete: "+result.resp)
			}
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: result})
		}
	}
}

func doUDPPing(parent context.Context, req udpPingClientRequest) udpPingClientResult {
	if req.net == nil {
		return udpPingClientResult{id: req.id, err: "network is not ready"}
	}
	ctx, cancel := context.WithTimeout(parent, req.cfg.timeout)
	defer cancel()
	conn, err := openUDPConn(ctx, req.net, req.cfg)
	if err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	defer conn.Close() //nolint:errcheck

	if err := conn.SetDeadline(time.Now().Add(req.cfg.timeout)); err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	if _, err := writeUDP(conn, req.cfg.socketMode, []byte(req.cfg.payload), udpAddr(req.cfg.network, req.cfg.remoteAddr())); err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	buf := make([]byte, 64*1024)
	n, _, err := readUDP(conn, req.cfg.socketMode, buf)
	if err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	resp := string(buf[:n])
	want := "pong:" + req.cfg.payload
	if resp != want {
		return udpPingClientResult{id: req.id, resp: resp, err: fmt.Sprintf("unexpected response %q, want %q", resp, want)}
	}
	return udpPingClientResult{id: req.id, status: "ok", resp: resp}
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

type udpPacketConn interface {
	gonnect.UDPConn
}

func openUDPConn(ctx context.Context, netw NetworkCloser, cfg udpPingConfig) (udpPacketConn, error) {
	if cfg.socketMode == udpSocketDial {
		if cfg.remotePort == 0 {
			return nil, errors.New("remote port is required for dial socket mode")
		}
		return netw.DialUDP(ctx, cfg.network, cfg.localAddr(), cfg.remoteAddr())
	}
	return netw.ListenUDP(ctx, cfg.network, cfg.localAddr())
}

func readUDP(conn udpPacketConn, socketMode string, buf []byte) (int, net.Addr, error) {
	if socketMode == udpSocketDial {
		n, err := conn.Read(buf)
		return n, conn.RemoteAddr(), err
	}
	return conn.ReadFrom(buf)
}

func writeUDP(conn udpPacketConn, socketMode string, payload []byte, addr net.Addr) (int, error) {
	if socketMode == udpSocketDial {
		return conn.Write(payload)
	}
	if addr == nil {
		return 0, errors.New("remote address is required for unconnected socket mode")
	}
	return conn.WriteTo(payload, addr)
}

func udpAddr(network, address string) net.Addr {
	addr, err := net.ResolveUDPAddr(network, address)
	if err != nil {
		return &gonnect.NetAddr{Net: network, Addr: address}
	}
	return addr
}

func sendUDPPingLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: udpPingLog{level: level, text: text}})
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func (n *udpPingServerNode) requestConfig() {
	n.requestPort(n.netp)
	for _, port := range n.configPorts() {
		n.requestPort(port)
	}
}

func (n *udpPingClientNode) requestConfig() {
	n.requestPort(n.netp)
	for _, port := range n.configPorts() {
		n.requestPort(port)
	}
}

func (n *udpPingServerNode) requestPort(port uint64) {
	requestPortValue(n.w, n.id, port)
}

func (n *udpPingClientNode) requestPort(port uint64) {
	requestPortValue(n.w, n.id, port)
}

func requestPortValue(w *pasta.Workspace, node, port uint64) {
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(w, node, link)
	}
}

func (n *udpPingServerNode) configPorts() []uint64 {
	return []uint64{n.cfgp.network, n.cfgp.socketMode, n.cfgp.localHost, n.cfgp.localPort, n.cfgp.remoteHost, n.cfgp.remotePort}
}

func (n *udpPingClientNode) configPorts() []uint64 {
	return []uint64{n.cfgp.network, n.cfgp.socketMode, n.cfgp.localHost, n.cfgp.localPort, n.cfgp.remoteHost, n.cfgp.remotePort, n.cfgp.payload, n.cfgp.timeout}
}

func (n *udpPingServerNode) isConfigPort(port uint64) bool {
	for _, candidate := range n.configPorts() {
		if port == candidate {
			return true
		}
	}
	return false
}

func (n *udpPingClientNode) isConfigPort(port uint64) bool {
	for _, candidate := range n.configPorts() {
		if port == candidate {
			return true
		}
	}
	return false
}

func (n *udpPingServerNode) applyConfigString(port uint64, value string) bool {
	return applyUDPPingConfigString(&n.cfg, n.cfgp, port, value, true)
}

func (n *udpPingClientNode) applyConfigString(port uint64, value string) bool {
	return applyUDPPingConfigString(&n.cfg, n.cfgp, port, value, false)
}

func applyUDPPingConfigString(cfg *udpPingConfig, ports udpPingConfigPorts, port uint64, value string, server bool) bool {
	before := *cfg
	switch port {
	case ports.network:
		cfg.network = value
	case ports.socketMode:
		cfg.socketMode = value
	case ports.localHost:
		cfg.localHost = value
	case ports.localPort:
		cfg.localPort = parsePort(value, cfg.localPort)
	case ports.remoteHost:
		cfg.remoteHost = value
	case ports.remotePort:
		cfg.remotePort = parsePort(value, cfg.remotePort)
	case ports.payload:
		cfg.payload = value
	case ports.timeout:
		ms := parsePort(value, int(cfg.timeout/time.Millisecond))
		if ms > 0 {
			cfg.timeout = time.Duration(ms) * time.Millisecond
		}
	}
	cfg.normalize(server)
	return before != *cfg
}

func applyUDPPingForm(cfg *udpPingConfig, values map[string]any, server bool) bool {
	before := *cfg
	if value, ok := std.StringFromPayload(values["network"]); ok {
		cfg.network = value
	}
	if value, ok := std.StringFromPayload(values["socket_mode"]); ok {
		cfg.socketMode = value
	}
	if value, ok := std.StringFromPayload(values["local_host"]); ok {
		cfg.localHost = value
	}
	if value, ok := std.IntFromPayload(values["local_port"]); ok {
		cfg.localPort = value
	}
	if value, ok := std.StringFromPayload(values["remote_host"]); ok {
		cfg.remoteHost = value
	}
	if value, ok := std.IntFromPayload(values["remote_port"]); ok {
		cfg.remotePort = value
	}
	if value, ok := std.StringFromPayload(values["payload"]); ok {
		cfg.payload = value
	}
	if value, ok := std.IntFromPayload(values["timeout_ms"]); ok && value > 0 {
		cfg.timeout = time.Duration(value) * time.Millisecond
	}
	cfg.normalize(server)
	return before != *cfg
}

func saveUDPPingConfig(cfg configer.Config, value udpPingConfig) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"network":     value.network,
		"socket_mode": value.socketMode,
		"local_host":  value.localHost,
		"local_port":  value.localPort,
		"remote_host": value.remoteHost,
		"remote_port": value.remotePort,
		"payload":     value.payload,
		"timeout_ms":  int(value.timeout / time.Millisecond),
	})
}

func (n *udpPingServerNode) updateLabel() error {
	return n.w.SetNodeLabel(n.id, n.cfg.label())
}

func (n *udpPingClientNode) updateLabel() error {
	return n.w.SetNodeLabel(n.id, n.statusText)
}

func (n *udpPingServerNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshot(pasta.NodeMenuID(n.id), 1, n.configBlock(), n.logsBlock()))
}

func (n *udpPingClientNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshot(pasta.NodeMenuID(n.id), 1, n.configBlock(), n.resultBlock(), n.logsBlock()))
}

func (n *udpPingServerNode) sendConfigBlock() {
	sendUDPPingBlock(n.w, n.id, n.configBlock())
}

func (n *udpPingClientNode) sendConfigBlock() {
	sendUDPPingBlock(n.w, n.id, n.configBlock())
}

func (n *udpPingClientNode) sendResultBlock() {
	sendUDPPingBlock(n.w, n.id, n.resultBlock())
}

func (n *udpPingServerNode) sendLogsBlock() {
	sendUDPPingBlock(n.w, n.id, n.logsBlock())
}

func (n *udpPingClientNode) sendLogsBlock() {
	sendUDPPingBlock(n.w, n.id, n.logsBlock())
}

func sendUDPPingBlock(w *pasta.Workspace, node uint64, block formular.Block) {
	if w == nil || node == 0 {
		return
	}
	w.SendNodeMenuMsg(node, formular.BlockSnapshot(pasta.NodeMenuID(node), 1, block.Generation, block))
}

func (n *udpPingServerNode) configBlock() formular.Block {
	return udpPingConfigBlock(n.cfg, n.configReadonly(), false, false)
}

func (n *udpPingClientNode) configBlock() formular.Block {
	block := udpPingConfigBlock(n.cfg, n.configReadonly(), true, n.pending)
	return block
}

func udpPingConfigBlock(cfg udpPingConfig, readonly, client, pending bool) formular.Block {
	items := []formular.Item{
		udpPingField("network", "Network", formular.FieldRadio, cfg.network, readonly, "udp4", "udp6"),
		udpPingField("socket_mode", "Socket mode", formular.FieldRadio, cfg.socketMode, readonly, udpSocketListen, udpSocketDial),
		udpPingField("local_host", "Local host", formular.FieldText, cfg.localHost, readonly),
		udpPingField("local_port", "Local port", formular.FieldInt, cfg.localPort, readonly),
		udpPingField("remote_host", "Remote host", formular.FieldText, cfg.remoteHost, readonly),
		udpPingField("remote_port", "Remote port", formular.FieldInt, cfg.remotePort, readonly),
	}
	if client {
		items = append(items,
			udpPingField("payload", "Payload", formular.FieldText, cfg.payload, readonly),
			udpPingField("timeout_ms", "Timeout ms", formular.FieldInt, int(cfg.timeout/time.Millisecond), readonly),
			formular.Item{Type: formular.ItemButton, ID: "send", Label: "Send ping", Inactive: pending},
		)
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: items}
}

func udpPingField(id, label, kind string, value any, readonly bool, allowed ...any) formular.Item {
	return formular.Item{
		Type:  formular.ItemField,
		ID:    id,
		Label: label,
		Field: &formular.Field{
			Kind:          kind,
			Value:         value,
			Readonly:      readonly,
			AllowedValues: allowed,
		},
	}
}

func (n *udpPingClientNode) resultBlock() formular.Block {
	return formular.Block{ID: "result", Order: 20, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLabel, ID: "status", Text: "Status: " + n.statusText, Format: formular.TextPlain},
		{
			Type:  formular.ItemField,
			ID:    "error",
			Label: "Error",
			Field: &formular.Field{Kind: formular.FieldText, Value: n.errText, Readonly: true},
		},
		{
			Type:  formular.ItemField,
			ID:    "response",
			Label: "Response",
			Field: &formular.Field{Kind: formular.FieldText, Value: n.resp, Readonly: true},
		},
	}}
}

func (n *udpPingServerNode) logsBlock() formular.Block {
	return udpPingLogsBlock(n.logs)
}

func (n *udpPingClientNode) logsBlock() formular.Block {
	return udpPingLogsBlock(n.logs)
}

func udpPingLogsBlock(logs []formular.LogLine) formular.Block {
	return formular.Block{ID: "logs", Order: 30, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLogs, ID: "worker", Label: "Worker", Logs: append([]formular.LogLine(nil), logs...)},
	}}
}

func (n *udpPingServerNode) log(level, text string) {
	n.appendLog(level, text)
	n.sendLogsBlock()
}

func (n *udpPingServerNode) appendLog(level, text string) {
	n.logs = appendUDPPingLog(n.logs, level, text)
}

func (n *udpPingClientNode) appendLog(level, text string) {
	n.logs = appendUDPPingLog(n.logs, level, text)
}

func appendUDPPingLog(logs []formular.LogLine, level, text string) []formular.LogLine {
	logs = append(logs, formular.LogLine{Level: level, Text: text})
	if len(logs) > 40 {
		logs = append([]formular.LogLine(nil), logs[len(logs)-40:]...)
	}
	return logs
}

func (n *udpPingServerNode) configReadonly() bool {
	for _, port := range n.configPorts() {
		if n.portHasLinks(port) {
			return true
		}
	}
	return false
}

func (n *udpPingClientNode) configReadonly() bool {
	for _, port := range n.configPorts() {
		if n.portHasLinks(port) {
			return true
		}
	}
	return false
}

func (n *udpPingServerNode) portHasLinks(port uint64) bool {
	return udpPingPortHasLinks(n.w, port)
}

func (n *udpPingClientNode) portHasLinks(port uint64) bool {
	return udpPingPortHasLinks(n.w, port)
}

func udpPingPortHasLinks(w *pasta.Workspace, port uint64) bool {
	if w == nil || port == 0 {
		return false
	}
	snapshot, ok := w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}
