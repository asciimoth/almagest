package httpnodes

import (
	"crypto/tls"
	"net/http"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	NodeTypeHTTPClient = "almagest/HttpClient"

	DefaultHTTPClientURL    = "http://127.0.0.1:8080/"
	DefaultHTTPClientMethod = http.MethodGet

	HTTPClientStatusRunning = "running..."

	httpClientStatusReady   = "ready"
	httpClientStatusRunning = HTTPClientStatusRunning
	httpClientStatusFail    = "fail"
)

type HTTPClientClass struct {
	Runtime Runtime
}

func (HTTPClientClass) ClassName() string        { return NodeTypeHTTPClient }
func (HTTPClientClass) ShortDescription() string { return "HTTP client" }
func (HTTPClientClass) LongDescription() string {
	return "Runs HTTP requests over a received demo.pasta/network in a background worker."
}

func (HTTPClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "URL", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Method", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Body", Types: []string{std.TypeString}},
	}}
}

func (c HTTPClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &httpClientNode{
		NodeBase:      NodeBase{Runtime: c.Runtime},
		url:           configer.String(cfg, configer.Path{"url"}, DefaultHTTPClientURL),
		method:        configer.String(cfg, configer.Path{"method"}, DefaultHTTPClientMethod),
		body:          configer.String(cfg, configer.Path{"body"}, ""),
		tlsConfigName: readNodeTLSConfigName(cfg),
		statusText:    httpClientStatusReady,
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

	tlsConfigName      string
	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string

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
	n.refreshTLSConfig()

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
		std.RequestLocked(n.W, n.ID, link)

	case n.urlp, n.methodp, n.bodyp:
		n.sendRequestBlock()
		std.RequestLocked(n.W, n.ID, link)
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
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.network)
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
	switch payload := message.Payload.(type) {
	case httpClientResult:
		return n.handleRequestResult(payload)
	case TlsConfigChanged:
		n.handleTLSConfigChanged(payload)
	}
	return nil
}

func (n *httpClientNode) handleRequestResult(result httpClientResult) error {
	if result.id != n.requestID {
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

func (n *httpClientNode) handleTLSConfigChanged(msg TlsConfigChanged) {
	nextName, relevant := handleNodeTLSConfigChanged(n.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.sendRequestBlock()
		return
	}
	n.tlsConfigName = nextName
	n.refreshTLSConfig()
	n.resetStatusReady()
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
	if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "request" || len(msg.Field.ElementPath) > 0 {
		return nil
	}

	switch msg.Field.FieldID {
	case "url":
		value, ok := std.StringFromPayload(msg.Value)
		if !ok {
			n.sendRequestBlock()
			return nil
		}
		if n.PortHasLinks(n.urlp) {
			n.sendRequestBlock()
			return nil
		}
		n.setURL(value)

	case "method":
		value, ok := std.StringFromPayload(msg.Value)
		if !ok {
			n.sendRequestBlock()
			return nil
		}
		if n.PortHasLinks(n.methodp) {
			n.sendRequestBlock()
			return nil
		}
		n.setMethod(value)

	case "body":
		value, ok := std.StringFromPayload(msg.Value)
		if !ok {
			n.sendRequestBlock()
			return nil
		}
		if n.PortHasLinks(n.bodyp) {
			n.sendRequestBlock()
			return nil
		}
		n.setBody(value)

	case nodeTLSConfigFieldID:
		value, ok := nodeTLSConfigValueFromAny(msg.Value)
		if !ok {
			n.sendRequestBlock()
			return nil
		}
		n.setTLSConfig(value)
	}

	return nil
}

func (n *httpClientNode) onButtonPress(msg formular.ButtonPressMessage) error {
	if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "request" || msg.ButtonID != "send" {
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
		"url":       n.url,
		"method":    n.method,
		"body":      n.body,
		"tlsConfig": n.tlsConfigName,
	})
}

func (n *httpClientNode) sendRequest() {
	if n.pending {
		n.Logger.Debug("request ignored: already pending")
		return
	}

	n.requestID++
	req := httpClientRequest{
		id:           n.requestID,
		network:      n.network,
		url:          n.url,
		method:       n.method,
		body:         n.body,
		tlsConfig:    n.currentTLSConfig(),
		tlsConfigErr: n.tlsConfigErr,
	}

	n.pending = true
	n.statusText = httpClientStatusRunning
	n.resp = ""
	n.errText = ""
	n.Logger.Debugf("request %d: %s %s", req.id, req.method, req.url)

	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()

	if n.jobs == nil {
		n.startWorker()
	}

	select {
	case n.jobs <- req:
	default:
		n.Logger.Debug("request failed: worker is busy")
		n.W.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.ID,
			Payload:      httpClientResult{id: req.id, err: "worker is busy"},
		})
	}
}
