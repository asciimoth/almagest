package almagest

import (
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// NodeBase carries common runtime handles and mechanical workspace helpers for
// node implementations.
type NodeBase struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger
	w      *pasta.Workspace
	id     uint64
}

// InitBaseNode stores runtime handles provided by a node's OnInit callback.
func (b *NodeBase) InitBaseNode(w *pasta.Workspace, logger pasta.Logger, id uint64) {
	b.w = w
	b.logger = logger
	b.id = id
}

// RequestPort sends std.Request on every link attached to port.
func (b *NodeBase) RequestPort(port uint64) {
	requestPortLinks(b.w, b.id, port)
}

// ForEachPortLink calls fn for every link attached to port.
func (b *NodeBase) ForEachPortLink(port uint64, fn func(link uint64)) {
	forEachPortLink(b.w, port, fn)
}

// PortHasLinks reports whether port has at least one attached link.
func (b *NodeBase) PortHasLinks(port uint64) bool {
	return portHasLinks(b.w, port)
}

// EmitIfLinked emits payload on link if the link still exists.
func (b *NodeBase) EmitIfLinked(link uint64, payload any) bool {
	return emitIfLinked(b.w, b.id, link, payload)
}

// SetLabel updates the node label when the base has been initialized.
func (b *NodeBase) SetLabel(label string) error {
	if b.w == nil || b.id == 0 {
		return nil
	}
	return b.w.SetNodeLabel(b.id, label)
}

// SetPrimary updates the node primary type when the base has been initialized.
func (b *NodeBase) SetPrimary(primaryType string) error {
	if b.w == nil || b.id == 0 {
		return nil
	}
	return b.w.SetNodePrimary(b.id, primaryType)
}

func requestPortLinks(w *pasta.Workspace, node, port uint64) {
	forEachPortLink(w, port, func(link uint64) {
		std.Request(w, node, link)
	})
}

func forEachPortLink(w *pasta.Workspace, port uint64, fn func(link uint64)) {
	if w == nil || port == 0 || fn == nil {
		return
	}
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		fn(link)
	}
}

func portHasLinks(w *pasta.Workspace, port uint64) bool {
	if w == nil || port == 0 {
		return false
	}
	snapshot, ok := w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

func restoredLeft(restored *pasta.NodeInitData, index int) uint64 {
	if restored == nil || index < 0 || index >= len(restored.LeftPorts) {
		return 0
	}
	return restored.LeftPorts[index]
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	if restored == nil || index < 0 || index >= len(restored.RightPorts) {
		return 0
	}
	return restored.RightPorts[index]
}

func emitIfLinked(w *pasta.Workspace, node, link uint64, payload any) bool {
	if w == nil || link == 0 {
		return false
	}
	if _, ok := w.LinkSnapshot(link); !ok {
		return false
	}
	w.EmitEvent(node, link, payload)
	return true
}

func sendNodeMenuSnapshot(w *pasta.Workspace, node uint64, force bool, blocks ...formular.Block) {
	if w == nil || node == 0 {
		return
	}
	menu := pasta.NodeMenuID(node)
	if force {
		w.SendNodeMenuMsg(node, formular.ForcedMenuSnapshot(menu, 1, blocks...))
		return
	}
	w.SendNodeMenuMsg(node, formular.MenuSnapshot(menu, 1, blocks...))
}

func sendNodeBlockSnapshot(w *pasta.Workspace, node uint64, block formular.Block) {
	if w == nil || node == 0 {
		return
	}
	w.SendNodeMenuMsg(node, formular.BlockSnapshot(pasta.NodeMenuID(node), 1, block.Generation, block))
}

func sendNodeFieldStatus(w *pasta.Workspace, node uint64, field formular.FieldRef, err error) {
	if w == nil || node == 0 {
		return
	}
	w.SendNodeMenuMsg(node, formular.FieldStatusFromError(pasta.NodeMenuID(node), 1, 1, field, err))
}
