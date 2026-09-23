package links

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// TypeTun is the Pasta link type carrying gonnect/tun.Tun values and
// TunConfig suggestions.
//
// Tun values flow from right-directed ports to left-directed ports, which
// corresponds to left-to-right graph data flow. A node owning a right-directed
// almagest/tun port sends its current Tun instance on every connected link
// when a new link connects, when OnReady runs, when the owned Tun instance is
// replaced, and when the peer sends std.RequestValue from the left-directed
// side. If a right-directed port does not yet have a ready Tun instance, it
// defers sending until a value becomes available.
//
// Every sent Tun must be wrapped with tun.Detach before being emitted, and both
// senders and receivers must register any sent or received Tun with the
// Workspace resource tracking system, bound to the owning node and link. The
// event payload implements std.ClosablePayload, so middleware nodes such as
// std.SelectClass can close a previously-routed wrapper when switching paths.
// A TunPayload with a nil Tun explicitly makes the current value unavailable
// while the graph link stays attached.
//
// Repeated payload sends for the same link must be idempotent while the
// underlying Tun source is unchanged. Providers should re-emit the existing
// live detached wrapper instead of closing it and creating another one. Close
// and replace a live wrapper only when the wrapper is missing, down, closed,
// the link is removed, or the underlying Tun source is intentionally rebuilt.
//
// TunConfig values flow in the opposite direction, from left-directed ports to
// right-directed ports. Config values are suggestions for Tun-providing nodes:
// the provider may configure its device with the suggested interface addresses,
// route prefixes, destination-to-source routes, MTU, and DNS server address.
// The DNS suggestion is intended only for simple topologies, such as a VTun
// connected to WireGuard. Use the almagest/dns link type and DNS nodes when a
// topology needs DNS routing, composition, or policy.
//
// Middleware with one TUN input and one TUN output should pass DNS suggestions
// through. Middleware with multiple TUN inputs or outputs must drop them because
// one DNS server suggestion cannot describe a branched topology. TunJoiner is
// the exception: it sends the same downstream suggestion to all input TUNs.
//
// Only the latest config received through a specific link is current for that
// link, and config must be forgotten when that link detaches.
//
// Both left-directed and right-directed almagest/tun ports allow at most one
// almagest/tun link at a time.
const TypeTun = "almagest/tun"

var _ std.ClosablePayload = &TunPayload{}

// ErrTunPortBusy reports that an almagest/tun port already has an almagest/tun
// link attached. Use TunPreLinkAdd from node PreLinkAdd implementations to
// enforce TypeTun's one-link-per-port rule.
var ErrTunPortBusy = errors.New("tun port already linked")

// TunPayload is the TypeTun event payload for Tun objects.
type TunPayload struct {
	Tun tun.Tun
}

// Close closes the carried Tun when present.
func (p TunPayload) Close() error {
	if p.Tun == nil {
		return nil
	}
	return p.Tun.Close()
}

// TunConfig is the TypeTun event payload for provider configuration
// suggestions.
type TunConfig struct {
	// Addrs is a list of interface CIDR addresses, for example "10.0.0.2/32".
	Addrs []string

	// Routes is a list of route prefixes to route through the TUN device, for
	// example "10.0.0.0/24".
	Routes []string

	// SourceRoutes maps remote destination prefixes to local source addresses.
	// A userspace network stack can use this policy when one TUN endpoint has
	// multiple local addresses from the same IP family.
	SourceRoutes []TunSourceRoute

	// MTU is the suggested interface MTU. Zero means no MTU suggestion.
	MTU int

	// MTUConstraint explains MTU when diagnostic provenance is available.
	// Its numeric value does not replace MTU.
	MTUConstraint *MTUConstraint

	// DNS is the suggested DNS server IP address, for example "8.8.8.8".
	// An empty string means no DNS suggestion. DNS must not contain a CIDR
	// prefix or a host name.
	DNS string
}

// TunSourceRoute selects a local source address for one destination prefix.
// The most-specific matching destination has priority.
type TunSourceRoute struct {
	Destination netip.Prefix
	Source      netip.Addr
}

