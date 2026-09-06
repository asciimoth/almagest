package nodekit

import (
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// RequestPort sends std.Request on every link attached to port.
func (b *Base) RequestPort(port uint64) {
	RequestPortLinks(b.W, b.ID, port)
}

// ForEachPortLink calls fn for every link attached to port.
func (b *Base) ForEachPortLink(port uint64, fn func(link uint64)) {
	ForEachPortLink(b.W, port, fn)
}

// PortHasLinks reports whether port has at least one attached link.
func (b *Base) PortHasLinks(port uint64) bool {
	return PortHasLinks(b.W, port)
}

// EmitIfLinked emits payload on link if the link still exists.
func (b *Base) EmitIfLinked(link uint64, payload any) bool {
	return EmitIfLinked(b.W, b.ID, link, payload)
}

// SetLabel updates the node label when the base has been initialized.
func (b *Base) SetLabel(label string) error {
	if b == nil || b.W == nil || b.ID == 0 {
		return nil
	}
	return b.W.SetNodeLabelLocked(b.ID, label)
}

// SetPrimary updates the node primary type when the base has been initialized.
func (b *Base) SetPrimary(primaryType string) error {
	if b == nil || b.W == nil || b.ID == 0 {
		return nil
	}
	return b.W.SetNodePrimaryLocked(b.ID, primaryType)
}

// RequestPortLinks sends std.Request on every link attached to port.
func RequestPortLinks(w *pasta.Workspace, node, port uint64) {
	ForEachPortLink(w, port, func(link uint64) {
		std.RequestLocked(w, node, link)
	})
}

// ForEachPortLink calls fn for every link attached to port.
func ForEachPortLink(w *pasta.Workspace, port uint64, fn func(link uint64)) {
	if w == nil || port == 0 || fn == nil {
		return
	}
	snapshot, ok := w.PortSnapshotLocked(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		fn(link)
	}
}

// PortHasLinks reports whether port has at least one attached link.
func PortHasLinks(w *pasta.Workspace, port uint64) bool {
	if w == nil || port == 0 {
		return false
	}
	snapshot, ok := w.PortSnapshotLocked(port)
	return ok && len(snapshot.Links) > 0
}

// InitialNodeName returns the workspace-owned node name available during
// OnInit. The workspace record is authoritative when it exists. Restored is a
// fallback when the record is unavailable. The caller must hold the workspace
// lock when w is not nil.
func InitialNodeName(w *pasta.Workspace, node uint64, restored *pasta.NodeInitData) string {
	if w != nil && node != 0 {
		if snapshot, ok := w.NodeSnapshotLocked(node); ok && snapshot.Name != "" {
			return snapshot.Name
		}
	}
	if restored != nil {
		return restored.Name
	}
	return ""
}

// RestoredLeft returns a restored left port by index.
func RestoredLeft(restored *pasta.NodeInitData, index int) uint64 {
	if restored == nil || index < 0 || index >= len(restored.LeftPorts) {
		return 0
	}
	return restored.LeftPorts[index]
}

// RestoredRight returns a restored right port by index.
func RestoredRight(restored *pasta.NodeInitData, index int) uint64 {
	if restored == nil || index < 0 || index >= len(restored.RightPorts) {
		return 0
	}
	return restored.RightPorts[index]
}

// EmitIfLinked emits payload on link if the link still exists.
func EmitIfLinked(w *pasta.Workspace, node, link uint64, payload any) bool {
	if w == nil || link == 0 {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	w.EmitEventLocked(node, link, payload)
	return true
}

// SendNodeMenuSnapshot sends a full node menu snapshot.
func SendNodeMenuSnapshot(w *pasta.Workspace, node uint64, force bool, blocks ...formular.Block) {
	if w == nil || node == 0 {
		return
	}
	menu := pasta.NodeMenuID(node)
	if force {
		w.SendNodeMenuMsgLocked(node, formular.ForcedMenuSnapshot(menu, 1, blocks...))
		return
	}
	w.SendNodeMenuMsgLocked(node, formular.MenuSnapshot(menu, 1, blocks...))
}

// SendNodeBlockSnapshot sends one node menu block snapshot.
func SendNodeBlockSnapshot(w *pasta.Workspace, node uint64, block formular.Block) {
	if w == nil || node == 0 {
		return
	}
	w.SendNodeMenuMsgLocked(node, formular.BlockSnapshot(pasta.NodeMenuID(node), 1, block.Generation, block))
}

// SendNodeFieldStatus sends validation state for one node menu field.
func SendNodeFieldStatus(w *pasta.Workspace, node uint64, field formular.FieldRef, err error) {
	if w == nil || node == 0 {
		return
	}
	w.SendNodeMenuMsgLocked(node, formular.FieldStatusFromError(pasta.NodeMenuID(node), 1, 1, field, err))
}

// SleepOrStopped sleeps for d or returns false when stop is closed first.
func SleepOrStopped(stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

// AppendCappedLog appends one log line and keeps at most max entries.
func AppendCappedLog(logs *[]formular.LogLine, max int, level, text string) {
	if logs == nil || max <= 0 {
		return
	}
	*logs = append(*logs, formular.LogLine{Level: level, Text: text})
	if len(*logs) > max {
		*logs = append([]formular.LogLine(nil), (*logs)[len(*logs)-max:]...)
	}
}

// LogsBlock builds a read-only logs block.
func LogsBlock(id, label string, order int, logs []formular.LogLine) formular.Block {
	return formular.Block{ID: id, Order: order, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLogs, ID: "worker", Label: label, Logs: append([]formular.LogLine(nil), logs...)},
	}}
}
