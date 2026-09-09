package links

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/gonnect"
)

const (
	// IPv4UDPOverhead is the size of an IPv4 header and a UDP header when the
	// IPv4 header has no options.
	IPv4UDPOverhead = 20 + 8
	// IPv6UDPOverhead is the size of an IPv6 header and a UDP header when the
	// packet has no extension headers.
	IPv6UDPOverhead = 40 + 8
	// WireGuardDataOverhead is the WireGuard transport header and
	// authentication tag added to one data message.
	WireGuardDataOverhead = 16 + 16
	// IPv6MinimumLinkMTU is the minimum IPv6 link MTU.
	IPv6MinimumLinkMTU = 1280
)

// MTUConstraint explains one positive MTU limit. Path lists the nodes that
// contributed to the limit and is also used to reject dependency cycles.
type MTUConstraint struct {
	Value      int
	SourceNode uint64
	Source     string
	Reason     string
	InputValue int
	Overhead   int
	Result     int
	Path       []uint64
}

// Clone returns an independent constraint copy.
func (c MTUConstraint) Clone() MTUConstraint {
	c.Path = slices.Clone(c.Path)
	return c
}

// IsKnown reports whether the constraint has a positive value.
func (c MTUConstraint) IsKnown() bool { return c.Value > 0 }

// ContainsNode reports whether node already occurs in the dependency path.
func (c MTUConstraint) ContainsNode(node uint64) bool {
	return node != 0 && slices.Contains(c.Path, node)
}

// WithNode returns a copy with node added to the dependency path. It rejects a
// path that already contains node.
func (c MTUConstraint) WithNode(node uint64) (MTUConstraint, error) {
	c = c.Clone()
	if node == 0 {
		return c, nil
	}
	if c.ContainsNode(node) {
		return MTUConstraint{}, fmt.Errorf("MTU dependency cycle contains node %d", node)
	}
	c.Path = append(c.Path, node)
	return c, nil
}

// PositiveMinimum returns the smallest positive value. Zero and negative
// values are unknown and do not participate in the minimum.
func PositiveMinimum(values ...int) int {
	minimum := 0
	for _, value := range values {
		if value > 0 && (minimum == 0 || value < minimum) {
			minimum = value
		}
	}
	return minimum
}

// MinimumMTUConstraint returns the smallest known constraint. Equal values
// keep their input order.
func MinimumMTUConstraint(values ...MTUConstraint) MTUConstraint {
	var minimum MTUConstraint
	for _, value := range values {
		if value.Value > 0 && (minimum.Value == 0 || value.Value < minimum.Value) {
			minimum = value.Clone()
		}
	}
	return minimum
}

// UDPPayloadFromPathMTU converts an outer layer-3 path MTU to its maximum UDP
// payload. IPv4 assumes no options. IPv6 assumes no extension headers.
func UDPPayloadFromPathMTU(pathMTU int, ipv6 bool) int {
	if pathMTU <= 0 {
		return 0
	}
	overhead := IPv4UDPOverhead
	if ipv6 {
		overhead = IPv6UDPOverhead
	}
	if pathMTU <= overhead {
		return 0
	}
	return pathMTU - overhead
}

// WireGuardInnerMTU returns the maximum safe WireGuard plaintext packet for a
// carrier UDP payload. amneziaS4 is the prefix added after encryption.
func WireGuardInnerMTU(maxUDPPayload, amneziaS4 int) int {
	if maxUDPPayload <= 0 || amneziaS4 < 0 {
		return 0
	}
	overhead := WireGuardDataOverhead + amneziaS4
	if maxUDPPayload <= overhead {
		return 0
	}
	return maxUDPPayload - overhead
}