// Clone returns a deep copy of cfg.
func (cfg TunConfig) Clone() TunConfig {
	out := TunConfig{
		Addrs:        slices.Clone(cfg.Addrs),
		Routes:       slices.Clone(cfg.Routes),
		SourceRoutes: slices.Clone(cfg.SourceRoutes),
		MTU:          cfg.MTU,
		DNS:          cfg.DNS,
	}
	if cfg.MTUConstraint != nil {
		constraint := cfg.MTUConstraint.Clone()
		out.MTUConstraint = &constraint
	}
	return out
}

// WithInferredSourceRoutes returns a copy of cfg with a source route for each
// route prefix that does not already have one. The first valid address of the
// same IP family is the source. This preserves the source identity of one TUN
// consumer before a middleware node merges it with other consumer configs.
func (cfg TunConfig) WithInferredSourceRoutes() TunConfig {
	out := cfg.Clone()
	first4, first6 := firstTunConfigAddrs(out.Addrs)
	seenDestinations := make(map[netip.Prefix]struct{}, len(out.SourceRoutes))
	for _, route := range out.SourceRoutes {
		if route.Destination.IsValid() {
			seenDestinations[route.Destination.Masked()] = struct{}{}
		}
	}
	for _, value := range out.Routes {
		destination, err := netip.ParsePrefix(value)
		if err != nil {
			continue
		}
		destination = destination.Masked()
		if _, ok := seenDestinations[destination]; ok {
			continue
		}
		source := first6
		if destination.Addr().Is4() {
			source = first4
		}
		if !source.IsValid() {
			continue
		}
		out.SourceRoutes = append(out.SourceRoutes, TunSourceRoute{
			Destination: destination,
			Source:      source,
		})
		seenDestinations[destination] = struct{}{}
	}
	return out
}

func firstTunConfigAddrs(values []string) (first4, first6 netip.Addr) {
	for _, value := range values {
		var addr netip.Addr
		if prefix, err := netip.ParsePrefix(value); err == nil {
			addr = prefix.Addr()
		} else if parsed, err := netip.ParseAddr(value); err == nil {
			addr = parsed
		}
		switch {
		case addr.Is4() && !first4.IsValid():
			first4 = addr
		case addr.Is6() && !first6.IsValid():
			first6 = addr
		}
	}
	return first4, first6
}

// WithoutDNS returns a deep copy of cfg without its DNS suggestion.
//
// TUN providers that cannot use DNS suggestions and middleware that has
// multiple TUN inputs or outputs can use this method at their boundary.
func (cfg TunConfig) WithoutDNS() TunConfig {
	out := cfg.Clone()
	out.DNS = ""
	return out
}

// IsZero reports whether cfg carries no address, route, source-route, MTU, or
// DNS suggestion.
func (cfg TunConfig) IsZero() bool {
	return len(cfg.Addrs) == 0 && len(cfg.Routes) == 0 && len(cfg.SourceRoutes) == 0 && cfg.MTU == 0 && cfg.DNS == ""
}

// TunConfigByLink stores the latest TunConfig received per link.
//
// Middleware nodes can keep one store for configs received through their
// right-directed TypeTun ports, call Set on each TunConfig event, call Delete
// from OnLinkRemoved, and use Merge to build the single config object that
// should be sent upstream through left-directed TypeTun ports.
type TunConfigByLink map[uint64]TunConfig

// Set stores cfg as the latest config for link. The stored config is copied so
// later caller-side slice mutations do not alter the store.
func (s TunConfigByLink) Set(link uint64, cfg TunConfig) {
	if s == nil || link == 0 {
		return
	}
	s[link] = cfg.Clone()
}

// Delete forgets the latest config associated with link.
func (s TunConfigByLink) Delete(link uint64) {
	delete(s, link)
}

