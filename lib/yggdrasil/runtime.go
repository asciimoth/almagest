package yggdrasilnodes

import (
	"crypto/ed25519"
	"fmt"
	"maps"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/ygg/ygglib/autopeer"
	yconfig "github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/ipv6rwc"
	"github.com/asciimoth/ygg/ygglib/jumper"
	"github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/multicast"
	"github.com/asciimoth/ygg/ygglib/transport"
	ytun "github.com/asciimoth/ygg/ygglib/tun"
)

type desired struct {
	generation uint64
	config     configuration
	key        ed25519.PrivateKey
	device     tun.Tun
	networks   map[string]nodekit.NetworkCloser
}
type result struct {
	generation          uint64
	address, state      string
	requestTun          bool
	requestNetwork      bool
	connectedPeers      int
	autoPeerCandidates  int
	linkLocalInterfaces int
	jumperActive        bool
}

type runtimeState struct {
	log              core.Logger
	core             *core.Core
	manager          *transport.Manager
	adapter          *ytun.TunAdapter
	key              ed25519.PrivateKey
	device           tun.Tun
	failedTun        tun.Tun
	networks         map[string]nodekit.NetworkCloser
	peers            map[string]bool
	listeners        map[string]*core.Listener
	multicast        *multicast.Multicast
	multicastNetwork nodekit.NetworkCloser
	multicastConfig  string
	autoPeer         *autopeer.Manager
	autoPeerAdapter  *autoPeerCore
	autoPeerConfig   autoPeerSettings
	autoPeerNetworks map[string]nodekit.NetworkCloser
	jumper           *jumper.Manager
	jumperConfig     jumper.Config
	jumperNotify     *atomic.Pointer[jumper.Manager]
	nodeInfoConfig   string
}