// AmneziaInitiationPacketSize returns the datagram size described by one
// Amnezia custom initiation-packet specification.
func AmneziaInitiationPacketSize(spec string) (int, error) {
	total := 0
	remaining := spec
	for {
		start := strings.IndexByte(remaining, '<')
		if start < 0 {
			return total, nil
		}
		end := strings.IndexByte(remaining[start:], '>')
		if end < 0 {
			return 0, errors.New("missing enclosing >")
		}
		end += start
		fields := strings.Fields(remaining[start+1 : end])
		if len(fields) == 0 {
			return 0, errors.New("empty tag")
		}
		size := 0
		switch fields[0] {
		case "t":
			size = 4
		case "b":
			if len(fields) != 2 {
				return 0, errors.New("byte tag needs one argument")
			}
			value := strings.TrimPrefix(fields[1], "0x")
			if len(value)%2 != 0 {
				return 0, errors.New("byte tag has an odd number of symbols")
			}
			size = len(value) / 2
		case "r", "rc", "rd":
			if len(fields) != 2 {
				return 0, fmt.Errorf("%s tag needs one length argument", fields[0])
			}
			value, err := strconv.Atoi(fields[1])
			if err != nil || value < 0 {
				return 0, fmt.Errorf("%s tag has invalid length %q", fields[0], fields[1])
			}
			size = value
		default:
			return 0, fmt.Errorf("unknown tag <%s>", fields[0])
		}
		total += size
		remaining = remaining[end+1:]
	}
}

// DatagramMTUResult describes a maximum UDP payload for one destination.
type DatagramMTUResult struct {
	MaxPayload  int
	Known       bool
	IPv6        bool
	Destination string
	Constraint  MTUConstraint
}

// DatagramMTUProvider supplies destination-aware maximum UDP payloads.
// Revision changes when a later query can produce a materially different
// result.
type DatagramMTUProvider interface {
	MaxUDPPayload(ctx context.Context, network, address string) (DatagramMTUResult, error)
	Revision() uint64
	SubscribeDatagramMTU(func(uint64)) func()
}

// DatagramMTULearner accepts a smaller observed datagram limit after a send
// fails with EMSGSIZE.
type DatagramMTULearner interface {
	LearnDatagramMTU(network, address string, ipv6 bool, attemptedSize int)
}

var networkDatagramMTUProviders sync.Map

// AttachDatagramMTUProvider associates provider with network and returns the
// same network. Keeping the identity unchanged preserves established wrapper
// contracts while NetworkPayload carries the capability explicitly.
func AttachDatagramMTUProvider(network gonnect.Network, provider DatagramMTUProvider) gonnect.Network {
	if network == nil {
		return network
	}
	key, ok := comparableCapabilityKey(network)
	if !ok {
		return network
	}
	if provider == nil {
		networkDatagramMTUProviders.Delete(key)
	} else {
		networkDatagramMTUProviders.Store(key, provider)
	}
	return network
}

// DetachDatagramMTUProvider removes the capability association for network.
func DetachDatagramMTUProvider(network gonnect.Network) {
	key, ok := comparableCapabilityKey(network)
	if ok {
		networkDatagramMTUProviders.Delete(key)
	}
}

func comparableCapabilityKey(value any) (any, bool) {
	if value == nil {
		return nil, false
	}
	typeOf := reflect.TypeOf(value)
	return value, typeOf.Comparable()
}

// DatagramMTUProviderFromNetwork finds a capability on network or one of its
// wrappers. A direct native network gets a native route provider when no
// explicit provider exists.
func DatagramMTUProviderFromNetwork(network gonnect.Network) DatagramMTUProvider {
	for current := any(network); current != nil; current = gonnect.GetWrapped(current) {
		if key, ok := comparableCapabilityKey(current); ok {
			if provider, found := networkDatagramMTUProviders.Load(key); found {
				return provider.(DatagramMTUProvider)
			}
		}
		if provider, ok := current.(DatagramMTUProvider); ok {
			return provider
		}
	}
	if network != nil && network.IsNative() {
		provider := NewNativeDatagramMTUProvider(network)
		AttachDatagramMTUProvider(network, provider)
		return provider
	}
	return nil
}

type datagramMTUSubscriptions struct {
	mu     sync.Mutex
	nextID uint64
	items  map[uint64]func(uint64)
}