// Merge returns one config suggestion from all configs currently in the store.
//
// Address and route suggestions are de-duplicated by exact string value.
// Source routes are de-duplicated by exact destination and source values.
// First-seen order is preserved by ascending link ID and then by each config's
// slice order. MTU suggestions are merged by taking the smallest positive MTU,
// which is the conservative value when one Tun must satisfy multiple consumers.
// A non-positive MTU is ignored. The first non-empty DNS suggestion in link-ID
// order wins. TypeTun ports normally have one link, but deterministic behavior
// keeps this helper safe for the TunJoiner exception and custom middleware.
func (s TunConfigByLink) Merge() TunConfig {
	if len(s) == 0 {
		return TunConfig{}
	}
	links := make([]uint64, 0, len(s))
	for link := range s {
		links = append(links, link)
	}
	slices.Sort(links)

	seenAddrs := make(map[string]struct{})
	seenRoutes := make(map[string]struct{})
	seenSourceRoutes := make(map[TunSourceRoute]struct{})
	var merged TunConfig
	for _, link := range links {
		cfg := s[link]
		for _, addr := range cfg.Addrs {
			if _, ok := seenAddrs[addr]; ok {
				continue
			}
			seenAddrs[addr] = struct{}{}
			merged.Addrs = append(merged.Addrs, addr)
		}
		for _, route := range cfg.Routes {
			if _, ok := seenRoutes[route]; ok {
				continue
			}
			seenRoutes[route] = struct{}{}
			merged.Routes = append(merged.Routes, route)
		}
		for _, route := range cfg.SourceRoutes {
			if _, ok := seenSourceRoutes[route]; ok {
				continue
			}
			seenSourceRoutes[route] = struct{}{}
			merged.SourceRoutes = append(merged.SourceRoutes, route)
		}
		if cfg.MTU > 0 && (merged.MTU == 0 || cfg.MTU < merged.MTU) {
			merged.MTU = cfg.MTU
			merged.MTUConstraint = nil
			if cfg.MTUConstraint != nil {
				constraint := cfg.MTUConstraint.Clone()
				merged.MTUConstraint = &constraint
			}
		}
		if merged.DNS == "" && cfg.DNS != "" {
			merged.DNS = cfg.DNS
		}
	}
	return merged
}

// EmitTunConfig sends cfg to link when the link is still attached.
func EmitTunConfig(w *pasta.Workspace, node, link uint64, cfg TunConfig) bool {
	return emitIfLinked(w, node, link, cfg)
}

// EmitMergedTunConfig sends the current merged config store to link when the
// link is still attached.
func EmitMergedTunConfig(w *pasta.Workspace, node, link uint64, cfgs TunConfigByLink) bool {
	return EmitTunConfig(w, node, link, cfgs.Merge())
}

// BroadcastMergedTunConfig sends the current merged config store to every link
// attached to port.
func BroadcastMergedTunConfig(w *pasta.Workspace, node, port uint64, cfgs TunConfigByLink) {
	forEachPortLink(w, port, func(link uint64) {
		EmitMergedTunConfig(w, node, link, cfgs)
	})
}

// TunPort returns a Pasta port declaration for TypeTun.
func TunPort(direction, name string) pasta.Port {
	return pasta.Port{Direction: direction, Name: name, Types: []string{TypeTun}}
}

// TunPreLinkAdd validates a TypeTun PreLinkAdd callback for port.
//
// It rejects non-TypeTun links and enforces TypeTun's rule that both left and
// right TypeTun ports accept only one TypeTun link at a time.
func TunPreLinkAdd(w *pasta.Workspace, port uint64, linkType string) error {
	if linkType != TypeTun {
		return pasta.LinkTypeErr(linkType)
	}
	if TunPortHasLink(w, port) {
		return ErrTunPortBusy
	}
	return nil
}

// TunPortHasLink reports whether port currently has an attached TypeTun link.
func TunPortHasLink(w *pasta.Workspace, port uint64) bool {
	if w == nil || port == 0 {
		return false
	}
	snapshot, ok := w.PortSnapshotLocked(port)
	if !ok {
		return false
	}
	for _, link := range snapshot.Links {
		linkSnapshot, ok := w.LinkSnapshotLocked(link)
		if ok && linkSnapshot.Type == TypeTun {
			return true
		}
	}
	return false
}

// BindTunResource binds t to workspace node and link lifecycles when possible.
func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	if w == nil || t == nil {
		return
	}
	_ = w.AddNodeResourceLocked(node, t)
	if link != 0 {
		_ = w.AddLinkResourceLocked(link, t)
	}
}

