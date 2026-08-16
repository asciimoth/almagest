package debugnodes

import (
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

const NodeTypeDebugErrorsWorkers = "almagest/ErrorsWorkers"

type DebugErrorsWorkersClass struct {
	Runtime Runtime
}

func (DebugErrorsWorkersClass) ClassName() string { return NodeTypeDebugErrorsWorkers }
func (DebugErrorsWorkersClass) ShortDescription() string {
	return "DEBUG node for callback errors and bad workers"
}
func (DebugErrorsWorkersClass) LongDescription() string {
	return "Provides actions that intentionally panic or spawn workers that panic or hang."
}

func (DebugErrorsWorkersClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{}}
}

func (c DebugErrorsWorkersClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugErrorsWorkersNode{NodeBase: NodeBase{Runtime: c.Runtime}}, nil
}

type debugErrorsWorkersNode struct {
	NodeBase

	workerSeq uint64
}

func (n *debugErrorsWorkersNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	sendNodeMenuSnapshot(n.W, n.ID, false, n.actionsBlock())
	return nil
}

func (n *debugErrorsWorkersNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.ButtonPressMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "actions" {
		return nil
	}

	switch msg.ButtonID {
	case "panic":
		panic("debug errors/workers node: immediate callback panic")

	case "panic-worker":
		return n.spawnPanicWorker()

	case "new-worker":
		return n.spawnHangingWorker()
	}

	return nil
}

func (n *debugErrorsWorkersNode) actionsBlock() formular.Block {
	return formular.Block{ID: "actions", Order: 10, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemButton, ID: "panic", Label: "Panic"},
		{Type: formular.ItemButton, ID: "panic-worker", Label: "Panic worker"},
		{Type: formular.ItemButton, ID: "new-worker", Label: "New worker"},
	}}
}

func (n *debugErrorsWorkersNode) spawnPanicWorker() error {
	n.workerSeq++
	name := "debug panic worker"
	seq := n.workerSeq
	return n.SpawnWorker(name, func() {
		time.Sleep(100 * time.Millisecond)
		panic("debug errors/workers node: delayed worker panic " + workerSeqString(seq))
	})
}

func (n *debugErrorsWorkersNode) spawnHangingWorker() error {
	n.workerSeq++
	name := "debug hanging worker"
	return n.SpawnWorker(name, func() {
		for {
			time.Sleep(24 * time.Hour)
		}
	})
}

func workerSeqString(seq uint64) string {
	if seq == 0 {
		return "0"
	}

	var buf [20]byte
	i := len(buf)
	for seq > 0 {
		i--
		buf[i] = byte('0' + seq%10)
		seq /= 10
	}
	return string(buf[i:])
}
