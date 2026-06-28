package almagest

import (
	"errors"
	"slices"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// TypeTun is the Pasta link type carrying gonnect/tun.Tun values and TunConfig
// suggestions.
//
// Tun values flow from right-directed ports to left-directed ports, which
// corresponds to left-to-right graph data flow. A node owning a right-directed
// almagest/tun port sends its current Tun instance on every connected link
// when a new link connects, when OnReady runs, when the owned Tun instance is
// replaced, and when the peer sends std.RequestValue from the left-directed
// side. If a right-directed port does not yet have a ready Tun instance, it
// defers sending until a value becomes available. Every sent Tun must be
// wrapped with tun.Detach before being emitted, and both senders and receivers
// must register any sent or received Tun with the Workspace resource tracking
// system, bound to the owning node and link. The event payload implements
// std.ClosablePayload, so middleware nodes such as std.SelectClass can close a
// previously-routed wrapper when switching paths.
// Repeated payload sends for the same link must be idempotent while the
// underlying Tun source is unchanged: providers should re-emit the existing
// live detached wrapper instead of closing it and creating another one. Closing
// and replacing a live wrapper during OnReady, OnLinkAdd, std.Request, or
// config propagation can leave downstream nodes holding a closed Tun and cause
// startup packet loss. Keep sent wrappers per link, check them with
// TunIsReady, and only close/replace them when the wrapper is missing, down,
// closed, the link is removed, or the underlying Tun source is intentionally
// rebuilt. Nodes that emit secondary frontend Tuns, such as splitters, must
// keep the frontend lifetime aligned with the emitted wrapper lifetime.
//
// TunConfig values flow in the opposite direction, from left-directed ports to
// right-directed ports. Config values are suggestions for Tun-providing nodes:
// the provider may configure its device with the suggested interface addresses
// and MTU. Only the latest config received through a specific link is current
// for that link, and config must be forgotten when that link detaches.
//
// Both left-directed and right-directed almagest/tun ports allow at most one
// almagest/tun link at a time.
const TypeTun = "almagest/tun"

var (
	_ std.ClosablePayload = &TunPayload{}
)

var (
	// ErrTunPortBusy reports that an almagest/tun port already has an
	// almagest/tun link attached. Use TunPreLinkAdd from node PreLinkAdd
	// implementations to enforce TypeTun's one-link-per-port rule.
	ErrTunPortBusy = errors.New("tun port already linked")
)

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

	// MTU is the suggested interface MTU. Zero means no MTU suggestion.
	MTU int
}

// Clone returns a deep copy of cfg.
func (cfg TunConfig) Clone() TunConfig {
	return TunConfig{
		Addrs: slices.Clone(cfg.Addrs),
		MTU:   cfg.MTU,
	}
}

// IsZero reports whether cfg carries no address or MTU suggestion.
func (cfg TunConfig) IsZero() bool {
	return len(cfg.Addrs) == 0 && cfg.MTU == 0
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
// Address suggestions are de-duplicated by exact string value while preserving
// first-seen order by ascending link ID and then by each config's address
// order. MTU suggestions are merged by taking the smallest positive MTU, which
// is the conservative value when one Tun must satisfy multiple consumers. A
// non-positive MTU is ignored.
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
		if cfg.MTU > 0 && (merged.MTU == 0 || cfg.MTU < merged.MTU) {
			merged.MTU = cfg.MTU
		}
	}
	return merged
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
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		return false
	}
	for _, link := range snapshot.Links {
		linkSnapshot, ok := w.LinkSnapshot(link)
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
	_ = w.AddNodeResource(node, t)
	if link != 0 {
		_ = w.AddLinkResource(link, t)
	}
}

// DetachTunForLink wraps t with tun.Detach and binds the wrapper to workspace
// node and link lifecycles. It returns nil when t is nil.
func DetachTunForLink(w *pasta.Workspace, node, link uint64, t tun.Tun, pools ...bufpool.Pool) tun.Tun {
	if t == nil {
		return nil
	}
	wrapper := tun.Detach(t, pools...)
	BindTunResource(w, node, link, wrapper)
	return wrapper
}

type tunWaiter interface {
	Wait()
}

func closeTunForLaterWait(waiting *[]tun.Tun, t tun.Tun) {
	if t == nil {
		return
	}
	_ = t.Close()
	if _, ok := t.(tunWaiter); ok && waiting != nil {
		*waiting = append(*waiting, t)
	}
}

func waitTun(t tun.Tun) {
	if waiter, ok := t.(tunWaiter); ok {
		waiter.Wait()
	}
}

func waitTuns(tuns []tun.Tun) {
	for _, t := range tuns {
		waitTun(t)
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