// MinimumDatagramMTUProvider combines the capabilities of networks that can
// carry the same operation. It reports the smallest known limit. If one active
// network has no known capability, the combined result is unknown.
type MinimumDatagramMTUProvider struct {
	mu          sync.RWMutex
	providers   map[uint64]DatagramMTUProvider
	unsubscribe map[uint64]func()
	revision    uint64
	subs        datagramMTUSubscriptions
}

// NewMinimumDatagramMTUProvider creates an empty composite provider.
func NewMinimumDatagramMTUProvider() *MinimumDatagramMTUProvider {
	return &MinimumDatagramMTUProvider{
		providers:   make(map[uint64]DatagramMTUProvider),
		unsubscribe: make(map[uint64]func()),
	}
}

// SetProvider adds or replaces one active path capability. A nil provider is
// retained as an unknown active path.
func (p *MinimumDatagramMTUProvider) SetProvider(key uint64, provider DatagramMTUProvider) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if current, ok := p.providers[key]; ok && SameDatagramMTUProvider(current, provider) {
		p.mu.Unlock()
		return
	}
	if unsubscribe := p.unsubscribe[key]; unsubscribe != nil {
		unsubscribe()
	}
	p.providers[key] = provider
	delete(p.unsubscribe, key)
	if provider != nil {
		p.unsubscribe[key] = provider.SubscribeDatagramMTU(func(uint64) {
			p.providerChanged(key, provider)
		})
	}
	p.revision++
	revision := p.revision
	p.mu.Unlock()
	p.subs.notify(revision)
}

// RemoveProvider removes one active path capability.
func (p *MinimumDatagramMTUProvider) RemoveProvider(key uint64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if _, ok := p.providers[key]; !ok {
		p.mu.Unlock()
		return
	}
	if unsubscribe := p.unsubscribe[key]; unsubscribe != nil {
		unsubscribe()
	}
	delete(p.providers, key)
	delete(p.unsubscribe, key)
	p.revision++
	revision := p.revision
	p.mu.Unlock()
	p.subs.notify(revision)
}

// Close removes every child subscription.
func (p *MinimumDatagramMTUProvider) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	for _, unsubscribe := range p.unsubscribe {
		if unsubscribe != nil {
			unsubscribe()
		}
	}
	p.providers = make(map[uint64]DatagramMTUProvider)
	p.unsubscribe = make(map[uint64]func())
	p.revision++
	revision := p.revision
	p.mu.Unlock()
	p.subs.notify(revision)
}

func (p *MinimumDatagramMTUProvider) providerChanged(key uint64, provider DatagramMTUProvider) {
	p.mu.Lock()
	if !SameDatagramMTUProvider(p.providers[key], provider) {
		p.mu.Unlock()
		return
	}
	p.revision++
	revision := p.revision
	p.mu.Unlock()
	p.subs.notify(revision)
}

// Revision returns the combined capability revision.
func (p *MinimumDatagramMTUProvider) Revision() uint64 {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.revision
}

// SubscribeDatagramMTU subscribes to a change in this provider or an active
// child provider.
func (p *MinimumDatagramMTUProvider) SubscribeDatagramMTU(fn func(uint64)) func() {
	if p == nil {
		return func() {}
	}
	return p.subs.subscribe(fn)
}

// MaxUDPPayload queries every active path and returns the conservative result.
func (p *MinimumDatagramMTUProvider) MaxUDPPayload(ctx context.Context, network, address string) (DatagramMTUResult, error) {
	if p == nil {
		return DatagramMTUResult{}, nil
	}
	p.mu.RLock()
	providers := make([]DatagramMTUProvider, 0, len(p.providers))
	unknown := false
	for _, provider := range p.providers {
		if provider == nil {
			unknown = true
		}
		providers = append(providers, provider)
	}
	p.mu.RUnlock()
	if len(providers) == 0 || unknown {
		return DatagramMTUResult{}, nil
	}
	var selected DatagramMTUResult
	for _, provider := range providers {
		result, err := provider.MaxUDPPayload(ctx, network, address)
		if err != nil {
			return DatagramMTUResult{}, err
		}
		if !result.Known || result.MaxPayload <= 0 {
			return DatagramMTUResult{}, nil
		}
		if !selected.Known || result.MaxPayload < selected.MaxPayload {
			selected = result
		}
	}
	return selected, nil
}

