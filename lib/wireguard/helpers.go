package wireguardnodes

import (
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	TypeNetwork = nodekit.TypeNetwork
	TypeTun     = nodekit.TypeTun
)

var ErrSecretInvalidName = nodekit.ErrSecretInvalidName

type NetworkCloser = nodekit.NetworkCloser
type NetworkPayload = nodekit.NetworkPayload
type NodeBase = nodekit.Base
type Runtime = nodekit.Runtime
type SecretsStorage = nodekit.SecretsStorage
type Secret = nodekit.Secret
type SecretValueChanged = nodekit.SecretValueChanged
type TunPayload = nodekit.TunPayload
type TunConfig = nodekit.TunConfig

func NetworkPort(direction, name string) pasta.Port {
	return nodekit.NetworkPort(direction, name)
}

func TunPort(direction, name string) pasta.Port {
	return nodekit.TunPort(direction, name)
}

func TunPreLinkAdd(w *pasta.Workspace, port uint64, linkType string) error {
	return nodekit.TunPreLinkAdd(w, port, linkType)
}

func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	nodekit.BindTunResource(w, node, link, t)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	nodekit.BindNetworkResource(w, node, link, n)
}

func TunIsReady(t tun.Tun) bool {
	return nodekit.TunIsReady(t)
}

func NetworkIsReady(n NetworkCloser) bool {
	return nodekit.NetworkIsReady(n)
}

func SameTunInstance(a, b tun.Tun) bool {
	return nodekit.SameTunInstance(a, b)
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	return nodekit.SameNetworkInstance(a, b)
}

func EmitTunConfig(w *pasta.Workspace, node, link uint64, cfg TunConfig) bool {
	return nodekit.EmitTunConfig(w, node, link, cfg)
}

func restoredLeft(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredLeft(restored, index)
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredRight(restored, index)
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

func splitDefaultTunList(value string) []string {
	return nodekit.SplitListText(value)
}

func configTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	return nodekit.ConfigTextField(id, label, value, statusErr, statusText, multiline)
}

func configSecretNameField(id, label, value string, statusErr bool, statusText string) formular.Item {
	return nodekit.ConfigSecretNameField(id, label, value, statusErr, statusText)
}

func configIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	return nodekit.ConfigIntField(id, label, value, statusErr, statusText)
}

func readonlyTextField(id, label, value string) formular.Item {
	return nodekit.ReadonlyTextField(id, label, value)
}

func readonlyStringListField(id, label string, values []string) formular.Item {
	return nodekit.ReadonlyStringListField(id, label, values)
}

func readonlyIntField(id, label string, value int) formular.Item {
	return nodekit.ReadonlyIntField(id, label, value)
}
