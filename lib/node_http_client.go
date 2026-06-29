package almagest

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return httpClientClass{engine: e}
	}, false)
}

const (
	NodeTypeHTTPClient = "almagest/HttpClient"

	DefaultHTTPClientURL    = "http://127.0.0.1:8080/"
	DefaultHTTPClientMethod = http.MethodGet

	httpClientStatusReady   = "ready"
	httpClientStatusRunning = "running..."
	httpClientStatusFail    = "fail"
)

type httpClientClass struct {
	engine *engine
}

func (httpClientClass) ClassName() string        { return NodeTypeHTTPClient }
func (httpClientClass) ShortDescription() string { return "HTTP client" }
func (httpClientClass) LongDescription() string {
	return "Runs HTTP requests over a received demo.pasta/network in a background worker."
}

func (httpClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "URL", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Method", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Body", Types: []string{std.TypeString}},
	}}
}

func (c httpClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &httpClientNode{
		url:        configer.String(cfg, configer.Path{"url"}, DefaultHTTPClientURL),
		method:     configer.String(cfg, configer.Path{"method"}, DefaultHTTPClientMethod),
		body:       configer.String(cfg, configer.Path{"body"}, ""),
		statusText: httpClientStatusReady,

		engine: c.engine,
	}, nil
}

type httpClientNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w       *pasta.Workspace
	id      uint64
	netp    uint64
	urlp    uint64
	methodp uint64
	bodyp   uint64

	network NetworkCloser
	url     string
	method  string
	body    string

	requestID  uint64
	pending    bool
	statusText string
	resp       string
	errText    string

	jobs chan httpClientRequest
	stop chan struct{}
}

type httpClientRequest struct {
	id      uint64
	network NetworkCloser
	url     string
	method  string
	body    string
}

type httpClientResult struct {
	id     uint64
	status string
	body   string
	err    string
}

func (n *httpClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger

	if n.method == "" {
		n.method = DefaultHTTPClientMethod
	}
	if n.url == "" {
		n.url = DefaultHTTPClientURL
	}
	if n.statusText == "" {
		n.statusText = httpClientStatusReady
	}

	if restored != nil {
		if len(restored.RightPorts) > 0 {
			n.netp = restored.RightPorts[0]
		}
		if len(restored.LeftPorts) > 0 {
			n.urlp = restored.LeftPorts[0]
		}
		if len(restored.LeftPorts) > 1 {
			n.methodp = restored.LeftPorts[1]
		}
		if len(restored.LeftPorts) > 2 {
			n.bodyp = restored.LeftPorts[2]
		}
	}

	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}

	_ = n.updateLabel()
	n.startWorker()
	n.sendMenuSnapshot()
	return nil
}

func (n *httpClientNode) OnReady() error {
	n.requestNetwork()
	n.requestURL()
	n.requestMethod()
	n.requestBody()
	return nil
}

func (n *httpClientNode) OnStop() {
	n.stopWorker()
}

func (n *httpClientNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.netp:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}

	case n.urlp, n.methodp, n.bodyp:
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

func (n *httpClientNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.netp:
		std.Request(n.w, n.id, link)

	case n.urlp, n.methodp, n.bodyp:
		n.sendRequestBlock()
		std.Request(n.w, n.id, link)
	}

	return nil
}

func (n *httpClientNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	switch port {
	case n.netp:
		n.network = nil
		n.resetStatusReady()

	case n.urlp, n.methodp, n.bodyp:
		n.sendRequestBlock()
	}

	return nil
}