func (r *runtimeState) close() {
	if r.jumperNotify != nil {
		r.jumperNotify.Store(nil)
	}
	if r.jumper != nil {
		_ = r.jumper.Close()
	}
	if r.autoPeer != nil {
		r.autoPeer.SetPeerManager(nil)
		_ = r.autoPeer.Close()
	}
	if r.multicast != nil {
		_ = r.multicast.Stop()
	}
	if r.adapter != nil {
		_ = r.adapter.Stop()
	}
	if r.core != nil {
		r.core.Stop()
	}
	if r.manager != nil {
		_ = r.manager.Close()
	}
	*r = runtimeState{log: r.log}
}
func (r *runtimeState) start(key ed25519.PrivateKey) error {
	cfg := &yconfig.NodeConfig{PrivateKey: yconfig.KeyBytes(key)}
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		return err
	}
	m := transport.NewManager(nil)
	if err := m.RegisterTransport(transport.NewTCPTransport()); err != nil {
		return err
	}
	tlsConfig, err := core.GenerateTLSConfig(cfg.Certificate)
	if err != nil {
		return err
	}
	if err = m.RegisterTransport(transport.NewTLSTransport(tlsConfig)); err != nil {
		return err
	}
	if r.log == nil {
		r.log = logger.Discard()
	}
	c, err := core.New(cfg.Certificate, r.log, core.TransportManager{Manager: m})
	if err != nil {
		_ = m.Close()
		return err
	}
	a, err := ytun.New(ipv6rwc.NewReadWriteCloser(c), r.log)
	if err != nil {
		c.Stop()
		_ = m.Close()
		return err
	}
	r.core = c
	r.manager = m
	r.adapter = a
	r.key = key
	r.peers = map[string]bool{}
	r.listeners = map[string]*core.Listener{}
	r.jumperNotify = &atomic.Pointer[jumper.Manager]{}
	notify := r.jumperNotify
	c.AddPathNotify(func(key ed25519.PublicKey) {
		if manager := notify.Load(); manager != nil {
			manager.NotifyTraffic(key)
		}
	})
	return nil
}
func (r *runtimeState) reconcile(d desired) result {
	out := result{generation: d.generation, state: "disabled"}
	if !d.config.enabled || len(d.key) == 0 {
		r.close()
		if d.config.enabled {
			out.state = "identity unavailable"
		}
		return out
	}
	if r.core != nil && !r.key.Equal(d.key) {
		r.close()
	}
	if r.core == nil {
		if err := r.start(d.key); err != nil {
			out.state = "error: " + err.Error()
			return out
		}
	}
	out.address = r.core.Address().String()
	changed := !maps.EqualFunc(r.networks, d.networks, links.SameNetworkInstance)
	if changed {
		// Stop only listeners that the transport manager will invalidate. An
		// unrelated mapped carrier must not interrupt existing inbound peers.
		for raw, l := range r.listeners {
			if listenerCarrierChanged(raw, r.networks, d.networks) {
				l.Stop()
				delete(r.listeners, raw)
			}
		}
		if !links.SameNetworkInstance(r.networks["default"], d.networks["default"]) {
			r.manager.SetDefaultNetwork(d.networks["default"])
		}
		for pattern := range r.networks {
			if pattern != "default" {
				if _, ok := d.networks[pattern]; !ok {
					_ = r.manager.UnmapNetwork(pattern)
				}
			}
		}
		for pattern, network := range d.networks {
			if previous, exists := r.networks[pattern]; pattern != "default" && (!exists || !links.SameNetworkInstance(previous, network)) {
				_ = r.manager.MapNetwork(pattern, network)
			}
		}
		r.networks = maps.Clone(d.networks)
		r.core.RetryPeersNow()
	}
	wantedPeers := map[string]bool{}
	for _, raw := range strings.Fields(d.config.peers) {
		wantedPeers[raw] = true
	}
	for raw := range r.peers {
		if !wantedPeers[raw] {
			u, _ := url.Parse(raw)
			_ = r.core.RemovePeer(u, "")
			delete(r.peers, raw)
		}
	}
	var problem error
	for raw := range wantedPeers {
		if !r.peers[raw] {
			u, _ := url.Parse(raw)
			if err := r.core.AddPeer(u, ""); err != nil {
				problem = err
			} else {
				r.peers[raw] = true
			}
		}
	}
	wantedListeners := map[string]bool{}
	for _, raw := range strings.Fields(d.config.listen) {
		wantedListeners[raw] = true
	}
	for raw, l := range r.listeners {
		if !wantedListeners[raw] {
			l.Stop()
			delete(r.listeners, raw)
		}
	}
	for raw := range wantedListeners {
		if r.listeners[raw] == nil {
			u, _ := url.Parse(raw)
			l, err := r.core.Listen(u, "")
			if err != nil {
				problem = fmt.Errorf("listen %s: %w", raw, err)
			} else {
				r.listeners[raw] = l
			}
		}
	}
	if err := r.reconcileFeatures(d); err != nil && problem == nil {
		problem = err
	}
	out.connectedPeers = len(r.core.ConnectedPeerURIs())
	if r.autoPeer != nil {
		out.autoPeerCandidates = len(r.autoPeer.Peers())
	}
	if r.multicast != nil {
		out.linkLocalInterfaces = len(r.multicast.InterfaceStates())
	}
	out.jumperActive = r.jumper != nil && r.jumper.IsStarted()
	if r.device != nil && (!r.adapter.Status().Attached || !links.TunIsReady(r.device)) {
		r.failedTun = r.device
		r.device = nil
	}
	if !links.SameTunInstance(r.device, d.device) {
		_ = r.adapter.Detach()
		r.device = nil
		if d.device != nil && links.TunIsReady(d.device) && !links.SameTunInstance(r.failedTun, d.device) {
			if err := r.adapter.Attach(d.device); err != nil {
				problem = err
				r.failedTun = d.device
			} else {
				r.device = d.device
				r.failedTun = nil
			}
		}
	}
	out.state = "ready"
	if d.device == nil {
		out.state = "waiting for TUN"
	} else if r.device == nil {
		out.state = "waiting for TUN"
		out.requestTun = true
	} else if !r.adapter.Status().Enabled {
		out.state = "TUN down"
	} else {
		out.state = "up"
	}
	hasNetwork := false
	for _, network := range d.networks {
		hasNetwork = hasNetwork || links.NetworkIsReady(network)
		if network != nil && !links.NetworkIsReady(network) {
			out.requestNetwork = true
		}
	}
	if !hasNetwork {
		out.state = "waiting for network"
	}
	if problem != nil {
		out.state = "error: " + problem.Error()
	}
	return out
}
func (n *node) run() {
	defer close(n.done)
	runtime := runtimeState{log: n.Logger}
	defer runtime.close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var current desired
	var previous result
	for {
		select {
		case <-n.stop:
			return
		case current = <-n.updates:
		case <-ticker.C:
			if current.generation == 0 {
				continue
			}
		}
		select {
		case <-n.stop:
			return
		default:
		}
		next := runtime.reconcile(current)
		if next != previous || next.requestTun || next.requestNetwork {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: next})
			previous = next
		}
	}
}

func listenerCarrierChanged(raw string, before, after map[string]nodekit.NetworkCloser) bool {
	u, _ := url.Parse(raw)
	host := strings.ToLower(u.Hostname())
	mapped := false
	for pattern, network := range before {
		if pattern == "default" {
			continue
		}
		matches, _ := path.Match(pattern, host)
		if matches {
			mapped = true
			next, exists := after[pattern]
			if !exists || !links.SameNetworkInstance(network, next) {
				return true
			}
		}
	}
	for pattern, network := range after {
		if pattern == "default" {
			continue
		}
		matches, _ := path.Match(pattern, host)
		if matches {
			mapped = true
			previous, exists := before[pattern]
			if !exists || !links.SameNetworkInstance(network, previous) {
				return true
			}
		}
	}
	return !mapped && !links.SameNetworkInstance(before["default"], after["default"])
}
