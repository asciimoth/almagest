package tunnodes

import (
	"net/netip"
	"strings"

	"github.com/asciimoth/formular"
)

func (n *vtunNode) updateLabel() error {
	if n.formError != "" {
		return n.SetLabel("invalid config")
	}
	if n.vtun == nil {
		return n.SetLabel("idle")
	}
	return n.SetLabel(n.config.name)
}

func (n *vtunNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.menuBlocks()...)
}

func (n *vtunNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.menuBlocks()...)
}

func (n *vtunNode) menuBlocks() []formular.Block {
	return []formular.Block{n.effectiveBlock(), n.configBlock()}
}

func (n *vtunNode) sendEffectiveBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.effectiveBlock())
}

func (n *vtunNode) effectiveBlock() formular.Block {
	localAddrs := n.effectiveLocalAddrsForMenu()
	dnsServers, _ := n.effectiveDNSServers()
	mtu := n.config.mtu
	if mtu <= 0 && n.tunConfig.MTU > 0 {
		mtu = n.tunConfig.MTU
	}
	if mtu <= 0 {
		mtu = defaultVTunMTU
	}
	return formular.Block{ID: "effective", Order: 0, Generation: 1, Items: []formular.Item{
		readonlyTextField("name", "Name", n.config.name),
		readonlyStringListField("localAddrs", "Local addrs", netipAddrsText(localAddrs)),
		readonlyStringListField("dnsServers", "DNS servers", netipAddrsText(dnsServers)),
		readonlyIntField("mtu", "MTU", mtu),
		readonlyIntField("endpointCh", "Endpoint channel", n.config.endpointCh),
		readonlyIntField("eventCh", "Event channel", n.config.eventCh),
		readonlyBoolField("noLoopbackAddr", "No loopback addrs", n.config.noLoopbackAddr),
		readonlyBoolField("disableSACK", "Disable TCP SACK", n.config.disableSACK),
	}}
}

func (n *vtunNode) effectiveLocalAddrsForMenu() []netip.Addr {
	if strings.TrimSpace(n.config.localAddrs) != "" {
		addrs, _ := parseAddrList(n.config.localAddrs, true)
		return addrs
	}
	if addrs := tunConfigAddrs(n.tunConfig); len(addrs) > 0 {
		return addrs
	}
	return append([]netip.Addr(nil), n.generatedAddrs...)
}

func (n *vtunNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	dnsServers := configTextField("dnsServers", "DNS servers", n.config.dnsServers, statusErr, n.formError, true)
	dnsServers.Help = "DNS server IP addresses. If this field is empty, VTun uses 8.8.8.8."
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configTextField("name", "Name", n.config.name, statusErr, n.formError, false),
		configTextField("localAddrs", "Local addrs", n.config.localAddrs, statusErr, n.formError, true),
		dnsServers,
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
		configIntField("endpointCh", "Endpoint channel", n.config.endpointCh, statusErr, n.formError),
		configIntField("eventCh", "Event channel", n.config.eventCh, statusErr, n.formError),
		// configIntField("mwo", "Max write offset", n.config.mwo, false, ""),
		// configIntField("mro", "Max read offset", n.config.mro, false, ""),
		configCheckbox("noLoopbackAddr", "No loopback addrs", n.config.noLoopbackAddr),
		configCheckbox("disableSACK", "Disable TCP SACK", n.config.disableSACK),
	}}
}

func netipAddrsText(addrs []netip.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IsValid() {
			out = append(out, addr.String())
		}
	}
	return out
}
