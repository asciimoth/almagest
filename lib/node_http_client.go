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
		NodeBase:   NodeBase{engine: c.engine},
		url:        configer.String(cfg, configer.Path{"url"}, DefaultHTTPClientURL),
		method:     configer.String(cfg, configer.Path{"method"}, DefaultHTTPClientMethod),
		body:       configer.String(cfg, configer.Path{"body"}, ""),
		statusText: httpClientStatusReady,
	}, nil
}

type httpClientNode struct {
	NodeBase

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
	n.InitBaseNode(w, logger, id)

	if n.method == "" {
		n.method = DefaultHTTPClientMethod
	}
	if n.url == "" {
		n.url = DefaultHTTPClientURL
	}
	if n.statusText == "" {
		n.statusText = httpClientStatusReady
	}

	n.netp = restoredRight(restored, 0)
	n.urlp = restoredLeft(restored, 0)
	n.methodp = restoredLeft(restored, 1)
	n.bodyp = restoredLeft(restored, 2)

	if err := n.SetPrimary(TypeNetwork); err != nil {
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
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}

	case n.urlp, n.methodp, n.bodyp:
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
		next, changed := AcceptNetworkPayload(n.w, n.id, event, n.network)
		if !changed {
			return nil
		}
		n.network = next
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
		if n.PortHasLinks(n.urlp) {
			n.sendRequestBlock()
			return nil
		}
		n.setURL(value)

	case "method":
		if n.PortHasLinks(n.methodp) {
			n.sendRequestBlock()
			return nil
		}
		n.setMethod(value)

	case "body":
		if n.PortHasLinks(n.bodyp) {
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
	n.RequestPort(n.netp)
}

func (n *httpClientNode) requestURL() {
	n.RequestPort(n.urlp)
}

func (n *httpClientNode) requestMethod() {
	n.RequestPort(n.methodp)
}

func (n *httpClientNode) requestBody() {
	n.RequestPort(n.bodyp)
}

func (n *httpClientNode) updateLabel() error {
	return n.SetLabel(n.currentStatusText())
}

func (n *httpClientNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.requestBlock(), n.resultBlock())
}

func (n *httpClientNode) sendRequestBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.requestBlock())
}

func (n *httpClientNode) sendResultBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.resultBlock())
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
				Readonly: n.PortHasLinks(n.urlp),
			},
		},
		{
			Type:  formular.ItemField,
			ID:    "method",
			Label: "Method",
			Field: &formular.Field{
				Kind:     formular.FieldText,
				Value:    n.method,
				Readonly: n.PortHasLinks(n.methodp),
			},
		},
		{
			Type:  formular.ItemField,
			ID:    "body",
			Label: "Body",
			Field: &formular.Field{
				Kind:      formular.FieldText,
				Value:     n.body,
				Readonly:  n.PortHasLinks(n.bodyp),
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