// DetachTunForLink wraps t with tun.Detach and binds the wrapper to workspace
// node and link lifecycles. It returns nil when t is nil.
func DetachTunForLink(w *pasta.Workspace, node, link uint64, t tun.Tun, pools ...bufpool.Pool) tun.Tun {
	if t == nil {
		return nil
	}
	var pool bufpool.Pool
	if len(pools) > 0 {
		pool = pools[0]
	}
	wrapper := tun.Detach(t, nil, pool)
	BindTunResource(w, node, link, wrapper)
	return wrapper
}

// AcceptTunPayloadValue validates a TUN event and returns the accepted TUN
// without binding it to a workspace resource.
func AcceptTunPayloadValue(event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(TunPayload)
	if !ok {
		return current, false
	}
	if expectedLink != 0 && event.Link != expectedLink {
		return current, false
	}
	if payload.Tun == nil {
		if current == nil {
			return nil, false
		}
		_ = CloseTun(current)
		return nil, true
	}
	if !TunIsReady(payload.Tun) {
		_ = payload.Tun.Close()
		return current, false
	}
	if SameTunInstance(current, payload.Tun) {
		return current, false
	}
	_ = CloseTun(current)
	return payload.Tun, true
}

// AcceptTunPayload validates and binds a received TunPayload from expectedLink.
func AcceptTunPayload(w *pasta.Workspace, node uint64, event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	next, changed := AcceptTunPayloadValue(event, expectedLink, current)
	if !changed {
		return next, false
	}
	BindTunResource(w, node, event.Link, next)
	return next, true
}

// CloseTun closes t when it is not nil.
func CloseTun(t tun.Tun) error {
	if t == nil {
		return nil
	}
	return t.Close()
}

type tunOutputEntry struct {
	source *tunOutputSource
	output tun.Tun
}

type tunOutputSource struct {
	source tun.Tun
	root   *tun.DetachedTun
}

func (s *tunOutputSource) ready() bool {
	return s != nil && TunIsReady(s.root)
}

// TunOutputCache owns detached TUN wrappers emitted by a node, keyed by link.
//
// Each source has one stable detached root. Replacement link wrappers are
// nested under that root, so closing a consumer wrapper cannot leave an old
// read blocked directly on the source while a replacement starts a second
// source read.
type TunOutputCache struct {
	entries map[uint64]tunOutputEntry
	sources []*tunOutputSource
	waiting []tun.Tun
}

// RebindSource updates the source identity for cached outputs without replacing
// their detached wrappers. The caller must ensure oldSource and newSource use
// the same live packet stream.
func (c *TunOutputCache) RebindSource(oldSource, newSource tun.Tun) {
	if c == nil || oldSource == nil || newSource == nil {
		return
	}
	for _, state := range c.sources {
		if !SameTunSource(state.source, oldSource) {
			continue
		}
		state.source = newSource
	}
}

// Send emits source as a detached TunPayload on link. It re-emits a cached
// ready wrapper for the same source and replaces stale wrappers.
func (c *TunOutputCache) Send(w *pasta.Workspace, node, link uint64, source tun.Tun, pool bufpool.Pool) bool {
	return c.SendWrapped(w, node, link, source, nil, pool)
}

// SendLocked is Send for callers already running under the workspace lock.
func (c *TunOutputCache) SendLocked(w *pasta.Workspace, node, link uint64, source tun.Tun, pool bufpool.Pool) bool {
	return c.SendWrappedLocked(w, node, link, source, nil, pool)
}

// RefreshLocked emits a fresh detached TunPayload on link even when the source
// is unchanged. Use it to answer std.Request callbacks from middleware that may
// have just closed the previously routed payload.
func (c *TunOutputCache) RefreshLocked(w *pasta.Workspace, node, link uint64, source tun.Tun, pool bufpool.Pool) bool {
	return c.RefreshWrappedLocked(w, node, link, source, nil, pool)
}

// SendWrapped emits source as a detached TunPayload on link after wrapping the
// detached Tun with wrap. Middleware uses wrap to decorate the detached TUN
// before publishing it.
func (c *TunOutputCache) SendWrapped(w *pasta.Workspace, node, link uint64, source tun.Tun, wrap func(tun.Tun) tun.Tun, pool bufpool.Pool) bool {
	if w == nil {
		return false
	}
	w.Lock()
	defer w.Unlock()
	return c.SendWrappedLocked(w, node, link, source, wrap, pool)
}

