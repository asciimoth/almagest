package dnsnodes

import (
	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

func (n *dnsServerNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case !nodekit.NetworkIsReady(n.network):
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
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock(), n.logsBlock())
}

func (n *dnsServerNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock(), n.logsBlock())
}

func (n *dnsServerNode) sendLogsBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.logsBlock())
}

func (n *dnsServerNode) configBlock() formular.Block {
	status := []formular.FieldOption{formular.Validation}
	if n.formError != "" {
		status = append(status, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return formular.Block{ID: DNSServerConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		formular.RadioField(DNSServerNetworkField, "Network", n.config.network, []formular.Choice{
			{Value: "udp", Label: "UDP"},
			{Value: "udp4", Label: "UDP4"},
			{Value: "udp6", Label: "UDP6"},
		}, status...),
		formular.TextField(DNSServerHostField, "Host", n.config.host, append(status, formular.Placeholder(DefaultDNSServerHost))...),
		formular.IntField(DNSServerPortField, "Port", n.config.port, status...),
	}}
}

func (n *dnsServerNode) logsBlock() formular.Block {
	return nodekit.LogsBlock("logs", "Worker", 20, n.logs)
}

func (n *dnsServerNode) appendLog(level, text string) {
	nodekit.AppendCappedLog(&n.logs, 40, level, text)
}