// LearnDatagramMTU forwards a learned limit to all active paths. This is
// conservative when the router cannot identify which path sent the datagram.
func (p *MinimumDatagramMTUProvider) LearnDatagramMTU(network, address string, ipv6 bool, attemptedSize int) {
	if p == nil {
		return
	}
	p.mu.RLock()
	providers := make([]DatagramMTUProvider, 0, len(p.providers))
	for _, provider := range p.providers {
		providers = append(providers, provider)
	}
	p.mu.RUnlock()
	for _, provider := range providers {
		if learner, ok := provider.(DatagramMTULearner); ok {
			learner.LearnDatagramMTU(network, address, ipv6, attemptedSize)
		}
	}
}

func (s *datagramMTUSubscriptions) subscribe(fn func(uint64)) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.items == nil {
		s.items = make(map[uint64]func(uint64))
	}
	s.nextID++
	id := s.nextID
	s.items[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.items, id)
		s.mu.Unlock()
	}
}

func (s *datagramMTUSubscriptions) notify(revision uint64) {
	s.mu.Lock()
	callbacks := make([]func(uint64), 0, len(s.items))
	for _, fn := range s.items {
		callbacks = append(callbacks, fn)
	}
	s.mu.Unlock()
	for _, fn := range callbacks {
		fn(revision)
	}
}

// Layer3DatagramMTUProvider converts one mutable link MTU to IPv4 and IPv6 UDP
// payload limits. It is used by VTun network outputs.
type Layer3DatagramMTUProvider struct {
	mu         sync.RWMutex
	mtu        int
	hasIPv4    bool
	hasIPv6    bool
	revision   uint64
	constraint MTUConstraint
	subs       datagramMTUSubscriptions
}

// NewLayer3DatagramMTUProvider creates a link-MTU provider.
func NewLayer3DatagramMTUProvider(mtu int, hasIPv4, hasIPv6 bool, constraint MTUConstraint) *Layer3DatagramMTUProvider {
	provider := &Layer3DatagramMTUProvider{}
	provider.SetMTU(mtu, hasIPv4, hasIPv6, constraint)
	return provider
}

// SetMTU updates the link limit and notifies subscribers after a material
// change.
func (p *Layer3DatagramMTUProvider) SetMTU(mtu int, hasIPv4, hasIPv6 bool, constraint MTUConstraint) {
	if p == nil {
		return
	}
	constraint = constraint.Clone()
	p.mu.Lock()
	if p.mtu == mtu && p.hasIPv4 == hasIPv4 && p.hasIPv6 == hasIPv6 && mtuConstraintsEqual(p.constraint, constraint) {
		p.mu.Unlock()
		return
	}
	p.mtu = mtu
	p.hasIPv4 = hasIPv4
	p.hasIPv6 = hasIPv6
	p.constraint = constraint
	p.revision++
	revision := p.revision
	p.mu.Unlock()
	p.subs.notify(revision)
}

// Revision returns the current capability revision.
func (p *Layer3DatagramMTUProvider) Revision() uint64 {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.revision
}

// SubscribeDatagramMTU subscribes to material limit changes.
func (p *Layer3DatagramMTUProvider) SubscribeDatagramMTU(fn func(uint64)) func() {
	if p == nil {
		return func() {}
	}
	return p.subs.subscribe(fn)
}