func (n *httpClientNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == TypeNetwork {
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil {
			return nil
		}
		if NetworkIsReady(n.network) && SameNetworkSource(n.network, payload.Network) {
			if !SameNetworkInstance(n.network, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.network = payload.Network
		n.resetStatusReady()
		return nil
	}

	if receiverPortDirection == "left" && linkType == std.TypeString {
		value, ok := std.StringFromPayload(event.Payload)
		if !ok {
			return nil
		}

		switch event.ReceiverPort {
		case n.urlp:
			n.setURL(value)
		case n.methodp:
			n.setMethod(value)
		case n.bodyp:
			n.setBody(value)
		}
	}

	return nil
}

func (n *httpClientNode) OnInbox(message pasta.InboxMessage) error {
	result, ok := message.Payload.(httpClientResult)
	if !ok || result.id != n.requestID {
		return nil
	}

	n.pending = false
	n.resp = result.body
	n.errText = result.err

	if result.err != "" {
		n.statusText = httpClientStatusFail
	} else if result.status != "" {
		n.statusText = result.status
	} else {
		n.statusText = httpClientStatusReady
	}

	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()
	return nil
}

func (n *httpClientNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FieldUpdateMessage:
		return n.onFieldUpdate(msg)

	case formular.ButtonPressMessage:
		return n.onButtonPress(msg)

	default:
		return nil
	}
}

func (n *httpClientNode) onFieldUpdate(msg formular.FieldUpdateMessage) error {
	if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "request" || len(msg.Field.ElementPath) > 0 {
		return nil
	}

	value, ok := std.StringFromPayload(msg.Value)
	if !ok {
		n.sendRequestBlock()
		return nil
	}

	switch msg.Field.FieldID {
	case "url":
		if n.portHasLinks(n.urlp) {
			n.sendRequestBlock()
			return nil
		}
		n.setURL(value)

	case "method":
		if n.portHasLinks(n.methodp) {
			n.sendRequestBlock()
			return nil
		}
		n.setMethod(value)

	case "body":
		if n.portHasLinks(n.bodyp) {
			n.sendRequestBlock()
			return nil
		}
		n.setBody(value)
	}

	return nil
}

func (n *httpClientNode) onButtonPress(msg formular.ButtonPressMessage) error {
	if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "request" || msg.ButtonID != "send" {
		return nil
	}
	n.sendRequest()
	return nil
}

func (n *httpClientNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"url":    n.url,
		"method": n.method,
		"body":   n.body,
	})
}

func (n *httpClientNode) sendRequest() {
	if n.pending {
		n.logger.Debug("request ignored: already pending")
		return
	}

	n.requestID++
	req := httpClientRequest{
		id:      n.requestID,
		network: n.network,
		url:     n.url,
		method:  n.method,
		body:    n.body,
	}

	n.pending = true
	n.statusText = httpClientStatusRunning
	n.resp = ""
	n.errText = ""
	n.logger.Debugf("request %d: %s %s", req.id, req.method, req.url)

	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()

	if n.jobs == nil {
		n.startWorker()
	}

	select {
	case n.jobs <- req:
	default:
		n.logger.Debug("request failed: worker is busy")
		n.w.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.id,
			Payload:      httpClientResult{id: req.id, err: "worker is busy"},
		})
	}
}

func (n *httpClientNode) startWorker() {
	if n.jobs != nil || n.stop != nil {
		return
	}
	n.jobs = make(chan httpClientRequest, 1)
	n.stop = make(chan struct{})
	n.logger.Debug("worker started")
	go runHTTPClientWorker(n.engine.ctx, n.logger, n.w, n.id, n.jobs, n.stop)
}

func (n *httpClientNode) stopWorker() {
	if n.stop != nil {
		n.logger.Debug("worker stopping")
		close(n.stop)
		n.stop = nil
	}
	n.jobs = nil
}

func (n *httpClientNode) restartWorker() {
	n.stopWorker()
	n.startWorker()
}

func runHTTPClientWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, jobs <-chan httpClientRequest, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return

		case req := <-jobs:
			logger.Debugf("request %d: worker processing", req.id)
			ctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})

			go func() {
				select {
				case <-stop:
					cancel()
				case <-done:
				}
			}()

			result := doHTTPRequest(ctx, req)

			cancel()
			close(done)

			if result.err != "" {
				logger.Debugf("request %d failed: %s", req.id, result.err)
			} else {
				logger.Debugf("request %d complete: %s", req.id, result.status)
			}
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: result})
		}
	}
}

