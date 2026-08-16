package socksnodes

import (
	"crypto/tls"
	"fmt"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	DefaultTLSConfigName = nodekit.DefaultTLSConfigName
	TypeNetwork          = nodekit.TypeNetwork
	nodeTLSConfigFieldID = nodekit.TLSConfigFieldID
)

type NetworkCloser = nodekit.NetworkCloser
type NetworkPayload = nodekit.NetworkPayload
type NetworkOutputCache = nodekit.NetworkOutputCache
type NodeBase = nodekit.Base
type Runtime = nodekit.Runtime
type restartableWorker = nodekit.RestartableWorker
type TlsConfigChanged = nodekit.TlsConfigChanged
type WorkerSpawner = nodekit.WorkerSpawner

func NetworkPort(direction, name string) pasta.Port {
	return nodekit.NetworkPort(direction, name)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	nodekit.BindNetworkResource(w, node, link, n)
}

func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return nodekit.AcceptNetworkPayload(w, node, event, current)
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	return nodekit.SameNetworkInstance(a, b)
}

func SameComparableValue(a, b any) bool {
	return nodekit.SameComparableValue(a, b)
}

func NetworkIsReady(n NetworkCloser) bool {
	return nodekit.NetworkIsReady(n)
}

func closeNetwork(n NetworkCloser) error {
	return nodekit.CloseNetwork(n)
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

func appendCappedLog(logs *[]formular.LogLine, max int, level, text string) {
	nodekit.AppendCappedLog(logs, max, level, text)
}

func logsBlock(id, label string, order int, logs []formular.LogLine) formular.Block {
	return nodekit.LogsBlock(id, label, order, logs)
}

func sleepOrStopped(stop <-chan struct{}, d time.Duration) bool {
	return nodekit.SleepOrStopped(stop, d)
}

func enginePool(runtime nodekit.Runtime) bufpool.Pool {
	if runtime == nil {
		return nil
	}
	return runtime.Pool()
}

type nodeTLSConfigResult struct {
	config          *tls.Config
	effectiveName   string
	missingSelected bool
}

func readNodeTLSConfigName(cfg configer.Config) string {
	return nodekit.ReadTLSConfigName(cfg)
}

func nodeTLSConfigValueFromAny(raw any) (string, bool) {
	return nodekit.TLSConfigValueFromAny(raw)
}

func normalizeNodeTLSConfigName(value string) string {
	return nodekit.NormalizeTLSConfigName(value)
}

func resolveNodeTLSClientConfig(base nodekit.NodeContext, selectedName string) (nodeTLSConfigResult, error) {
	result, err := nodekit.ResolveTLSClientConfig(base, selectedName)
	return nodeTLSConfigResult{
		config:          result.Config,
		effectiveName:   result.EffectiveName,
		missingSelected: result.MissingSelected,
	}, err
}

func cloneTLSClientConfig(cfg *tls.Config) *tls.Config {
	return nodekit.CloneTLSClientConfig(cfg)
}

func nodeTLSConfigSelectorItem(base nodekit.NodeContext, selectedName, errText string) formular.Item {
	return nodekit.TLSConfigSelectorItem(base, selectedName, errText)
}

func handleNodeTLSConfigChanged(selectedName, effectiveName string, msg TlsConfigChanged) (string, bool) {
	return nodekit.HandleTLSConfigChanged(selectedName, effectiveName, msg)
}

func errNodeTLSConfig(errText string) error {
	return fmt.Errorf("TLS config: %s", errText)
}
