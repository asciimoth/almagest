package almagest

import (
	"strings"

	"github.com/asciimoth/formular"
)

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
		nodeTLSConfigSelectorItem(&n.NodeBase, n.tlsConfigName, n.tlsConfigErr),
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

func (n *httpClientNode) setTLSConfig(value string) {
	value = normalizeNodeTLSConfigName(value)
	if n.tlsConfigName == value {
		n.sendRequestBlock()
		return
	}
	n.tlsConfigName = value
	n.refreshTLSConfig()
	n.resetStatusReady()
}

func (n *httpClientNode) refreshTLSConfig() {
	result, err := resolveNodeTLSClientConfig(&n.NodeBase, n.tlsConfigName)
	n.tlsConfig = result.config
	n.tlsConfigEffective = result.effectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
}