// MaxUDPPayload returns the family-specific UDP payload derived from the link
// MTU. A host name uses the conservative minimum of all enabled families.
func (p *Layer3DatagramMTUProvider) MaxUDPPayload(_ context.Context, network, address string) (DatagramMTUResult, error) {
	if p == nil {
		return DatagramMTUResult{}, nil
	}
	p.mu.RLock()
	mtu, hasIPv4, hasIPv6, constraint := p.mtu, p.hasIPv4, p.hasIPv6, p.constraint.Clone()
	p.mu.RUnlock()
	if mtu <= 0 {
		return DatagramMTUResult{}, nil
	}
	family, destination := datagramAddressFamily(network, address)
	result := DatagramMTUResult{Destination: destination, Constraint: constraint}
	switch family {
	case 4:
		if !hasIPv4 {
			return result, nil
		}
		result.MaxPayload = UDPPayloadFromPathMTU(mtu, false)
	case 6:
		if !hasIPv6 {
			return result, nil
		}
		result.IPv6 = true
		result.MaxPayload = UDPPayloadFromPathMTU(mtu, true)
	default:
		values := make([]int, 0, 2)
		if hasIPv4 {
			values = append(values, UDPPayloadFromPathMTU(mtu, false))
		}
		if hasIPv6 {
			values = append(values, UDPPayloadFromPathMTU(mtu, true))
			result.IPv6 = true
		}
		result.MaxPayload = PositiveMinimum(values...)
	}
	result.Known = result.MaxPayload > 0
	if result.Known {
		result.Constraint.Value = result.MaxPayload
		result.Constraint.InputValue = mtu
		result.Constraint.Overhead = mtu - result.MaxPayload
		result.Constraint.Result = result.MaxPayload
		if result.Constraint.Source == "" {
			result.Constraint.Source = "virtual link"
		}
		result.Constraint.Reason = "maximum UDP payload"
	}
	return result, nil
}

// NativeDatagramMTUProvider uses the routed native interface as a baseline and
// keeps destination-specific limits learned from EMSGSIZE.
type NativeDatagramMTUProvider struct {
	network gonnect.Network

	mu       sync.RWMutex
	revision uint64
	learned  map[string]learnedDatagramLimit
	sequence uint64
	subs     datagramMTUSubscriptions
}

type learnedDatagramLimit struct {
	value    int
	expires  time.Time
	sequence uint64
}

const datagramMTULearnedHoldDown = 10 * time.Minute

// NewNativeDatagramMTUProvider creates a provider for a direct OS network.
func NewNativeDatagramMTUProvider(network gonnect.Network) *NativeDatagramMTUProvider {
	return &NativeDatagramMTUProvider{network: network, revision: 1, learned: make(map[string]learnedDatagramLimit)}
}

func (p *NativeDatagramMTUProvider) Revision() uint64 {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.revision
}

func (p *NativeDatagramMTUProvider) SubscribeDatagramMTU(fn func(uint64)) func() {
	if p == nil {
		return func() {}
	}
	return p.subs.subscribe(fn)
}

