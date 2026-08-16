package networknodes

import (
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	TypeNetwork = nodekit.TypeNetwork
	TypeTun     = nodekit.TypeTun
)

type NetworkCloser = nodekit.NetworkCloser
type NetworkPayload = nodekit.NetworkPayload
type NetworkOutputCache = nodekit.NetworkOutputCache
type NodeBase = nodekit.Base
type Runtime = nodekit.Runtime
type WorkerSpawner = nodekit.WorkerSpawner
type slotPorts = nodekit.SlotPorts
type slotPortReview = nodekit.SlotPortReview

func NetworkPort(direction, name string) pasta.Port {
	return nodekit.NetworkPort(direction, name)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	nodekit.BindNetworkResource(w, node, link, n)
}

func BroadcastNetworkPort(w *pasta.Workspace, node, port uint64, send func(link uint64)) {
	nodekit.BroadcastNetworkPort(w, node, port, send)
}

func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return nodekit.AcceptNetworkPayload(w, node, event, current)
}

func acceptNetworkPayload(event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return links.AcceptNetworkPayloadValue(event, current)
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	return nodekit.SameNetworkInstance(a, b)
}

func NetworkIsReady(n NetworkCloser) bool {
	return nodekit.NetworkIsReady(n)
}

func closeNetwork(n NetworkCloser) error {
	return nodekit.CloseNetwork(n)
}

func TunPort(direction, name string) pasta.Port {
	return nodekit.TunPort(direction, name)
}

func TunPreLinkAdd(w *pasta.Workspace, port uint64, linkType string) error {
	return nodekit.TunPreLinkAdd(w, port, linkType)
}

func AcceptTunPayload(w *pasta.Workspace, node uint64, event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	return nodekit.AcceptTunPayload(w, node, event, expectedLink, current)
}

func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	nodekit.BindTunResource(w, node, link, t)
}

func restoredLeft(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredLeft(restored, index)
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredRight(restored, index)
}

func restoredRightPorts(restored *pasta.NodeInitData) []uint64 {
	return nodekit.RestoredRightPorts(restored)
}

func newSlotPorts(max int, direction string, newPort func(slot int) pasta.Port, name func(slot int) string, parseName func(name string) (int, bool)) slotPorts {
	return nodekit.NewSlotPorts(max, direction, newPort, name, parseName)
}

func slotPortName(slot int) string {
	return nodekit.SlotPortName(slot)
}

func slotPortFromName(name string, max int) (int, bool) {
	return nodekit.SlotPortFromName(name, max)
}

func sendNodeMenuSnapshot(w *pasta.Workspace, node uint64, force bool, blocks ...formular.Block) {
	nodekit.SendNodeMenuSnapshot(w, node, force, blocks...)
}

func sendNodeBlockSnapshot(w *pasta.Workspace, node uint64, block formular.Block) {
	nodekit.SendNodeBlockSnapshot(w, node, block)
}

func sendNodeFieldStatus(w *pasta.Workspace, node uint64, field formular.FieldRef, err error) {
	nodekit.SendNodeFieldStatus(w, node, field, err)
}

func configTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	return nodekit.ConfigTextField(id, label, value, statusErr, statusText, multiline)
}

func configIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	return nodekit.ConfigIntField(id, label, value, statusErr, statusText)
}

func configFloatField(id, label string, value float64, statusErr bool, statusText string) formular.Item {
	return nodekit.ConfigFloatField(id, label, value, statusErr, statusText)
}

func configCheckbox(id, label string, value bool) formular.Item {
	return nodekit.ConfigCheckbox(id, label, value)
}

func enginePool(runtime nodekit.Runtime) bufpool.Pool {
	if runtime == nil {
		return nil
	}
	return runtime.Pool()
}

func runtimeDNSCacheManager(runtime nodekit.Runtime) storage.DnsCacheManager {
	if runtime == nil {
		return nil
	}
	return runtime.DnsCacheManager()
}
