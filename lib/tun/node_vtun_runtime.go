package tunnodes

import (
	"net"
	"net/netip"
	"slices"
	"strings"

	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
)

type vtunRuntimeConfig struct {
	localAddrs     []netip.Addr
	sourceRoutes   []netstackvtun.SourceRoute
	dnsServers     []netip.Addr
	name           string
	mtu            int
	endpointCh     int
	eventCh        int
	noLoopbackAddr bool
	disableSACK    bool
}

func (c vtunRuntimeConfig) equal(other vtunRuntimeConfig) bool {
	return c.name == other.name &&
		c.mtu == other.mtu &&
		c.endpointCh == other.endpointCh &&
		c.eventCh == other.eventCh &&
		c.noLoopbackAddr == other.noLoopbackAddr &&
		c.disableSACK == other.disableSACK &&
		slices.Equal(c.localAddrs, other.localAddrs) &&
		slices.Equal(c.sourceRoutes, other.sourceRoutes) &&
		slices.Equal(c.dnsServers, other.dnsServers)
}

func (c vtunRuntimeConfig) equalExceptSourceRoutes(other vtunRuntimeConfig) bool {
	c.sourceRoutes = nil
	other.sourceRoutes = nil
	return c.equal(other)
}

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
	if !active {
		n.dropVTun()
		_ = n.updateLabel()
		return nil
	}
	runtimeConfig, opts, err := n.buildRuntimeConfig()
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	if n.vtun != nil && n.vtunRuntime.equal(runtimeConfig) {
		n.formError = ""
		_ = n.updateLabel()
		n.sendEffectiveBlock()
		return nil
	}
	if n.vtun != nil && n.vtunRuntime.equalExceptSourceRoutes(runtimeConfig) {
		if err := n.vtun.SetSourceRoutes(opts.SourceRoutes); err != nil {
			n.formError = err.Error()
			n.sendForcedMenuSnapshot()
			_ = n.updateLabel()
			return err
		}
		n.formError = ""
		n.vtunRuntime = runtimeConfig
		_ = n.updateLabel()
		n.sendEffectiveBlock()
		return nil
	}
	vt, err := opts.Build()
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	n.dropVTun()
	n.formError = ""
	n.vtun = vt
	n.vtunRuntime = runtimeConfig
	_ = n.W.AddNodeResourceLocked(n.ID, vt)
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.sendAll()
	return nil
}

func (n *vtunNode) buildRuntimeConfig() (vtunRuntimeConfig, *netstackvtun.Opts, error) {
	localAddrs, err := n.effectiveLocalAddrs()
	if err != nil {
		return vtunRuntimeConfig{}, nil, err
	}
	sourceRoutes := n.effectiveSourceRoutes()
	dnsServers, err := n.effectiveDNSServers()
	if err != nil {
		return vtunRuntimeConfig{}, nil, err
	}
	mtu := n.config.mtu
	if mtu <= 0 && n.tunConfig.MTU > 0 {
		mtu = n.tunConfig.MTU
	}
	if mtu <= 0 {
		mtu = defaultVTunMTU
	}
	runtimeConfig := vtunRuntimeConfig{
		localAddrs:     slices.Clone(localAddrs),
		sourceRoutes:   slices.Clone(sourceRoutes),
		dnsServers:     slices.Clone(dnsServers),
		name:           n.config.name,
		mtu:            mtu,
		endpointCh:     n.config.endpointCh,
		eventCh:        n.config.eventCh,
		noLoopbackAddr: n.config.noLoopbackAddr,
		disableSACK:    n.config.disableSACK,
	}
	return runtimeConfig, &netstackvtun.Opts{
		EndpointCh:     n.config.endpointCh,
		EventCh:        n.config.eventCh,
		LocalAddrs:     localAddrs,
		SourceRoutes:   sourceRoutes,
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

func (n *vtunNode) effectiveSourceRoutes() []netstackvtun.SourceRoute {
	// Explicit local addresses replace the address suggestions. Their list can
	// differ from the source addresses in the suggested policy.
	if strings.TrimSpace(n.config.localAddrs) != "" {
		return nil
	}
	out := make([]netstackvtun.SourceRoute, 0, len(n.tunConfig.SourceRoutes))
	for _, route := range n.tunConfig.SourceRoutes {
		out = append(out, netstackvtun.SourceRoute{
			Destination: route.Destination,
			Source:      route.Source,
		})
	}
	return out
}

func (n *vtunNode) effectiveDNSServers() ([]netip.Addr, error) {
	servers, err := parseAddrList(n.config.dnsServers, false)
	if err != nil {
		return nil, err
	}
	if len(servers) > 0 {
		return servers, nil
	}
	if suggested, err := netip.ParseAddr(strings.TrimSpace(n.tunConfig.DNS)); err == nil {
		return []netip.Addr{suggested}, nil
	}
	return []netip.Addr{netip.MustParseAddr(DefaultVTunDNSServer)}, nil
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
	return strings.TrimSpace(n.config.localAddrs) == "" ||
		strings.TrimSpace(n.config.dnsServers) == "" ||
		n.config.mtu <= 0
}

func (n *vtunNode) allocLocalAddrs() []netip.Addr {
	alloc := runtimeIPAllocator(n.Runtime)
	if n.System() == nil || alloc == nil {
		return nil
	}
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
	alloc := runtimeIPAllocator(n.Runtime)
	if len(n.generatedAddrs) == 0 || n.System() == nil || alloc == nil {
		n.generatedAddrs = nil
		return
	}
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
	n.vtunRuntime = vtunRuntimeConfig{}
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
	n.outputNetworks.SendLocked(n.W, n.ID, link, n.vtun)
}

func (n *vtunNode) refreshNetworkToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	n.outputNetworks.RefreshLocked(n.W, n.ID, link, n.vtun)
}

func (n *vtunNode) sendTunToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	n.outputTuns.SendLocked(n.W, n.ID, link, n.vtun, enginePool(n.Runtime))
}

func (n *vtunNode) refreshTunToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	n.outputTuns.RefreshLocked(n.W, n.ID, link, n.vtun, enginePool(n.Runtime))
}

func (n *vtunNode) hasAnyLink() bool {
	return n.PortHasLinks(n.network) || n.PortHasLinks(n.tunp)
}