func doHTTPRequest(parent context.Context, req httpClientRequest) httpClientResult {
	if req.network == nil {
		return httpClientResult{id: req.id, err: "network is not ready"}
	}

	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	body := io.Reader(nil)
	if req.body != "" {
		body = strings.NewReader(req.body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, req.url, body)
	if err != nil {
		return httpClientResult{id: req.id, err: err.Error()}
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return req.network.Dial(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	resp, err := client.Do(httpReq)
	if err != nil {
		return httpClientResult{id: req.id, err: err.Error()}
	}
	defer resp.Body.Close() //nolint

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpClientResult{id: req.id, status: resp.Status, err: err.Error()}
	}

	return httpClientResult{id: req.id, status: resp.Status, body: string(data)}
}

func (n *httpClientNode) resetStatusReady() {
	n.requestID++
	n.pending = false
	n.statusText = httpClientStatusReady
	n.resp = ""
	n.errText = ""

	n.restartWorker()
	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()
}

func (n *httpClientNode) requestNetwork() {
	n.requestPort(n.netp)
}

func (n *httpClientNode) requestURL() {
	n.requestPort(n.urlp)
}

func (n *httpClientNode) requestMethod() {
	n.requestPort(n.methodp)
}

func (n *httpClientNode) requestBody() {
	n.requestPort(n.bodyp)
}

func (n *httpClientNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *httpClientNode) updateLabel() error {
	return n.w.SetNodeLabel(n.id, n.currentStatusText())
}

func (n *httpClientNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshot(pasta.NodeMenuID(n.id), 1, n.requestBlock(), n.resultBlock()))
}

func (n *httpClientNode) sendRequestBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshot(pasta.NodeMenuID(n.id), 1, 1, n.requestBlock()))
}

func (n *httpClientNode) sendResultBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshot(pasta.NodeMenuID(n.id), 1, 1, n.resultBlock()))
}

func (n *httpClientNode) requestBlock() formular.Block {
	return formular.Block{ID: "request", Order: 10, Generation: 1, Items: []formular.Item{
		{
			Type:  formular.ItemField,
			ID:    "url",
			Label: "URL",
			Field: &formular.Field{
				Kind:     formular.FieldText,
				Value:    n.url,
				Readonly: n.portHasLinks(n.urlp),
			},
		},
		{
			Type:  formular.ItemField,
			ID:    "method",
			Label: "Method",
			Field: &formular.Field{
				Kind:     formular.FieldText,
				Value:    n.method,
				Readonly: n.portHasLinks(n.methodp),
			},
		},
		{
			Type:  formular.ItemField,
			ID:    "body",
			Label: "Body",
			Field: &formular.Field{
				Kind:      formular.FieldText,
				Value:     n.body,
				Readonly:  n.portHasLinks(n.bodyp),
				Multiline: true,
			},
		},
		{
			Type:     formular.ItemButton,
			ID:       "send",
			Label:    "Send request",
			Inactive: n.pending,
		},
	}}
}

func (n *httpClientNode) resultBlock() formular.Block {
	return formular.Block{ID: "result", Order: 20, Generation: 1, Items: []formular.Item{
		{
			Type:   formular.ItemLabel,
			ID:     "status",
			Text:   "Status: " + n.currentStatusText(),
			Format: formular.TextPlain,
		},
		{
			Type:  formular.ItemField,
			ID:    "error",
			Label: "Error",
			Field: &formular.Field{
				Kind:     formular.FieldText,
				Value:    n.errText,
				Readonly: true,
			},
		},
		{
			Type:  formular.ItemField,
			ID:    "response",
			Label: "Response",
			Field: &formular.Field{
				Kind:      formular.FieldText,
				Value:     n.resp,
				Readonly:  true,
				Multiline: true,
			},
		},
	}}
}

func (n *httpClientNode) currentStatusText() string {
	if n.statusText == "" {
		return httpClientStatusReady
	}
	return n.statusText
}

func (n *httpClientNode) setURL(value string) {
	if n.setURLNoRefresh(value) {
		n.sendRequestBlock()
		return
	}
	n.sendRequestBlock()
}

func (n *httpClientNode) setURLNoRefresh(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultHTTPClientURL
	}
	if n.url == value {
		return false
	}
	n.url = value
	return true
}

func (n *httpClientNode) setMethod(value string) {
	if n.setMethodNoRefresh(value) {
		n.sendRequestBlock()
		return
	}
	n.sendRequestBlock()
}

func (n *httpClientNode) setMethodNoRefresh(value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		value = DefaultHTTPClientMethod
	}
	if n.method == value {
		return false
	}
	n.method = value
	return true
}

func (n *httpClientNode) setBody(value string) {
	if n.setBodyNoRefresh(value) {
		n.sendRequestBlock()
		return
	}
	n.sendRequestBlock()
}

func (n *httpClientNode) setBodyNoRefresh(value string) bool {
	if n.body == value {
		return false
	}
	n.body = value
	return true
}

func (n *httpClientNode) portHasLinks(port uint64) bool {
	if n.w == nil || port == 0 {
		return false
	}

	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}
