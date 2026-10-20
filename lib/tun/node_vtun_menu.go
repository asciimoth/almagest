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
	mtu, constraint := n.effectiveMTU()
	mtuSource := constraint.Source + ": " + constraint.Reason
	addressFamilies := readonlyStringListField("addressFamilies", "Address families", n.effectiveAddressFamilies(localAddrs))
	return formular.Block{ID: "effective", Order: 0, Generation: 1, Items: []formular.Item{
		readonlyTextField("name", "Name", n.config.name),
		readonlyStringListField("localAddrs", "Local addrs", netipAddrsText(localAddrs)),
		addressFamilies,
		readonlyStringListField("dnsServers", "DNS servers", netipAddrsText(dnsServers)),
		readonlyIntField("mtu", "MTU", mtu),
		readonlyTextField("mtuSource", "MTU source", mtuSource),
		readonlyIntField("endpointCh", "Endpoint channel", n.config.endpointCh),
		readonlyIntField("eventCh", "Event channel", n.config.eventCh),
		readonlyBoolField("noLoopbackAddr", "No loopback addrs", n.config.noLoopbackAddr),
		readonlyBoolField("disableSACK", "Disable TCP SACK", n.config.disableSACK),
	}}
}

func (n *vtunNode) effectiveAddressFamilies(localAddrs []netip.Addr) []string {
	if n.vtun != nil {
		localAddrs = n.vtun.LocalAddrs()
	}
	hasIPv4, hasIPv6 := false, false
	for _, addr := range localAddrs {
		hasIPv4 = hasIPv4 || addr.Is4()
		hasIPv6 = hasIPv6 || addr.Is6()
	}
	if n.vtun == nil && !n.config.noLoopbackAddr {
		hasIPv4 = true
		hasIPv6 = true
	}
	families := make([]string, 0, 2)
	if hasIPv4 {
		families = append(families, "ipv4")
	}
	if hasIPv6 {
		families = append(families, "ipv6")
	}
	return families
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
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configTextField("name", "Name", n.config.name, statusErr, n.formError, false),
		configTextField("localAddrs", "Local addrs", n.config.localAddrs, statusErr, n.formError, true),
		dnsServers,
		configIntField("mtu", "MTU ceiling", n.config.mtu, statusErr, n.formError),
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
