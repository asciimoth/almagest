package lib

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

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

type httpServerClass struct {
	engine *engine
}

func (httpServerClass) ClassName() string        { return NodeTypeHTTPServer }
func (httpServerClass) ShortDescription() string { return "Debug HTTP server" }
func (httpServerClass) LongDescription() string {
	return "Runs a HTTP server over network and responds to every request with the configured string. Use for debug/tcp conncectiviy tests/etc"
}
func (httpServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "Response", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Host", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Port", Types: []string{std.TypeString}},
	}}
}
func (c httpServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &httpServerNode{
		host:     readConfigString(cfg, "host", DefaultHTTPServerHost),
		port:     readConfigInt(cfg, "port", DefaultHTTPServerPort),
		response: readConfigString(cfg, "response", ""),

		engine: c.engine,
	}, nil
}

type httpServerNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w     *pasta.Workspace
	id    uint64
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

type httpServerLog struct {
	level string
	text  string
}

type httpServerStart struct {
	id  uint64
	cfg httpServerConfig
}

type httpServerWorker struct {
	stop chan struct{}
	done chan struct{}
}

type httpServerConfig struct {
	host     string
	port     int
	response string
	network  NetworkCloser
}

func (n *httpServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger

	if n.host == "" {
		n.host = DefaultHTTPServerHost
	}
	if n.port == 0 {
		n.port = DefaultHTTPServerPort
	}

	if restored != nil {
		if len(restored.RightPorts) > 0 {
			n.netp = restored.RightPorts[0]
		}
		if len(restored.LeftPorts) > 0 {
			n.in = restored.LeftPorts[0]
		}
		if len(restored.LeftPorts) > 1 {
			n.hostp = restored.LeftPorts[1]
		}
		if len(restored.LeftPorts) > 2 {
			n.portp = restored.LeftPorts[2]
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
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}

	case n.in:
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}

	case n.hostp, n.portp:
		if portDirection != "left" || linkType != std.TypeString {
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

func (n *httpServerNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.netp, n.in:
		std.Request(n.w, n.id, link)

	case n.hostp, n.portp:
		n.sendListenBlock()
		std.Request(n.w, n.id, link)
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
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil {
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.network = payload.Network
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
	if !ok || msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "listen" {
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

func (n *httpServerNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	if err := cfg.Set(configer.Path{"host"}, n.host); err != nil {
		return err
	}
	if err := cfg.Set(configer.Path{"port"}, n.port); err != nil {
		return err
	}
	return cfg.Set(configer.Path{"response"}, n.response)
}

func (n *httpServerNode) restartWorker() {
	n.restartID++
	id := n.restartID
	n.logger.Debug("worker restarting")
	n.appendLog(formular.LogInfo, "worker restarting")
	n.sendLogsBlock()
	old := n.worker
	if old != nil {
		n.worker = nil
		close(old.stop)
	}
	if n.network == nil {
		n.logger.Debug("waiting for network")
		n.appendLog(formular.LogWarn, "waiting for network")
		n.sendLogsBlock()
		return
	}
	cfg := httpServerConfig{host: n.host, port: n.port, response: n.response, network: n.network}
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: httpServerStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

func (n *httpServerNode) startWorker(cfg httpServerConfig) {
	stop := make(chan struct{})
	worker := &httpServerWorker{stop: stop, done: make(chan struct{})}
	n.worker = worker
	n.logger.Debugf("worker starting on %s", net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)))
	go func() {
		defer close(worker.done)
		runHTTPServerWorker(n.engine.ctx, n.logger, n.w, n.id, stop, cfg)
	}()
}

func (n *httpServerNode) stopWorker() {
	if n.worker != nil {
		n.logger.Debug("worker stopping")
		close(n.worker.stop)
		n.worker = nil
	}
}

func runHTTPServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg httpServerConfig) {
	addr := net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port))
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return
		default:
		}
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}()
		ln, err := cfg.network.Listen(ctx, "tcp", addr)
		cancel()
		close(done)
		if err != nil {
			logger.Debugf("listen failed on %s: %v", addr, err)
			sendHTTPServerLog(w, node, formular.LogDebug, "listen failed: "+err.Error())
			if !sleepOrStopped(stop, time.Second) {
				logger.Debug("worker stopped")
				return
			}
			continue
		}
		body := cfg.response
		if body == "" {
			body = "Debug http server response"
		}
		handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			logger.Debugf("%s %s", r.Method, r.URL.String())
			sendHTTPServerLog(w, node, formular.LogInfo, fmt.Sprintf("%s %s", r.Method, r.URL.String()))
			_, _ = rw.Write([]byte(body))
		})
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		serveDone := make(chan error, 1)
		go func() { serveDone <- server.Serve(ln) }()
		logger.Debugf("listening on %s", addr)
		sendHTTPServerLog(w, node, formular.LogInfo, "listening on "+addr)
		select {
		case <-stop:
			_ = ln.Close()
			_ = server.Shutdown(context.Background())
			<-serveDone
			logger.Debug("worker stopped")
			return
		case err := <-serveDone:
			_ = ln.Close()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Debugf("server stopped: %v", err)
				sendHTTPServerLog(w, node, formular.LogWarn, "server stopped: "+err.Error())
			}
			if !sleepOrStopped(stop, time.Second) {
				logger.Debug("worker stopped")
				return
			}
		}
	}
}

func sendHTTPServerLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: httpServerLog{level: level, text: text}})
}

func (n *httpServerNode) requestNetwork() {
	n.requestPort(n.netp)
}

func (n *httpServerNode) requestResponse() {
	n.requestPort(n.in)
}

func (n *httpServerNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *httpServerNode) updateLabel() error {
	return n.w.SetNodeLabel(n.id, net.JoinHostPort(n.host, strconv.Itoa(n.port)))
}

func (n *httpServerNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.listenBlock(), n.logsBlock()},
	})
}

func (n *httpServerNode) sendListenBlock() {
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.listenBlock(),
	})
}

func (n *httpServerNode) sendLogsBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.logsBlock(),
	})
}

func (n *httpServerNode) listenBlock() formular.Block {
	readonly := n.listenReadonly()

	return formular.Block{ID: "listen", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{
			Type:  formular.ItemField,
			ID:    "host",
			Label: "Host",
			Field: &formular.Field{
				Kind:     formular.FieldText,
				Value:    n.host,
				Readonly: readonly,
			},
		},
		{
			Type:  formular.ItemField,
			ID:    "port",
			Label: "Port",
			Field: &formular.Field{
				Kind:     formular.FieldInt,
				Value:    n.port,
				Readonly: readonly,
			},
		},
	}}
}

func (n *httpServerNode) logsBlock() formular.Block {
	return formular.Block{ID: "logs", Order: 20, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLogs, ID: "worker", Label: "Worker", Logs: append([]formular.LogLine(nil), n.logs...)},
	}}
}

func (n *httpServerNode) appendLog(level, text string) {
	n.logs = append(n.logs, formular.LogLine{Level: level, Text: text})
	if len(n.logs) > 40 {
		n.logs = append([]formular.LogLine(nil), n.logs[len(n.logs)-40:]...)
	}
}

func (n *httpServerNode) setHost(value string) {
	if n.setHostNoRefresh(value) {
		_ = n.updateLabel()
		n.sendListenBlock()
		n.restartWorker()
		return
	}
	n.sendListenBlock()
}

func (n *httpServerNode) setHostNoRefresh(value string) bool {
	if value == "" {
		value = DefaultHTTPServerHost
	}
	if n.host == value {
		return false
	}
	n.host = value
	return true
}

func (n *httpServerNode) setPortString(value string) {
	port := parsePort(value, DefaultHTTPServerPort)
	if n.setPortNoRefresh(port) {
		_ = n.updateLabel()
		n.sendListenBlock()
		n.restartWorker()
		return
	}

	n.sendListenBlock()
}

func (n *httpServerNode) setPortNoRefresh(port int) bool {
	if port <= 0 {
		port = DefaultHTTPServerPort
	}
	if n.port == port {
		return false
	}
	n.port = port
	return true
}

func (n *httpServerNode) requestHost() {
	n.requestPort(n.hostp)
}

func (n *httpServerNode) requestListenPort() {
	n.requestPort(n.portp)
}

// Reports if node menu should be in readonly state
func (n *httpServerNode) listenReadonly() bool {
	return n.portHasLinks(n.hostp) || n.portHasLinks(n.portp)
}

func (n *httpServerNode) portHasLinks(port uint64) bool {
	if n.w == nil || port == 0 {
		return false
	}

	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}
