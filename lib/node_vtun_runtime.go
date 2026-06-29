package almagest

import (
	"net"
	"net/netip"
	"strings"

	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
)

func (n *vtunNode) ensureVTun() error {
	if n.vtun != nil || !n.hasAnyLink() {
		return nil
	}
	return n.rebuildVTun()
}

func (n *vtunNode) rebuildVTun() error {
	return n.rebuildVTunWithActive(n.hasAnyLink())
}

func (n *vtunNode) rebuildVTunActive() error {
	return n.rebuildVTunWithActive(true)
}

func (n *vtunNode) rebuildVTunWithActive(active bool) error {
	n.dropVTun()
	if !active {
		_ = n.updateLabel()
		return nil
	}
	opts, err := n.buildOpts()
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	vt, err := opts.Build()
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	n.formError = ""
	n.vtun = vt
	_ = n.w.AddNodeResource(n.id, vt)
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.sendAll()
	return nil
}

func (n *vtunNode) buildOpts() (*netstackvtun.Opts, error) {
	localAddrs, err := n.effectiveLocalAddrs()
	if err != nil {
		return nil, err
	}
	dnsServers, err := parseAddrList(n.config.dnsServers, false)
	if err != nil {
		return nil, err
	}
	mtu := n.config.mtu
	if mtu <= 0 && n.tunConfig.MTU > 0 {
		mtu = n.tunConfig.MTU
	}
	if mtu <= 0 {
		mtu = defaultVTunMTU
	}
	return &netstackvtun.Opts{
		EndpointCh:     n.config.endpointCh,
		EventCh:        n.config.eventCh,
		LocalAddrs:     localAddrs,
		DnsServers:     dnsServers,
		Name:           n.config.name,
		NoLoopbackAddr: n.config.noLoopbackAddr,
		NetStackOpts: &netstackhelpers.Opts{
			MTU:         mtu,
			DisableSACK: n.config.disableSACK,
		},
		// MWO: n.config.mwo,
		// MRO: n.config.mro,
	}, nil
}

func (n *vtunNode) effectiveLocalAddrs() ([]netip.Addr, error) {
	if strings.TrimSpace(n.config.localAddrs) != "" {
		return parseAddrList(n.config.localAddrs, true)
	}
	if addrs := tunConfigAddrs(n.tunConfig); len(addrs) > 0 {
		return addrs, nil
	}
	if len(n.generatedAddrs) == 0 {
		n.generatedAddrs = n.allocLocalAddrs()
	}
	return append([]netip.Addr(nil), n.generatedAddrs...), nil
}

func (n *vtunNode) usesTunConfig() bool {
	return strings.TrimSpace(n.config.localAddrs) == "" || n.config.mtu <= 0
}

func (n *vtunNode) allocLocalAddrs() []netip.Addr {
	if n.engine == nil || n.engine.sys == nil || n.engine.AllocIP() == nil {
		return nil
	}
	alloc := n.engine.AllocIP()
	out := make([]netip.Addr, 0, 2)
	if ip, _ := alloc.AllocIP4(); ip != nil {
		if addr, ok := netip.AddrFromSlice(ip.To4()); ok {
			out = append(out, addr)
		}
	}
	if ip, _ := alloc.AllocIP6(); ip != nil {
		if addr, ok := netip.AddrFromSlice(ip.To16()); ok {
			out = append(out, addr)
		}
	}
	return out
}

func (n *vtunNode) freeGeneratedAddrs() {
	if len(n.generatedAddrs) == 0 || n.engine == nil || n.engine.sys == nil || n.engine.AllocIP() == nil {
		n.generatedAddrs = nil
		return
	}
	alloc := n.engine.AllocIP()
	for _, addr := range n.generatedAddrs {
		alloc.FreeIP(net.IP(addr.AsSlice()))
	}
	n.generatedAddrs = nil
}

func (n *vtunNode) dropVTun() {
	n.outputNetworks.CloseAll()
	n.outputTuns.CloseAll()
	if n.vtun != nil {
		_ = n.vtun.Close()
		n.vtun = nil
	}
	n.outputTuns.Wait()
}

func (n *vtunNode) sendAll() {
	if n.vtun == nil {
		return
	}
	n.ForEachPortLink(n.network, n.sendNetworkToLink)
	n.ForEachPortLink(n.tunp, n.sendTunToLink)
}

func (n *vtunNode) sendNetworkToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	n.outputNetworks.Send(n.w, n.id, link, n.vtun)
}

func (n *vtunNode) sendTunToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	n.outputTuns.Send(n.w, n.id, link, n.vtun, enginePool(n.engine))
}

func (n *vtunNode) hasAnyLink() bool {
	return n.PortHasLinks(n.network) || n.PortHasLinks(n.tunp)
}