func (p *NativeDatagramMTUProvider) MaxUDPPayload(ctx context.Context, network, address string) (DatagramMTUResult, error) {
	if p == nil || p.network == nil {
		return DatagramMTUResult{}, nil
	}
	ips, destination, err := resolveDatagramDestination(ctx, p.network, network, address)
	if err != nil {
		return DatagramMTUResult{}, err
	}
	var results []DatagramMTUResult
	var queryErrs []error
	for _, ip := range ips {
		result, queryErr := p.maxUDPPayloadForIP(ctx, network, destination, ip)
		if queryErr != nil {
			queryErrs = append(queryErrs, queryErr)
			continue
		}
		if result.Known {
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return DatagramMTUResult{Destination: destination}, errors.Join(queryErrs...)
	}
	selected := results[0]
	for _, result := range results[1:] {
		if result.MaxPayload < selected.MaxPayload {
			selected = result
		}
	}
	return selected, nil
}

func (p *NativeDatagramMTUProvider) maxUDPPayloadForIP(ctx context.Context, network, destination string, ip netip.Addr) (DatagramMTUResult, error) {
	ipv6 := ip.Is6()
	mtu, iface, err := routedInterfaceMTU(ctx, p.network, network, destination, ip)
	if err != nil {
		return DatagramMTUResult{}, err
	}
	maxPayload := UDPPayloadFromPathMTU(mtu, ipv6)
	key := learnedDatagramKey(network, destination, ipv6)
	resolvedKey := learnedDatagramKey(network, resolvedDatagramDestination(destination, ip), ipv6)
	p.mu.RLock()
	learned := PositiveMinimum(p.learned[key].value, p.learned[resolvedKey].value)
	p.mu.RUnlock()
	selected := PositiveMinimum(maxPayload, learned)
	reason := "routed interface MTU"
	if learned > 0 && learned <= maxPayload {
		reason = "learned PMTU after EMSGSIZE"
	}
	return DatagramMTUResult{
		MaxPayload:  selected,
		Known:       selected > 0,
		IPv6:        ipv6,
		Destination: destination,
		Constraint: MTUConstraint{
			Value: selected, Source: iface, Reason: reason,
			InputValue: mtu, Overhead: mtu - maxPayload, Result: selected,
		},
	}, nil
}

// LearnDatagramMTU reduces a destination limit immediately. The fixed safety
// step prevents a long one-byte EMSGSIZE convergence loop on platforms where
// the socket does not expose the route PMTU.
func (p *NativeDatagramMTUProvider) LearnDatagramMTU(network, address string, ipv6 bool, attemptedSize int) {
	if p == nil || attemptedSize <= 1 {
		return
	}
	const conservativeStep = 64
	limit := attemptedSize - conservativeStep
	if limit < 1 {
		limit = attemptedSize - 1
	}
	_, destination := datagramAddressFamily(network, address)
	key := learnedDatagramKey(network, destination, ipv6)
	p.mu.Lock()
	old := p.learned[key]
	p.sequence++
	entry := learnedDatagramLimit{
		value: limit, expires: time.Now().Add(datagramMTULearnedHoldDown), sequence: p.sequence,
	}
	if old.value > 0 && old.value <= limit {
		entry.value = old.value
		p.learned[key] = entry
		p.mu.Unlock()
		p.expireLearnedLimitAfter(key, entry)
		return
	}
	p.learned[key] = entry
	p.revision++
	revision := p.revision
	p.mu.Unlock()
	p.subs.notify(revision)
	p.expireLearnedLimitAfter(key, entry)
}

func (p *NativeDatagramMTUProvider) expireLearnedLimitAfter(key string, entry learnedDatagramLimit) {
	delay := time.Until(entry.expires)
	if delay < 0 {
		delay = 0
	}
	time.AfterFunc(delay, func() {
		p.mu.Lock()
		current, ok := p.learned[key]
		if !ok || current.sequence != entry.sequence || time.Now().Before(current.expires) {
			p.mu.Unlock()
			return
		}
		delete(p.learned, key)
		p.revision++
		revision := p.revision
		p.mu.Unlock()
		p.subs.notify(revision)
	})
}

func resolveDatagramDestination(ctx context.Context, network gonnect.Network, networkName, address string) ([]netip.Addr, string, error) {
	_, destination := datagramAddressFamily(networkName, address)
	host := destinationHost(destination)
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{ip.Unmap()}, destination, nil
	}
	ips, err := network.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, destination, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if ip.IsValid() {
			out = append(out, ip.Unmap())
		}
	}
	if len(out) == 0 {
		return nil, destination, fmt.Errorf("resolve UDP destination %q: no IP addresses", host)
	}
	return out, destination, nil
}