// SendWrappedLocked is SendWrapped for callers already running under the
// workspace lock.
func (c *TunOutputCache) SendWrappedLocked(w *pasta.Workspace, node, link uint64, source tun.Tun, wrap func(tun.Tun) tun.Tun, pool bufpool.Pool) bool {
	if w == nil || link == 0 || source == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	if entry, ok := c.entries[link]; ok && entry.source.ready() && TunIsReady(entry.output) && SameTunSource(entry.source.source, source) {
		w.EmitEventLocked(node, link, TunPayload{Tun: entry.output})
		return true
	}
	return c.emitWrappedLocked(w, node, link, source, wrap, pool)
}

// RefreshWrappedLocked is RefreshLocked with an additional wrapper hook.
func (c *TunOutputCache) RefreshWrappedLocked(w *pasta.Workspace, node, link uint64, source tun.Tun, wrap func(tun.Tun) tun.Tun, pool bufpool.Pool) bool {
	if w == nil || link == 0 || source == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	return c.emitWrappedLocked(w, node, link, source, wrap, pool)
}

func (c *TunOutputCache) emitWrappedLocked(w *pasta.Workspace, node, link uint64, source tun.Tun, wrap func(tun.Tun) tun.Tun, pool bufpool.Pool) bool {
	state := c.outputSource(w, node, source, pool)
	if state == nil {
		c.Close(link)
		return false
	}
	if previous, ok := c.entries[link]; ok {
		CloseTunForLaterWait(&c.waiting, previous.output)
	}
	var output tun.Tun = tun.Detach(state.root, nil, pool)
	if wrap != nil {
		wrapped := wrap(output)
		if wrapped == nil {
			CloseTunForLaterWait(&c.waiting, output)
			delete(c.entries, link)
			return false
		}
		output = wrapped
	}
	BindTunResource(w, node, link, output)
	if c.entries == nil {
		c.entries = make(map[uint64]tunOutputEntry)
	}
	c.entries[link] = tunOutputEntry{source: state, output: output}
	w.EmitEventLocked(node, link, TunPayload{Tun: output})
	return true
}

func (c *TunOutputCache) outputSource(w *pasta.Workspace, node uint64, source tun.Tun, pool bufpool.Pool) *tunOutputSource {
	for index := 0; index < len(c.sources); {
		state := c.sources[index]
		if !state.ready() {
			CloseTunForLaterWait(&c.waiting, state.root)
			c.sources = append(c.sources[:index], c.sources[index+1:]...)
			continue
		}
		if !SameTunSource(state.source, source) {
			index++
			continue
		}
		return state
	}
	state := &tunOutputSource{
		source: source,
	}
	state.root = tun.Detach(source, nil, pool)
	if !TunIsReady(state.root) {
		CloseTunForLaterWait(&c.waiting, state.root)
		return nil
	}
	_ = w.AddNodeResourceLocked(node, state.root)
	c.sources = append(c.sources, state)
	return state
}

// Close closes and forgets the wrapper emitted on link. It keeps the source
// root ready for a later link so no uncancelable source read is abandoned.
func (c *TunOutputCache) Close(link uint64) {
	if c == nil || c.entries == nil {
		return
	}
	entry := c.entries[link]
	delete(c.entries, link)
	CloseTunForLaterWait(&c.waiting, entry.output)
}

// CloseAll closes and forgets all emitted wrappers.
func (c *TunOutputCache) CloseAll() {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		CloseTunForLaterWait(&c.waiting, entry.output)
	}
	for _, state := range c.sources {
		CloseTunForLaterWait(&c.waiting, state.root)
	}
	c.sources = nil
}

