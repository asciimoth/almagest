package almagest

import (
	"github.com/asciimoth/formular"
)

func (n *dnsServerNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case !NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case n.listenErr != "":
		return n.SetLabel("listen failed")
	case n.server == nil:
		return n.SetLabel("starting DNS server")
	case !DNSIsReady(n.upstream):
		return n.SetLabel("waiting for DNS")
	default:
		return n.SetLabel("listening " + n.config.addr())
	}
}

func (n *dnsServerNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock(), n.logsBlock())
}

func (n *dnsServerNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.configBlock(), n.logsBlock())
}

func (n *dnsServerNode) sendLogsBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.logsBlock())
}

func (n *dnsServerNode) configBlock() formular.Block {
	status := []formular.FieldOption{formular.Validation}
	if n.formError != "" {
		status = append(status, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return formular.Block{ID: dnsServerConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		formular.RadioField(dnsServerNetworkField, "Network", n.config.network, []formular.Choice{
			{Value: "udp", Label: "UDP"},
			{Value: "udp4", Label: "UDP4"},
			{Value: "udp6", Label: "UDP6"},
		}, status...),
		formular.TextField(dnsServerHostField, "Host", n.config.host, append(status, formular.Placeholder(DefaultDNSServerHost))...),
		formular.IntField(dnsServerPortField, "Port", n.config.port, status...),
	}}
}

func (n *dnsServerNode) logsBlock() formular.Block {
	return logsBlock("logs", "Worker", 20, n.logs)
}

func (n *dnsServerNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 40, level, text)
}
