package wireguardnodes

import (
	"errors"
	"reflect"
	"slices"
	"sync"

	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/links"
)

// TypeWGCtrl is the Pasta link type that gives a controller access to one
// shared WireGuard device.
//
// API values flow from left-directed ports to right-directed ports. A
// WireGuard node owns the left-directed provider port. It can accept multiple
// links and must give each link a separate API created with
// wgdevice.DetachDevice. A controller owns a right-directed consumer port and
// should accept at most one TypeWGCtrl link.
//
// WGCtrlInterfaceConfig values flow in the opposite direction. Each controller
// can suggest interface addresses and one DNS server address. The WireGuard
// node merges interface addresses with its menu owned IPs and suggestions from
// all other attached controller links. For DNS, the most recently received
// current suggestion wins. A DNS value set in the WireGuard menu has priority
// over all controller suggestions. Only the latest config from each link is
// current. An empty config withdraws that link's suggestions, and removing the
// link removes its suggestions.
//
// Each detached API has an independent lifecycle. Closing it does not close
// the shared WireGuard device or another controller API. Providers must bind
// the detached API to both endpoint nodes and the link. EmitDetachedWGCtrl and
// AcceptWGCtrlPayload do this automatically. The payload is also closeable, so
// Pasta middleware can revoke an API when it stops routing that payload.
//
// Controllers must use the tracked wgo methods for controller-owned resources:
// UpsertTrackedPeer and DeleteTrackedPeer for peers, and AddTrackedTransport,
// ReplaceTrackedTransport, and RemoveTrackedTransport for custom named UDP
// outputs. Closing the detached API then removes those resources. Controllers
// must not change device-global state, replace all peers, attach a TUN or bind,
// bring the shared device up or down, or close the underlying device.
// Controller nodes must run wgo operations outside Pasta workspace callbacks.
// A callback should only accept the API, update desired state, and schedule
// runtime work. Peer and transport operations can open or stop workers and can
// block while they release runtime resources.
//
// Providers should keep and re-emit the live payload for ordinary broadcasts.
// They should emit a fresh detached API for std.RequestValue because closable
// middleware can request its new path before the old payload finishes closing.
// Consumers must close replaced APIs outside workspace callbacks.
const TypeWGCtrl = links.TypeWGCtrl

var (
	_ std.ClosablePayload = WGCtrlPayload{}

	// ErrWGCtrlPortBusy reports that a right-directed controller port already
	// has a TypeWGCtrl link.
	ErrWGCtrlPortBusy = errors.New("wireguard controller port already linked")
)

// WGCtrlAPI is the wgo control interface carried by TypeWGCtrl.
type WGCtrlAPI = wgdevice.DeviceAPI

// WGCtrlPayload carries one detached WireGuard API over a TypeWGCtrl event.
//
// API consumers should use tracked methods for all controller-owned peers and
// transports. Construct payloads with EmitDetachedWGCtrl so Close cannot close
// the underlying shared device.
type WGCtrlPayload struct {
	API WGCtrlAPI

	resource *wgCtrlResource
}

// WGCtrlInterfaceConfig carries controller-owned WireGuard interface settings
// from a right-directed controller port to a left-directed provider port.
// OwnedIPs accepts IP addresses or CIDR prefixes. DNS accepts one IP address
// without a CIDR prefix or host name.
type WGCtrlInterfaceConfig struct {
	OwnedIPs []string
	DNS      string
}

// wgCtrlInterfaceConfigApply carries a config update that must complete before
// the controller continues with peer or transport changes. result must be
// buffered so the provider never blocks a workspace callback.
type wgCtrlInterfaceConfigApply struct {
	config WGCtrlInterfaceConfig
	result chan<- error
	link   uint64
	err    error
}

type wgCtrlInterfaceConfigCommit struct {
	apply *wgCtrlInterfaceConfigApply
}

// Clone returns a deep copy of cfg.
func (cfg WGCtrlInterfaceConfig) Clone() WGCtrlInterfaceConfig {
	return WGCtrlInterfaceConfig{OwnedIPs: slices.Clone(cfg.OwnedIPs), DNS: cfg.DNS}
}

// IsZero reports whether cfg has no interface setting suggestions.
func (cfg WGCtrlInterfaceConfig) IsZero() bool {
	return len(cfg.OwnedIPs) == 0 && cfg.DNS == ""
}

// Close closes the detached API and releases its tracked wgo resources.
func (p WGCtrlPayload) Close() error {
	if p.resource != nil {
		return p.resource.Close()
	}
	if !wgCtrlIsNil(p.API) {
		p.API.Close()
	}
	return nil
}

