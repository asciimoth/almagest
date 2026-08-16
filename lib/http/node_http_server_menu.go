package httpnodes

import (
	"net"
	"strconv"

	"github.com/asciimoth/formular"
)

func (n *httpServerNode) requestNetwork() {
	n.RequestPort(n.netp)
}

func (n *httpServerNode) requestResponse() {
	n.RequestPort(n.in)
}

func (n *httpServerNode) requestHost() {
	n.RequestPort(n.hostp)
}

func (n *httpServerNode) requestListenPort() {
	n.RequestPort(n.portp)
}

func (n *httpServerNode) updateLabel() error {
	return n.SetLabel(net.JoinHostPort(n.host, strconv.Itoa(n.port)))
}

func (n *httpServerNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.listenBlock(), n.logsBlock())
}

func (n *httpServerNode) sendListenBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.listenBlock())
}

func (n *httpServerNode) sendLogsBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.logsBlock())
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
	return logsBlock("logs", "Worker", 20, n.logs)
}

func (n *httpServerNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 40, level, text)
}

// Reports if node menu should be in readonly state
func (n *httpServerNode) listenReadonly() bool {
	return n.PortHasLinks(n.hostp) || n.PortHasLinks(n.portp)
}
