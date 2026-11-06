package yggdrasilnodes

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/ygg/ygglib/autopeer"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/jumper"
	"github.com/asciimoth/ygg/ygglib/multicast"
)

// Public-peer documents use an attached carrier. A stalled source cannot keep
// node shutdown waiting without a bound read and handshake deadline.
type boundedFetchNetwork struct{ gonnect.Network }

func (n boundedFetchNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := n.Network.Dial(ctx, network, address)
	if err == nil {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	return conn, err
}

// Autopeer owns only URLs that it added. Manual URLs remain when autopeering
// is disabled or reconfigured.
type autoPeerCore struct {
	core  *core.Core
	mu    sync.Mutex
	owned map[string]struct{}
}

func (a *autoPeerCore) AddPeer(u *url.URL, intf string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	uri := u.String()
	if slices.Contains(a.core.PeerURIs(), uri) {
		return nil
	}
	if err := a.core.AddPeer(u, intf); err != nil {
		return err
	}
	a.owned[uri] = struct{}{}
	return nil
}
func (a *autoPeerCore) PeerURIs() []string          { return a.core.PeerURIs() }
func (a *autoPeerCore) ConnectedPeerURIs() []string { return a.core.ConnectedPeerURIs() }
func (a *autoPeerCore) removeOwned(manual map[string]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for uri := range a.owned {
		if !manual[uri] {
			u, _ := url.Parse(uri)
			_ = a.core.RemovePeer(u, "")
		}
	}
	a.owned = map[string]struct{}{}
}

type autoPeerSettings struct {
	sources, countries, schemes, fetchInterval, checkInterval string
	minimumConnected, minimumFromFetch                        int
}

func (c configuration) autoPeerSettings() autoPeerSettings {
	return autoPeerSettings{c.autoPeerSources, c.autoPeerCountries, c.autoPeerSchemes, c.autoPeerFetchInterval, c.autoPeerCheckInterval, c.autoPeerMinimumConnected, c.autoPeerMinimumFromFetch}
}

func duration(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	parsed, _ := time.ParseDuration(value) // validate rejects invalid values.
	return parsed
}

func (r *runtimeState) reconcileFeatures(d desired) error {
	if err := r.reconcileNodeInfo(d.config); err != nil {
		return err
	}
	if err := r.reconcileMulticast(d); err != nil {
		return err
	}
	r.reconcileAutoPeer(d)
	r.reconcileJumper(d.config)
	return nil
}

func (r *runtimeState) reconcileNodeInfo(c configuration) error {
	signature := fmt.Sprintf("%t:%q:%t:%q", c.nodeInfoPrivacy, c.nodeInfo, c.jumperEnabled, c.jumperAddresses)
	if signature == r.nodeInfoConfig {
		return nil
	}
	info, err := c.nodeInfoMap()
	if err != nil {
		return err
	}
	if c.jumperEnabled {
		for key, value := range jumper.AdvertisedNodeInfo(strings.Fields(c.jumperAddresses)) {
			info[key] = value
		}
	}
	if err := r.core.SetNodeInfo(info, c.nodeInfoPrivacy); err != nil {
		return err
	}
	r.nodeInfoConfig = signature
	return nil
}

type multicastCoreAdapter struct{ core *core.Core }

func (a multicastCoreAdapter) ListenLocal(u *url.URL, intf string) (multicast.Listener, error) {
	return a.core.ListenLocal(u, intf)
}
func (a multicastCoreAdapter) CallPeer(u *url.URL, intf string) error {
	return a.core.CallPeer(u, intf)
}
func (a multicastCoreAdapter) PublicKey() ed25519.PublicKey { return a.core.PublicKey() }

func (r *runtimeState) reconcileMulticast(d desired) error {
	network := d.networks["default"]
	if !links.NetworkIsReady(network) {
		network = nil
	}
	if r.multicast != nil && (r.multicastConfig != d.config.multicastInterfaces || !links.SameNetworkInstance(r.multicastNetwork, network)) {
		_ = r.multicast.Stop()
		r.multicast = nil
		r.multicastNetwork = nil
		r.multicastConfig = ""
	}
	if network == nil || d.config.multicastInterfaces == "" || r.multicast != nil {
		return nil
	}
	interfaces, err := d.config.multicastOptions()
	if err != nil {
		return err
	}
	if len(interfaces) == 0 {
		return nil
	}
	options := []multicast.SetupOption{
		multicast.Network{Network: network},
		multicast.ProtocolVersion{Major: core.ProtocolVersionMajor, Minor: core.ProtocolVersionMinor},
	}
	for _, intf := range interfaces {
		options = append(options, multicast.MulticastInterface{
			Regex: regexp.MustCompile(intf.Regex), Beacon: intf.Beacon, Listen: intf.Listen,
			Port: uint16(intf.Port), Priority: uint8(intf.Priority),
		})
	}
	m, err := multicast.New(multicastCoreAdapter{r.core}, r.log, options...)
	if err != nil {
		if m != nil {
			_ = m.Stop()
		}
		return fmt.Errorf("start link-local discovery: %w", err)
	}
	r.multicast = m
	r.multicastNetwork = network
	r.multicastConfig = d.config.multicastInterfaces
	return nil
}

func (r *runtimeState) reconcileAutoPeer(d desired) {
	if !d.config.autoPeerEnabled {
		if r.autoPeer != nil {
			r.autoPeer.SetPeerManager(nil)
			_ = r.autoPeer.Close()
			if r.autoPeerAdapter != nil {
				r.autoPeerAdapter.removeOwned(r.peers)
			}
			r.autoPeer = nil
			r.autoPeerAdapter = nil
			r.autoPeerNetworks = nil
			r.autoPeerConfig = autoPeerSettings{}
		}
		return
	}
	settings := d.config.autoPeerSettings()
	if r.autoPeer == nil {
		fetcher := autopeer.NewFetcher(r.log, duration(settings.fetchInterval, time.Hour))
		r.autoPeer = autopeer.NewManager(fetcher)
		r.autoPeerAdapter = &autoPeerCore{core: r.core, owned: map[string]struct{}{}}
		r.autoPeer.SetPeerManager(r.autoPeerAdapter)
	}
	fetcher := r.autoPeer.Fetcher()
	sources := strings.Fields(settings.sources)
	networks := map[string]nodekit.NetworkCloser{"default": d.networks["default"]}
	for _, source := range sources {
		if source != autopeer.BuiltinSource {
			networks[source] = sourceNetwork(source, d.networks)
		}
	}
	for source, network := range networks {
		if previous, exists := r.autoPeerNetworks[source]; !exists || !links.SameNetworkInstance(previous, network) {
			if source == "default" {
				if network == nil {
					fetcher.SetDefaultNetwork(nil)
				} else {
					fetcher.SetDefaultNetwork(boundedFetchNetwork{network})
				}
			} else {
				if network == nil {
					fetcher.SetSourceNetwork(source, nil)
				} else {
					fetcher.SetSourceNetwork(source, boundedFetchNetwork{network})
				}
			}
		}
	}
	for source := range r.autoPeerNetworks {
		if source != "default" {
			if _, exists := networks[source]; !exists {
				fetcher.RemoveSourceNetwork(source)
			}
		}
	}
	r.autoPeerNetworks = networks
	if settings != r.autoPeerConfig {
		fetcher.SetInterval(duration(settings.fetchInterval, time.Hour))
		fetcher.SetSources(sources)
		r.autoPeer.SetConfig(autopeer.ManagerConfig{
			CheckInterval:    duration(settings.checkInterval, time.Minute),
			MinimumConnected: settings.minimumConnected, MinimumConnectedFromFetch: settings.minimumFromFetch,
			Countries: strings.Fields(settings.countries), TransportSchemes: strings.Fields(settings.schemes),
		})
		r.autoPeerConfig = settings
	}
	r.autoPeer.Start()
}

func sourceNetwork(source string, networks map[string]nodekit.NetworkCloser) nodekit.NetworkCloser {
	u, err := url.Parse(source)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	bestPattern, bestScore, bestWildcards := "", -1, 0
	for pattern := range networks {
		if pattern == "default" {
			continue
		}
		matched, _ := path.Match(pattern, host)
		if !matched {
			continue
		}
		score, wildcards := 0, 0
		for _, char := range pattern {
			if char == '*' || char == '?' || char == '[' || char == ']' {
				wildcards++
			} else {
				score++
			}
		}
		if score > bestScore || (score == bestScore && wildcards < bestWildcards) || (score == bestScore && wildcards == bestWildcards && pattern < bestPattern) {
			bestPattern, bestScore, bestWildcards = pattern, score, wildcards
		}
	}
	if bestScore >= 0 {
		return networks[bestPattern]
	}
	return networks["default"]
}

func (r *runtimeState) reconcileJumper(c configuration) {
	if !c.jumperEnabled {
		if r.jumper != nil {
			r.jumperNotify.Store(nil)
			_ = r.jumper.Close()
			r.jumper = nil
		}
		return
	}
	config := jumper.Config{
		CheckInterval: duration(c.jumperCheckInterval, 10*time.Second),
		LinkTimeout:   duration(c.jumperLinkTimeout, 30*time.Second),
	}
	if r.jumper == nil {
		r.jumper = jumper.NewManager(r.core, r.log, config)
		r.jumperConfig = config
		r.jumperNotify.Store(r.jumper)
		r.jumper.Start()
	} else if config != r.jumperConfig {
		r.jumper.SetConfig(config)
		r.jumperConfig = config
	}
}
