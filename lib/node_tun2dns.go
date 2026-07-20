package almagest

import (
	"errors"
	"fmt"
	"sync"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return tun2DNSClass{engine: e}
	}, false)
}

const (
	// NodeTypeTun2Dns is the node class name for a TUN-to-DNS packet adapter.
	NodeTypeTun2Dns = "almagest/Tun2Dns"

	defaultTun2DNSPacketSize = 1500
)

type tun2DNSClass struct {
	engine *engine
}

func (tun2DNSClass) ClassName() string        { return NodeTypeTun2Dns }
func (tun2DNSClass) ShortDescription() string { return "TUN to DNS" }
func (tun2DNSClass) LongDescription() string {
	return "Adapts raw UDP DNS packets received from an almagest/tun endpoint into requests on an almagest/dns interface, then writes raw IP DNS responses back to the TUN."
}
func (tun2DNSClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		DNSPort("right", "DNS"),
	}}
}
func (c tun2DNSClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tun2DNSNode{NodeBase: NodeBase{engine: c.engine}, pool: enginePool(c.engine)}, nil
}

// tun2DNSNode is a two-input adapter around gonnect/dns.IPPacketAdapter. The
// left-directed TUN port supplies the packet stream and the right-directed DNS
// port supplies the upstream resolver. One reader goroutine is active for the
// current TUN wrapper; replacing the DNS input swaps only the IPPacketAdapter,
// avoiding competing reads from the same TUN.
type tun2DNSNode struct {
	NodeBase

	pool bufpool.Pool

	tunIn uint64
	dnsIn uint64

	tunLink uint64
	dnsLink uint64

	mu         sync.Mutex
	generation uint64
	currentTun tun.Tun
	currentDNS dns.Interface
	adapter    *dns.IPPacketAdapter
	tunFailed  bool
	lastErr    string
}

func (n *tun2DNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.tunIn = restoredLeft(restored, 0)
	n.dnsIn = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *tun2DNSNode) OnReady() error {
	n.RequestPort(n.tunIn)
	n.RequestPort(n.dnsIn)
	return nil
}

func (n *tun2DNSNode) OnStop() {
	n.detachRuntime(true, true)
}

func (n *tun2DNSNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunIn:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	case n.dnsIn:
		if portDirection != "right" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *tun2DNSNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunIn && portDirection == "left" && linkType == TypeTun:
		n.tunLink = link
		std.RequestLocked(n.w, n.id, link)
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS:
		n.dnsLink = link
		std.RequestLocked(n.w, n.id, link)
	}
	return n.updateLabel()
}

func (n *tun2DNSNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunIn && portDirection == "left" && linkType == TypeTun && link == n.tunLink:
		n.detachRuntime(true, false)
		n.tunLink = 0
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS && link == n.dnsLink:
		n.detachRuntime(false, true)
		n.dnsLink = 0
	}
	return n.updateLabel()
}

func (n *tun2DNSNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunIn && receiverPortDirection == "left" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			EmitTunConfig(n.w, n.id, event.Link, TunConfig{})
			return nil
		}
		return n.acceptTun(event)
	}
	if event.ReceiverPort == n.dnsIn && receiverPortDirection == "right" && linkType == TypeDNS {
		if event.Link != n.dnsLink {
			return nil
		}
		return n.acceptDNS(event)
	}
	return nil
}

func (n *tun2DNSNode) acceptTun(event pasta.Event) error {
	var oldAdapter *dns.IPPacketAdapter
	var next tun.Tun
	var gen uint64
	start := false

	n.mu.Lock()
	accepted, changed := acceptTunPayload(event, n.tunLink, n.currentTun)
	if !changed {
		n.mu.Unlock()
		return nil
	}
	if n.adapter != nil {
		oldAdapter = n.adapter
		n.adapter = nil
	}
	next = accepted
	n.currentTun = next
	n.tunFailed = false
	n.lastErr = ""
	n.generation++
	gen = n.generation
	start = true
	if DNSIsReady(n.currentDNS) {
		n.adapter = n.newAdapterLocked(gen, next, n.currentDNS)
	}
	n.mu.Unlock()

	closeIPPacketAdapter(oldAdapter)
	BindTunResource(n.w, n.id, event.Link, next)
	if start {
		n.startTunPump(gen, next)
	}
	return n.updateLabel()
}

func (n *tun2DNSNode) acceptDNS(event pasta.Event) error {
	var oldAdapter *dns.IPPacketAdapter
	var currentTun tun.Tun
	var gen uint64

	n.mu.Lock()
	next, changed := acceptDNSPayload(event, n.currentDNS)
	if !changed {
		n.mu.Unlock()
		return nil
	}
	if n.adapter != nil {
		oldAdapter = n.adapter
		n.adapter = nil
	}
	n.currentDNS = next
	n.lastErr = ""
	if TunIsReady(n.currentTun) && !n.tunFailed {
		currentTun = n.currentTun
		gen = n.generation
		n.adapter = n.newAdapterLocked(gen, currentTun, next)
	}
	n.mu.Unlock()

	closeIPPacketAdapter(oldAdapter)
	BindDNSResource(n.w, n.id, event.Link, next)
	return n.updateLabel()
}

