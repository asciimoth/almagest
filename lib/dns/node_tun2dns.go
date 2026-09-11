package dnsnodes

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeTun2Dns is the node class name for a TUN-to-DNS packet adapter.
	NodeTypeTun2Dns = "almagest/Tun2Dns"

	defaultTun2DNSPacketSize = 1500
)

// Tun2DNSClass builds TUN-to-DNS packet adapter nodes.
type Tun2DNSClass struct {
	Runtime nodekit.Runtime
}

func (Tun2DNSClass) ClassName() string        { return NodeTypeTun2Dns }
func (Tun2DNSClass) ShortDescription() string { return "TUN to DNS" }
func (Tun2DNSClass) LongDescription() string {
	return "Adapts raw UDP DNS packets received from an almagest/tun endpoint into requests on an almagest/dns interface, then writes raw IP DNS responses back to the TUN."
}
func (Tun2DNSClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: nodekit.TypeTun, InitialPorts: []pasta.Port{
		nodekit.TunPort("left", "Tun"),
		DNSPort("right", "DNS"),
	}}
}
func (c Tun2DNSClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	base := nodekit.Base{Runtime: c.Runtime}
	return &tun2DNSNode{Base: base, pool: base.Pool()}, nil
}

// tun2DNSNode is a two-input adapter around gonnect/dns.IPPacketAdapter. The
// left-directed TUN port supplies the packet stream and the right-directed DNS
// port supplies the upstream resolver. One reader goroutine is active for the
// current TUN wrapper; replacing the DNS input swaps only the IPPacketAdapter,
// avoiding competing reads from the same TUN.
type tun2DNSNode struct {
	nodekit.Base

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
	tunRetry   *time.Timer
}

const tun2DNSRetryDelay = 50 * time.Millisecond

type tun2DNSRetry struct {
	link       uint64
	generation uint64
}

func (n *tun2DNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.tunIn = nodekit.RestoredLeft(restored, 0)
	n.dnsIn = nodekit.RestoredRight(restored, 0)
	if err := n.SetPrimary(nodekit.TypeTun); err != nil {
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
	n.stopTunRetry()
	n.detachRuntime(true, true)
}

func (n *tun2DNSNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunIn:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return nodekit.TunPreLinkAdd(n.W, port, linkType)
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
	case port == n.tunIn && portDirection == "left" && linkType == nodekit.TypeTun:
		n.tunLink = link
		std.RequestLocked(n.W, n.ID, link)
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS:
		n.dnsLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *tun2DNSNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunIn && portDirection == "left" && linkType == nodekit.TypeTun && link == n.tunLink:
		n.stopTunRetry()
		n.detachRuntime(true, false)
		n.tunLink = 0
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS && link == n.dnsLink:
		n.detachRuntime(false, true)
		n.dnsLink = 0
	}
	return n.updateLabel()
}

func (n *tun2DNSNode) OnInbox(message pasta.InboxMessage) error {
	retry, ok := message.Payload.(tun2DNSRetry)
	if !ok {
		return nil
	}
	n.mu.Lock()
	current := retry.link == n.tunLink && retry.generation == n.generation && n.tunFailed
	if current {
		n.scheduleTunRetryLocked(retry.link, retry.generation)
	}
	n.mu.Unlock()
	if current {
		if n.Logger != nil {
			n.Logger.Debugf("requesting replacement TUN: link=%d generation=%d", retry.link, retry.generation)
		}
		std.RequestLocked(n.W, n.ID, retry.link)
	}
	return nil
}

func (n *tun2DNSNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunIn && receiverPortDirection == "left" && linkType == nodekit.TypeTun {
		if std.IsRequest(event.Payload) {
			nodekit.EmitTunConfig(n.W, n.ID, event.Link, nodekit.TunConfig{})
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
	accepted, changed := nodekit.AcceptTunPayloadValue(event, n.tunLink, n.currentTun)
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
	n.stopTunRetryLocked()
	n.generation++
	gen = n.generation
	start = true
	if DNSIsReady(n.currentDNS) {
		n.adapter = n.newAdapterLocked(gen, next, n.currentDNS)
	}
	n.mu.Unlock()

	closeIPPacketAdapter(oldAdapter)
	nodekit.BindTunResource(n.W, n.ID, event.Link, next)
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
	next, changed := AcceptDNSPayloadValue(event, n.currentDNS)
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
	if nodekit.TunIsReady(n.currentTun) && !n.tunFailed {
		currentTun = n.currentTun
		gen = n.generation
		n.adapter = n.newAdapterLocked(gen, currentTun, next)
	}
	n.mu.Unlock()

	closeIPPacketAdapter(oldAdapter)
	BindDNSResource(n.W, n.ID, event.Link, next)
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
		n.stopTunRetryLocked()
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
	_ = nodekit.CloseTun(oldTun)
	CloseDNS(oldDNS)
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
	if gen != n.generation || !nodekit.SameTunInstance(n.currentTun, t) || n.tunFailed {
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
	var retryLink uint64
	updateLabel := false
	n.mu.Lock()
	if gen == n.generation && nodekit.SameTunInstance(n.currentTun, t) {
		n.tunFailed = true
		n.lastErr = err.Error()
		oldAdapter = n.adapter
		n.adapter = nil
		retryLink = n.tunLink
		n.scheduleTunRetryLocked(retryLink, gen)
		updateLabel = true
	}
	n.mu.Unlock()
	closeIPPacketAdapter(oldAdapter)
	if updateLabel && n.W != nil && n.ID != 0 {
		if n.Logger != nil {
			n.Logger.Warnf(
				"TUN packet pump failed: link=%d generation=%d retry_scheduled=%t terminal=%t error=%v input={%s}",
				retryLink,
				gen,
				retryLink != 0,
				tun.IsTunTermError(err),
				err,
				nodekit.TunDebugString(t),
			)
		}
		n.W.Lock()
		n.mu.Lock()
		current := gen == n.generation && nodekit.SameTunInstance(n.currentTun, t) && n.tunFailed
		n.mu.Unlock()
		if current {
			_ = n.updateLabel()
		}
		n.W.Unlock()
	}
}

func (n *tun2DNSNode) scheduleTunRetryLocked(link, generation uint64) {
	n.stopTunRetryLocked()
	if link == 0 || n.W == nil || n.ID == 0 {
		return
	}
	w := n.W
	node := n.ID
	n.tunRetry = time.AfterFunc(tun2DNSRetryDelay, func() {
		w.SendInbox(pasta.InboxMessage{
			ReceiverNode: node,
			Payload: tun2DNSRetry{
				link:       link,
				generation: generation,
			},
		})
	})
}

func (n *tun2DNSNode) stopTunRetry() {
	n.mu.Lock()
	n.stopTunRetryLocked()
	n.mu.Unlock()
}

func (n *tun2DNSNode) stopTunRetryLocked() {
	if n.tunRetry == nil {
		return
	}
	n.tunRetry.Stop()
	n.tunRetry = nil
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
	case !nodekit.TunIsReady(n.currentTun):
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