// InvalidateAllLocked closes all emitted wrappers and source roots, then tells
// consumers that no TUN value is currently available. The graph links stay
// attached and can receive replacement values later.
func (c *TunOutputCache) InvalidateAllLocked(w *pasta.Workspace, node uint64) {
	if c == nil {
		return
	}
	links := make([]uint64, 0, len(c.entries))
	for link, entry := range c.entries {
		delete(c.entries, link)
		CloseTunForLaterWait(&c.waiting, entry.output)
		links = append(links, link)
	}
	for _, state := range c.sources {
		CloseTunForLaterWait(&c.waiting, state.root)
	}
	c.sources = nil
	if w == nil {
		return
	}
	for _, link := range links {
		if _, ok := w.LinkSnapshotLocked(link); ok {
			w.EmitEventLocked(node, link, TunPayload{})
		}
	}
}

// Wait waits for detached wrappers closed by Close or CloseAll.
func (c *TunOutputCache) Wait() {
	if c == nil {
		return
	}
	WaitTuns(c.waiting)
	c.waiting = nil
}

type tunWaiter interface {
	Wait()
}

// CloseTunForLaterWait closes t and appends it to waiting if it also exposes a
// Wait method.
func CloseTunForLaterWait(waiting *[]tun.Tun, t tun.Tun) {
	if t == nil {
		return
	}
	_ = t.Close()
	if _, ok := t.(tunWaiter); ok && waiting != nil {
		*waiting = append(*waiting, t)
	}
}

// WaitTun waits for t when it exposes a Wait method.
func WaitTun(t tun.Tun) {
	if waiter, ok := t.(tunWaiter); ok {
		waiter.Wait()
	}
}

// WaitTuns waits for each TUN wrapper that exposes a Wait method.
func WaitTuns(tuns []tun.Tun) {
	for _, t := range tuns {
		WaitTun(t)
	}
}

// TunIsReady reports whether t is usable now. Detached Tun wrappers expose
// IsUp; plain Tun implementations without lifecycle state are treated as ready.
func TunIsReady(t tun.Tun) bool {
	if t == nil {
		return false
	}
	updown, ok := t.(interface {
		IsUp() (bool, error)
	})
	if !ok {
		return true
	}
	up, err := updown.IsUp()
	return err == nil && up
}

// TunDebugString returns lifecycle metadata for diagnostics. It does not read
// packets or consume lifecycle events.
func TunDebugString(t tun.Tun) string {
	if t == nil {
		return "nil"
	}
	name, nameErr := t.Name()
	mtu, mtuErr := t.MTU()
	up := true
	var upErr error
	if updown, ok := t.(interface {
		IsUp() (bool, error)
	}); ok {
		up, upErr = updown.IsUp()
	}
	fd := -1
	filePointer := "nil"
	if file := t.File(); file != nil {
		fd = int(file.Fd())
		filePointer = fmt.Sprintf("%p", file)
	}
	return fmt.Sprintf(
		"type=%T pointer=%s source_type=%T source_pointer=%s native=%t file=%s fd=%d name=%q name_error=%v mtu=%d mtu_error=%v mro=%d mwo=%d batch=%d up=%t up_error=%v",
		t,
		valuePointer(t),
		TunSource(t),
		valuePointer(TunSource(t)),
		t.IsNative(),
		filePointer,
		fd,
		name,
		nameErr,
		mtu,
		mtuErr,
		t.MRO(),
		t.MWO(),
		t.BatchSize(),
		up,
		upErr,
	)
}

func valuePointer(value any) string {
	if value == nil {
		return "nil"
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		if v.IsNil() {
			return "nil"
		}
		return fmt.Sprintf("0x%x", v.Pointer())
	default:
		return "non-pointer"
	}
}

// SameTunSource reports whether a and b ultimately wrap the same comparable
// Tun source. Non-comparable sources are never considered the same unless both
// values are nil.
func SameTunSource(a, b tun.Tun) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(TunSource(a), TunSource(b))
}

// SameTunInstance reports whether a and b are the same comparable Tun object.
// Non-comparable Tun values are never considered the same.
func SameTunInstance(a, b tun.Tun) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

// TunSource unwraps gonnect.Wrapper values until it reaches the underlying Tun
// source, then returns that source.
func TunSource(t any) any {
	for t != nil {
		wrapped := gonnect.GetWrapped(t)
		if wrapped == nil {
			return t
		}
		t = wrapped
	}
	return nil
}
