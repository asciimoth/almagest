package debugnodes

import (
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const TypeNetwork = nodekit.TypeNetwork

type NetworkCloser = nodekit.NetworkCloser
type NetworkOutputCache = nodekit.NetworkOutputCache
type NodeBase = nodekit.Base
type Runtime = nodekit.Runtime
type WorkerSpawner = nodekit.WorkerSpawner
type restartableWorker = nodekit.RestartableWorker

func NetworkPort(direction, name string) pasta.Port {
	return nodekit.NetworkPort(direction, name)
}

func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return nodekit.AcceptNetworkPayload(w, node, event, current)
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

func appendCappedLog(logs *[]formular.LogLine, max int, level, text string) {
	nodekit.AppendCappedLog(logs, max, level, text)
}

func logsBlock(id, label string, order int, logs []formular.LogLine) formular.Block {
	return nodekit.LogsBlock(id, label, order, logs)
}

func parsePort(value string, fallback int) int {
	return nodekit.ParsePort(value, fallback)
}
