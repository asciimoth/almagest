package almagest

import (
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func (n *udpPingServerNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock(), n.logsBlock())
}

func (n *udpPingClientNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock(), n.resultBlock(), n.logsBlock())
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
	sendNodeBlockSnapshot(w, node, block)
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
	return logsBlock("logs", "Worker", 30, logs)
}

func (n *udpPingServerNode) log(level, text string) {
	n.appendLog(level, text)
	n.sendLogsBlock()
}

func (n *udpPingServerNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 40, level, text)
}

func (n *udpPingClientNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 40, level, text)
}

func (n *udpPingServerNode) configReadonly() bool {
	for _, port := range n.configPorts() {
		if n.PortHasLinks(port) {
			return true
		}
	}
	return false
}

func (n *udpPingClientNode) configReadonly() bool {
	for _, port := range n.configPorts() {
		if n.PortHasLinks(port) {
			return true
		}
	}
	return false
}
