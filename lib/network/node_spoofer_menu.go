package networknodes

import "github.com/asciimoth/formular"

func (n *spooferNode) updateLabel() error {
	if n.formError != "" {
		return n.SetLabel("invalid config")
	}
	if n.tun == nil {
		return n.SetLabel("waiting for tun")
	}
	if n.networkValue == nil {
		return n.SetLabel("waiting for network")
	}
	return n.SetLabel("forwarding")
}

func (n *spooferNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *spooferNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *spooferNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
		configIntField("queueLength", "Queue length", n.config.queueLength, statusErr, n.formError),
		configCheckbox("disableSACK", "Disable TCP SACK", n.config.disableSACK),
		configIntField("ttl", "TTL", n.config.ttl, statusErr, n.formError),
		configIntField("icmpBurst", "ICMP burst", n.config.icmpBurst, statusErr, n.formError),
		configFloatField("icmpLimit", "ICMP limit", n.config.icmpLimit, statusErr, n.formError),
		configIntField("tcpSendBufferSize", "TCP send buffer", n.config.tcpSendBufferSize, statusErr, n.formError),
		configIntField("tcpReceiveBufferSize", "TCP receive buffer", n.config.tcpReceiveBufferSize, statusErr, n.formError),
		configTextField("congestionControlAlg", "TCP congestion", n.config.congestionControlAlg, statusErr, n.formError, false),
		configCheckbox("disableNagle", "Disable Nagle", n.config.disableNagle),
		configCheckbox("disableTCPModRecBuff", "Disable TCP receive autotune", n.config.disableTCPModRecBuff),
		configCheckbox("tcpKeepAlive", "TCP keepalive", n.config.tcpKeepAlive),
		configIntField("tcpKeepAliveIdle", "TCP keepalive idle", n.config.tcpKeepAliveIdle, statusErr, n.formError),
		configIntField("tcpKeepAliveInterval", "TCP keepalive interval", n.config.tcpKeepAliveInterval, statusErr, n.formError),
		configIntField("tcpKeepAliveCount", "TCP keepalive count", n.config.tcpKeepAliveCount, statusErr, n.formError),
		configIntField("tcpForwardWnd", "TCP forward window", n.config.tcpForwardWnd, statusErr, n.formError),
		configIntField("tcpForwardAttempts", "TCP forward attempts", n.config.tcpForwardAttempts, statusErr, n.formError),
	}}
}
