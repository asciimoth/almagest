package tunnodes

import (
	"net/netip"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	TypeNetwork = nodekit.TypeNetwork
	TypeTun     = nodekit.TypeTun
)

type NetworkCloser = nodekit.NetworkCloser
type NetworkOutputCache = nodekit.NetworkOutputCache
type NodeBase = nodekit.Base
type Runtime = nodekit.Runtime
type TunPayload = nodekit.TunPayload
type TunConfig = nodekit.TunConfig
type TunSourceRoute = nodekit.TunSourceRoute
type TunConfigByLink = nodekit.TunConfigByLink
type TunOutputCache = nodekit.TunOutputCache
type tunInputFailureObserver = nodekit.TunFailureObserver
type WorkerSpawner = nodekit.WorkerSpawner
type slotPorts = nodekit.SlotPorts
type slotPortReview = nodekit.SlotPortReview

func NetworkPort(direction, name string) pasta.Port {
	return nodekit.NetworkPort(direction, name)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	nodekit.BindNetworkResource(w, node, link, n)
}

func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return nodekit.AcceptNetworkPayload(w, node, event, current)
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

func acceptTunPayload(event pasta.Event, expectedLink uint64, current tun.Tun) (tun.Tun, bool) {
	return nodekit.AcceptTunPayloadValue(event, expectedLink, current)
}

func newTunInputFailureObserver(source tun.Tun, report func(error)) *tunInputFailureObserver {
	return nodekit.NewTunFailureObserver(source, report)
}

func tunInputFailureObserverSource(t tun.Tun) tun.Tun {
	return nodekit.TunFailureObserverSource(t)
}

func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	nodekit.BindTunResource(w, node, link, t)
}

func SameTunInstance(a, b tun.Tun) bool {
	return nodekit.SameTunInstance(a, b)
}

func TunIsReady(t tun.Tun) bool {
	return nodekit.TunIsReady(t)
}

func tunDebugString(t tun.Tun) string {
	return nodekit.TunDebugString(t)
}

func EmitMergedTunConfig(w *pasta.Workspace, node, link uint64, cfgs TunConfigByLink) bool {
	return nodekit.EmitMergedTunConfig(w, node, link, cfgs)
}

func BroadcastMergedTunConfig(w *pasta.Workspace, node, port uint64, cfgs TunConfigByLink) {
	nodekit.BroadcastMergedTunConfig(w, node, port, cfgs)
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

func normalizeListText(value string) string {
	return nodekit.NormalizeListText(value)
}

func parseAddrList(value string, allowCIDR bool) ([]netip.Addr, error) {
	return nodekit.ParseAddrList(value, allowCIDR)
}

func configTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	return nodekit.ConfigTextField(id, label, value, statusErr, statusText, multiline)
}

func configIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	return nodekit.ConfigIntField(id, label, value, statusErr, statusText)
}

func configCheckbox(id, label string, value bool) formular.Item {
	return nodekit.ConfigCheckbox(id, label, value)
}

func readonlyTextField(id, label, value string) formular.Item {
	return nodekit.ReadonlyTextField(id, label, value)
}

func readonlyMultilineTextField(id, label, value string) formular.Item {
	return nodekit.ReadonlyMultilineTextField(id, label, value)
}

func readonlyStringListField(id, label string, values []string) formular.Item {
	return nodekit.ReadonlyStringListField(id, label, values)
}

func readonlyIntField(id, label string, value int) formular.Item {
	return nodekit.ReadonlyIntField(id, label, value)
}

func readonlyBoolField(id, label string, value bool) formular.Item {
	return nodekit.ReadonlyBoolField(id, label, value)
}

func logPayload(logger pasta.Logger, op string, payload []byte, err error) {
	nodekit.LogPayload(logger, op, payload, err)
}

func logResult(logger pasta.Logger, op string, err error) {
	nodekit.LogResult(logger, op, err)
}

func waitTun(t tun.Tun) {
	nodekit.WaitTun(t)
}

func enginePool(runtime nodekit.Runtime) bufpool.Pool {
	if runtime == nil {
		return nil
	}
	return runtime.Pool()
}

type ipAllocatorRuntime interface {
	AllocIP() subnet.IPAllocator
}

func runtimeIPAllocator(runtime nodekit.Runtime) subnet.IPAllocator {
	if runtime == nil {
		return nil
	}
	allocator, ok := runtime.(ipAllocatorRuntime)
	if !ok {
		return nil
	}
	return allocator.AllocIP()
}

func runtimeDNSCacheManager(runtime nodekit.Runtime) storage.DnsCacheManager {
	if runtime == nil {
		return nil
	}
	return runtime.DnsCacheManager()
}