// WGCtrlPort returns a Pasta port declaration for TypeWGCtrl.
func WGCtrlPort(direction, name string) pasta.Port {
	return pasta.Port{Direction: direction, Name: name, Types: []string{TypeWGCtrl}}
}

// WGCtrlPreLinkAdd validates a TypeWGCtrl PreLinkAdd callback.
//
// Left-directed provider ports can have multiple links. Right-directed
// controller ports can have at most one link.
func WGCtrlPreLinkAdd(w *pasta.Workspace, port uint64, linkType, portDirection string) error {
	if linkType != TypeWGCtrl {
		return pasta.LinkTypeErr(linkType)
	}
	switch portDirection {
	case "left":
		return nil
	case "right":
		if WGCtrlPortHasLink(w, port) {
			return ErrWGCtrlPortBusy
		}
		return nil
	default:
		return pasta.LinkTypeErr(linkType)
	}
}

// WGCtrlPortHasLink reports whether port has an attached TypeWGCtrl link.
func WGCtrlPortHasLink(w *pasta.Workspace, port uint64) bool {
	if w == nil || port == 0 {
		return false
	}
	snapshot, ok := w.PortSnapshotLocked(port)
	if !ok {
		return false
	}
	for _, link := range snapshot.Links {
		linkSnapshot, ok := w.LinkSnapshotLocked(link)
		if ok && linkSnapshot.Type == TypeWGCtrl {
			return true
		}
	}
	return false
}

// EmitWGCtrlInterfaceConfig sends a controller's current interface suggestions
// to the WireGuard provider on link. The workspace must be locked. A zero
// config withdraws the controller's previous suggestions.
func EmitWGCtrlInterfaceConfig(w *pasta.Workspace, node, link uint64, cfg WGCtrlInterfaceConfig) bool {
	if w == nil || node == 0 || link == 0 {
		return false
	}
	snapshot, ok := w.LinkSnapshotLocked(link)
	if !ok || snapshot.Type != TypeWGCtrl || snapshot.RightPortNode != node {
		return false
	}
	w.EmitEventLocked(node, link, cfg.Clone())
	return true
}

// EmitWGCtrlInterfaceConfigApply sends a controller's interface suggestions.
// It reports only after the provider has validated them and the resulting TUN
// generation is attached. The workspace must be locked. result must have
// capacity for one value.
func EmitWGCtrlInterfaceConfigApply(
	w *pasta.Workspace,
	node, link uint64,
	cfg WGCtrlInterfaceConfig,
	result chan<- error,
) bool {
	if w == nil || node == 0 || link == 0 || result == nil {
		return false
	}
	snapshot, ok := w.LinkSnapshotLocked(link)
	if !ok || snapshot.Type != TypeWGCtrl || snapshot.RightPortNode != node {
		return false
	}
	apply := &wgCtrlInterfaceConfigApply{
		config: cfg.Clone(),
		result: result,
	}
	w.EmitEventLocked(node, link, apply)
	w.EmitLowPriorityEventLocked(node, link, wgCtrlInterfaceConfigCommit{apply: apply})
	return true
}

// BindWGCtrlResource binds payload to both link endpoint nodes and the link.
//
// The workspace must be locked. Calls for a missing or non-TypeWGCtrl link, or
// for a payload without an API, have no effect.
func BindWGCtrlResource(w *pasta.Workspace, link uint64, payload WGCtrlPayload) {
	if w == nil || link == 0 || wgCtrlIsNil(payload.API) {
		return
	}
	snapshot, ok := w.LinkSnapshotLocked(link)
	if !ok || snapshot.Type != TypeWGCtrl {
		return
	}
	resource := payload.resource
	if resource == nil {
		resource = &wgCtrlResource{api: payload.API}
	}
	_ = w.AddNodeResourceLocked(snapshot.LeftPortNode, resource)
	if snapshot.RightPortNode != snapshot.LeftPortNode {
		_ = w.AddNodeResourceLocked(snapshot.RightPortNode, resource)
	}
	_ = w.AddLinkResourceLocked(link, resource)
}

// EmitWGCtrlPayload sends a ready payload from the left endpoint of link.
//
// The workspace must be locked. The function also binds the payload resource
// to both endpoint nodes and the link.
func EmitWGCtrlPayload(w *pasta.Workspace, node, link uint64, payload WGCtrlPayload) bool {
	if w == nil || node == 0 || link == 0 || !WGCtrlIsReady(payload.API) {
		return false
	}
	snapshot, ok := w.LinkSnapshotLocked(link)
	if !ok || snapshot.Type != TypeWGCtrl || snapshot.LeftPortNode != node {
		return false
	}
	BindWGCtrlResource(w, link, payload)
	w.EmitEventLocked(node, link, payload)
	return true
}

