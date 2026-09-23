package nodekit

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/links"
)

const (
	TypeNetwork = links.TypeNetwork
	TypeTun     = links.TypeTun
)

var ErrTunPortBusy = links.ErrTunPortBusy

type NetworkCloser = links.NetworkCloser
type NetworkPayload = links.NetworkPayload
type NetworkOutputCache = links.NetworkOutputCache

type TunPayload = links.TunPayload
type TunConfig = links.TunConfig
type TunSourceRoute = links.TunSourceRoute
type TunConfigByLink = links.TunConfigByLink
type TunOutputCache = links.TunOutputCache
type TunFailureObserver = links.TunFailureObserver

func NetworkPort(direction, name string) pasta.Port {
	return links.NetworkPort(direction, name)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	links.BindNetworkResource(w, node, link, n)
}

func EmitDetachedNetwork(w *pasta.Workspace, node, link uint64, source gonnect.Network) NetworkCloser {
	return links.EmitDetachedNetwork(w, node, link, source)
}

func BroadcastNetworkPort(w *pasta.Workspace, node, port uint64, send func(link uint64)) {
	links.BroadcastNetworkPort(w, node, port, send)
}

func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return links.AcceptNetworkPayload(w, node, event, current)
}

func SameNetworkSource(a, b any) bool {
	return links.SameNetworkSource(a, b)
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	return links.SameNetworkInstance(a, b)
}

func SameNetworkResolver(a, b gonnect.Resolver) bool {
	return links.SameNetworkResolver(a, b)
}

func CloseNetwork(n NetworkCloser) error {
	return links.CloseNetwork(n)
}

func NetworkSource(n any) any {
	return links.NetworkSource(n)
}

func SameComparableValue(a, b any) bool {
	return links.SameComparableValue(a, b)
}

func NetworkIsReady(n NetworkCloser) bool {
	return links.NetworkIsReady(n)
}

func EmitTunConfig(w *pasta.Workspace, node, link uint64, cfg TunConfig) bool {
	return links.EmitTunConfig(w, node, link, cfg)
}

func EmitMergedTunConfig(w *pasta.Workspace, node, link uint64, cfgs TunConfigByLink) bool {
	return links.EmitMergedTunConfig(w, node, link, cfgs)
}

func BroadcastMergedTunConfig(w *pasta.Workspace, node, port uint64, cfgs TunConfigByLink) {
	links.BroadcastMergedTunConfig(w, node, port, cfgs)
}

func TunPort(direction, name string) pasta.Port {
	return links.TunPort(direction, name)
}

func TunPreLinkAdd(w *pasta.Workspace, port uint64, linkType string) error {
	return links.TunPreLinkAdd(w, port, linkType)
}

// TunDebugString returns TUN lifecycle metadata for debug logs.
func TunDebugString(t tun.Tun) string {
	return links.TunDebugString(t)
}

func TunPortHasLink(w *pasta.Workspace, port uint64) bool {
	return links.TunPortHasLink(w, port)
}

func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	links.BindTunResource(w, node, link, t)
}

func DetachTunForLink(w *pasta.Workspace, node, link uint64, t tun.Tun, pools ...bufpool.Pool) tun.Tun {
	return links.DetachTunForLink(w, node, link, t, pools...)
}

func AcceptTunPayload(w *pasta.Workspace, node uint64, event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	return links.AcceptTunPayload(w, node, event, expectedLink, current)
}

// AcceptTunPayloadValue accepts a TUN payload without binding its resource.
func AcceptTunPayloadValue(event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	return links.AcceptTunPayloadValue(event, expectedLink, current)
}

func NewTunFailureObserver(source tun.Tun, report func(error)) *TunFailureObserver {
	return links.NewTunFailureObserver(source, report)
}

func TunFailureObserverSource(t tun.Tun) tun.Tun {
	return links.TunFailureObserverSource(t)
}

func CloseTun(t tun.Tun) error {
	return links.CloseTun(t)
}

func WaitTun(t tun.Tun) {
	links.WaitTun(t)
}

func TunIsReady(t tun.Tun) bool {
	return links.TunIsReady(t)
}

func SameTunSource(a, b tun.Tun) bool {
	return links.SameTunSource(a, b)
}

func SameTunInstance(a, b tun.Tun) bool {
	return links.SameTunInstance(a, b)
}

func TunSource(t any) any {
	return links.TunSource(t)
}