func routedInterfaceMTU(ctx context.Context, network gonnect.Network, networkName, destination string, ip netip.Addr) (int, string, error) {
	port := uint16(9)
	if _, value, err := net.SplitHostPort(destination); err == nil {
		if parsed, parseErr := net.LookupPort("udp", value); parseErr == nil && parsed > 0 && parsed <= 65535 {
			port = uint16(parsed)
		}
	}
	remote := netip.AddrPortFrom(ip, port).String()
	family := "udp4"
	if ip.Is6() {
		family = "udp6"
	}
	if strings.HasSuffix(networkName, "4") || strings.HasSuffix(networkName, "6") {
		family = networkName
	}
	conn, err := network.DialUDP(ctx, family, "", remote)
	if err != nil {
		return 0, "", fmt.Errorf("select route to %s: %w", remote, err)
	}
	pathMTU, _ := socketPathMTU(conn, ip.Is6())
	local := conn.LocalAddr()
	_ = conn.Close()
	localIP := addrIP(local)
	interfaces, err := network.Interfaces()
	if err != nil {
		return 0, "", fmt.Errorf("list routed interfaces: %w", err)
	}
	fallbackMTU := 0
	fallbackName := "native network"
	for _, iface := range interfaces {
		if iface == nil || iface.MTU() <= 0 || iface.Flags()&net.FlagUp == 0 {
			continue
		}
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		familyMatches := false
		for _, address := range addrs {
			candidate := addrIP(address)
			if !candidate.IsValid() || candidate.Is6() != ip.Is6() {
				continue
			}
			familyMatches = true
			if localIP.IsValid() && candidate == localIP {
				selectedMTU, selectedName := routedMTUResult(iface.MTU(), pathMTU, iface.Name())
				return selectedMTU, selectedName, nil
			}
		}
		if familyMatches && (fallbackMTU == 0 || iface.MTU() < fallbackMTU) {
			fallbackMTU = iface.MTU()
			fallbackName = iface.Name()
		}
	}
	if fallbackMTU > 0 {
		selectedMTU, selectedName := routedMTUResult(fallbackMTU, pathMTU, fallbackName)
		return selectedMTU, selectedName, nil
	}
	return 0, "", fmt.Errorf("find routed interface for %s: local address %v is not assigned", remote, local)
}

func routedMTUResult(interfaceMTU, pathMTU int, interfaceName string) (int, string) {
	if pathMTU > 0 && pathMTU < interfaceMTU {
		return pathMTU, interfaceName + " route PMTU"
	}
	return interfaceMTU, interfaceName
}

func addrIP(value net.Addr) netip.Addr {
	if value == nil {
		return netip.Addr{}
	}
	switch addr := value.(type) {
	case *net.IPAddr:
		value, _ := netip.AddrFromSlice(addr.IP)
		return value.Unmap()
	case *net.IPNet:
		value, _ := netip.AddrFromSlice(addr.IP)
		return value.Unmap()
	case *net.TCPAddr:
		value, _ := netip.AddrFromSlice(addr.IP)
		return value.Unmap()
	case *net.UDPAddr:
		value, _ := netip.AddrFromSlice(addr.IP)
		return value.Unmap()
	}
	host, _, err := net.SplitHostPort(value.String())
	if err != nil {
		host = value.String()
	}
	addr, _ := netip.ParseAddr(strings.Trim(host, "[]"))
	return addr.Unmap()
}

func datagramAddressFamily(network, address string) (int, string) {
	destination := strings.TrimSpace(address)
	host := destinationHost(destination)
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if ip.Is4() {
			return 4, destination
		}
		return 6, destination
	}
	if strings.HasSuffix(strings.ToLower(network), "4") {
		return 4, destination
	}
	if strings.HasSuffix(strings.ToLower(network), "6") {
		return 6, destination
	}
	return 0, destination
}

func destinationHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(address, "[]")
}

func resolvedDatagramDestination(destination string, ip netip.Addr) string {
	_, port, err := net.SplitHostPort(destination)
	if err != nil {
		return ip.String()
	}
	return net.JoinHostPort(ip.String(), port)
}

func learnedDatagramKey(network, destination string, ipv6 bool) string {
	family := "4"
	if ipv6 {
		family = "6"
	}
	return strings.ToLower(strings.TrimSpace(network)) + "\x00" + strings.ToLower(strings.TrimSpace(destination)) + "\x00" + family
}

func mtuConstraintsEqual(a, b MTUConstraint) bool {
	return a.Value == b.Value && a.SourceNode == b.SourceNode && a.Source == b.Source &&
		a.Reason == b.Reason && a.InputValue == b.InputValue && a.Overhead == b.Overhead &&
		a.Result == b.Result && slices.Equal(a.Path, b.Path)
}