// EmitDetachedWGCtrl creates and sends an independently closeable API for
// source on link.
//
// The workspace must be locked. The caller should keep the returned payload
// for identity-stable ordinary broadcasts. It should create a fresh detached
// payload for std.RequestValue. A zero payload is returned when source or link
// is not usable.
func EmitDetachedWGCtrl(w *pasta.Workspace, node, link uint64, source WGCtrlAPI) WGCtrlPayload {
	if w == nil || node == 0 || link == 0 || !WGCtrlIsReady(source) {
		return WGCtrlPayload{}
	}
	snapshot, ok := w.LinkSnapshotLocked(link)
	if !ok || snapshot.Type != TypeWGCtrl || snapshot.LeftPortNode != node {
		return WGCtrlPayload{}
	}
	api := wgdevice.DetachDevice(source)
	resource := &wgCtrlResource{api: api}
	payload := WGCtrlPayload{API: api, resource: resource}
	if !EmitWGCtrlPayload(w, node, link, payload) {
		_ = resource.Close()
		return WGCtrlPayload{}
	}
	return payload
}

// AcceptWGCtrlPayloadValue validates a controller API event without binding
// its workspace resource.
//
// A different live API is accepted. Middleware such as pasta/Select can route
// a replacement before it finishes closing the old closable payload. The
// consumer must switch APIs and close the previous API outside the workspace
// callback. Detached API Close is idempotent, so this is safe when middleware
// also closes the old payload.
func AcceptWGCtrlPayloadValue(event pasta.Event, expectedLink uint64, current WGCtrlAPI) (WGCtrlAPI, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(WGCtrlPayload)
	if !ok || !WGCtrlIsReady(payload.API) {
		return current, false
	}
	if expectedLink != 0 && event.Link != expectedLink {
		return current, false
	}
	if SameWGCtrlInstance(current, payload.API) {
		return current, false
	}
	return payload.API, true
}

// AcceptWGCtrlPayload validates a controller API event and binds its resource
// to both endpoint nodes and the event link.
func AcceptWGCtrlPayload(w *pasta.Workspace, event pasta.Event, expectedLink uint64, current WGCtrlAPI) (WGCtrlAPI, bool) {
	next, changed := AcceptWGCtrlPayloadValue(event, expectedLink, current)
	if !changed {
		return next, false
	}
	payload := event.Payload.(WGCtrlPayload)
	BindWGCtrlResource(w, event.Link, payload)
	return next, true
}

// WGCtrlPublicKey returns the shared device public key visible through api.
func WGCtrlPublicKey(api WGCtrlAPI) (wgdevice.NoisePublicKey, bool) {
	if wgCtrlIsNil(api) {
		return wgdevice.NoisePublicKey{}, false
	}
	privateKey := api.PrivateKey()
	if privateKey.IsZero() {
		return wgdevice.NoisePublicKey{}, false
	}
	return privateKey.PublicKey(), true
}

// WGCtrlIsReady reports whether api is present and has not closed.
func WGCtrlIsReady(api WGCtrlAPI) bool {
	if wgCtrlIsNil(api) {
		return false
	}
	select {
	case <-api.Wait():
		return false
	default:
		return true
	}
}

// SameWGCtrlInstance reports whether a and b are the same comparable API
// object. Non-comparable API values are never considered the same.
func SameWGCtrlInstance(a, b WGCtrlAPI) bool {
	aNil := wgCtrlIsNil(a)
	bNil := wgCtrlIsNil(b)
	if aNil || bNil {
		return aNil && bNil
	}
	return links.SameComparableValue(a, b)
}

// CloseWGCtrl closes api when it is not nil.
func CloseWGCtrl(api WGCtrlAPI) {
	if !wgCtrlIsNil(api) {
		api.Close()
	}
}

// wgCtrlIsNil also detects a nil pointer stored in an interface. This state
// occurs while a WireGuard node has not published its runtime device yet.
func wgCtrlIsNil(api WGCtrlAPI) bool {
	if api == nil {
		return true
	}
	value := reflect.ValueOf(api)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// wgCtrlResource adapts wgo's idempotent Close method to Pasta's io.Closer
// resource contract. One resource instance is shared by the payload, both
// endpoint node bindings, and the link binding.
type wgCtrlResource struct {
	api  WGCtrlAPI
	once sync.Once
}

func (r *wgCtrlResource) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		CloseWGCtrl(r.api)
	})
	return nil
}
