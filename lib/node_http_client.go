package almagest

import (
	"net/http"

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
