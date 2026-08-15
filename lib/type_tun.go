package almagest

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/links"
)

const TypeTun = links.TypeTun

var ErrTunPortBusy = links.ErrTunPortBusy

type TunPayload = links.TunPayload
type TunConfig = links.TunConfig
type TunConfigByLink = links.TunConfigByLink
type TunOutputCache = links.TunOutputCache

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

func TunPortHasLink(w *pasta.Workspace, port uint64) bool {
	return links.TunPortHasLink(w, port)
}

func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	links.BindTunResource(w, node, link, t)
}

func DetachTunForLink(w *pasta.Workspace, node, link uint64, t tun.Tun, pools ...bufpool.Pool) tun.Tun {
	return links.DetachTunForLink(w, node, link, t, pools...)
}

func acceptTunPayload(event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	return links.AcceptTunPayloadValue(event, expectedLink, current)
}

func AcceptTunPayload(w *pasta.Workspace, node uint64, event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	return links.AcceptTunPayload(w, node, event, expectedLink, current)
}

func waitTun(t tun.Tun) {
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