func (n *tun2DNSNode) detachRuntime(dropTun, dropDNS bool) {
	var oldAdapter *dns.IPPacketAdapter
	var oldTun tun.Tun
	var oldDNS dns.Interface

	n.mu.Lock()
	if n.adapter != nil {
		oldAdapter = n.adapter
		n.adapter = nil
	}
	if dropTun {
		oldTun = n.currentTun
		n.currentTun = nil
		n.tunFailed = false
		n.generation++
	}
	if dropDNS {
		oldDNS = n.currentDNS
		n.currentDNS = nil
	}
	n.lastErr = ""
	n.mu.Unlock()

	closeIPPacketAdapter(oldAdapter)
	_ = closeTun(oldTun)
	closeDNS(oldDNS)
}

func (n *tun2DNSNode) newAdapterLocked(gen uint64, t tun.Tun, upstream dns.Interface) *dns.IPPacketAdapter {
	return dns.NewIPPacketAdapter(upstream, func(pkt []byte) {
		n.writeResponsePacket(gen, t, pkt)
	})
}

func (n *tun2DNSNode) startTunPump(gen uint64, t tun.Tun) {
	run := func() { n.runTunPump(gen, t) }
	if err := n.SpawnWorker("tun2dns packet pump", run); err != nil {
		go run()
	}
}

func (n *tun2DNSNode) runTunPump(gen uint64, t tun.Tun) {
	if t == nil {
		return
	}
	batch := t.BatchSize()
	if batch <= 0 {
		batch = 1
	}
	packetSize := defaultTun2DNSPacketSize
	if mtu, err := t.MTU(); err == nil && mtu > packetSize {
		packetSize = mtu
	}
	offset := t.MRO()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = bufpool.GetBuffer(n.pool, offset+packetSize)
	}
	defer func() {
		for i := range bufs {
			bufpool.PutBuffer(n.pool, bufs[i])
		}
	}()

	for {
		count, err := t.Read(bufs, sizes, offset)
		if err != nil {
			if tun.IsTunTermError(err) {
				n.failTunPump(gen, t, err)
				return
			}
			continue
		}
		for i := 0; i < count && i < len(bufs) && i < len(sizes); i++ {
			size := sizes[i]
			if size <= 0 || offset+size > len(bufs[i]) {
				continue
			}
			adapter := n.adapterFor(gen, t)
			if adapter == nil {
				continue
			}
			adapter.FeedPacket(bufs[i][offset : offset+size])
		}
	}
}

func (n *tun2DNSNode) adapterFor(gen uint64, t tun.Tun) *dns.IPPacketAdapter {
	n.mu.Lock()
	defer n.mu.Unlock()
	if gen != n.generation || !SameTunInstance(n.currentTun, t) || n.tunFailed {
		return nil
	}
	return n.adapter
}

func (n *tun2DNSNode) writeResponsePacket(gen uint64, t tun.Tun, pkt []byte) {
	if n.adapterFor(gen, t) == nil {
		return
	}
	if err := writeRawTunPacket(t, n.pool, pkt); err != nil {
		n.failTunPump(gen, t, fmt.Errorf("write DNS response: %w", err))
	}
}

func (n *tun2DNSNode) failTunPump(gen uint64, t tun.Tun, err error) {
	var oldAdapter *dns.IPPacketAdapter
	updateLabel := false
	n.mu.Lock()
	if gen == n.generation && SameTunInstance(n.currentTun, t) {
		n.tunFailed = true
		n.lastErr = err.Error()
		oldAdapter = n.adapter
		n.adapter = nil
		updateLabel = true
	}
	n.mu.Unlock()
	closeIPPacketAdapter(oldAdapter)
	if updateLabel && n.w != nil && n.id != 0 {
		_ = n.w.SetNodeLabel(n.id, "TUN error")
	}
}

func (n *tun2DNSNode) updateLabel() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case n.tunFailed:
		if n.lastErr != "" {
			return n.SetLabel("TUN error")
		}
		return n.SetLabel("TUN error")
	case !TunIsReady(n.currentTun):
		return n.SetLabel("waiting for TUN")
	case !DNSIsReady(n.currentDNS):
		return n.SetLabel("waiting for DNS")
	default:
		return n.SetLabel("adapting DNS")
	}
}

func writeRawTunPacket(t tun.Tun, pool bufpool.Pool, pkt []byte) error {
	if t == nil {
		return errors.New("tun is nil")
	}
	offset := t.MWO()
	writeBuf := pkt
	if offset > 0 {
		buf := bufpool.GetBuffer(pool, offset+len(pkt))
		defer bufpool.PutBuffer(pool, buf)
		copy(buf[offset:], pkt)
		writeBuf = buf[:offset+len(pkt)]
	}
	written, err := t.Write([][]byte{writeBuf}, offset)
	if err != nil {
		return err
	}
	if written != 1 {
		return fmt.Errorf("short TUN write: %d of 1 packets", written)
	}
	return nil
}

func closeIPPacketAdapter(adapter *dns.IPPacketAdapter) {
	if adapter != nil {
		_ = adapter.Close()
	}
}
