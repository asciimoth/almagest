package nodekit

import (
	"strconv"
	"strings"

	"github.com/asciimoth/pasta/pasta"
)

// SlotPortReview controls which managed slot ports stay on a node.
type SlotPortReview struct {
	DefaultSlot int
	EnsureFree  bool
	// PreserveExisting keeps restored ports until their links are restored.
	PreserveExisting bool
	// ExtraOrder contains same-direction ports that the slot manager does not own.
	ExtraOrder []uint64
}

// SlotPorts manages a numbered set of dynamic node ports.
type SlotPorts struct {
	w         *pasta.Workspace
	node      uint64
	direction string
	max       int
	ports     []uint64
	newPort   func(slot int) pasta.Port
	name      func(slot int) string
	parseName func(name string) (int, bool)

	deferredReview bool
}

// Max returns the highest managed slot number.
func (s *SlotPorts) Max() int {
	if s == nil {
		return 0
	}
	return s.max
}

// NewSlotPorts creates a dynamic slot-port manager.
func NewSlotPorts(
	max int,
	direction string,
	newPort func(slot int) pasta.Port,
	name func(slot int) string,
	parseName func(name string) (int, bool),
) SlotPorts {
	return SlotPorts{
		direction: direction,
		max:       max,
		ports:     make([]uint64, max+1),
		newPort:   newPort,
		name:      name,
		parseName: parseName,
	}
}

// RestoredRightPorts returns all restored right-directed ports.
func RestoredRightPorts(restored *pasta.NodeInitData) []uint64 {
	if restored == nil {
		return nil
	}
	return restored.RightPorts
}

// Init binds the manager to its node and restores known slot ports.
func (s *SlotPorts) Init(w *pasta.Workspace, node uint64, restored []uint64) {
	s.w = w
	s.node = node
	if s.max < 1 {
		return
	}
	if len(s.ports) != s.max+1 {
		s.ports = make([]uint64, s.max+1)
	}
	for i, port := range restored {
		slot := s.slotFromPort(port, i+1)
		if slot >= 1 && slot <= s.max && s.ports[slot] == 0 {
			s.ports[slot] = port
		}
	}
}

// Slot returns the slot number for a port, or zero when the port is unknown.
func (s *SlotPorts) Slot(port uint64) int {
	for slot, id := range s.ports {
		if id == port {
			return slot
		}
	}
	return 0
}

// Port returns the port for a slot, or zero when the slot has no port.
func (s *SlotPorts) Port(slot int) uint64 {
	if slot < 1 || slot > s.max || slot >= len(s.ports) {
		return 0
	}
	return s.ports[slot]
}

// Remove forgets a managed port and returns its slot number.
func (s *SlotPorts) Remove(port uint64) int {
	slot := s.Slot(port)
	if slot != 0 {
		s.ports[slot] = 0
	}
	return slot
}

// HasLinks reports whether a managed port has links.
func (s *SlotPorts) HasLinks() bool {
	for slot := 1; slot <= s.max; slot++ {
		if PortHasLinks(s.w, s.Port(slot)) {
			return true
		}
	}
	return false
}

// ForEach calls fn for each managed port.
func (s *SlotPorts) ForEach(fn func(slot int, port uint64)) {
	if fn == nil {
		return
	}
	for slot := 1; slot <= s.max; slot++ {
		if port := s.Port(slot); port != 0 {
			fn(slot, port)
		}
	}
}

// Review updates the node ports to match the desired slots.
func (s *SlotPorts) Review(desired map[int]struct{}, opts SlotPortReview) error {
	if desired == nil {
		desired = map[int]struct{}{}
	}
	for slot := range desired {
		if slot < 1 || slot > s.max {
			delete(desired, slot)
		}
	}
	if opts.PreserveExisting {
		for slot := 1; slot <= s.max; slot++ {
			if s.Port(slot) != 0 {
				desired[slot] = struct{}{}
			}
		}
		s.deferredReview = true
	}
	for slot := 1; slot <= s.max; slot++ {
		if port := s.Port(slot); port != 0 && PortHasLinks(s.w, port) {
			desired[slot] = struct{}{}
		}
	}
	if len(desired) == 0 && opts.DefaultSlot >= 1 && opts.DefaultSlot <= s.max {
		desired[opts.DefaultSlot] = struct{}{}
	}
	if opts.EnsureFree {
		for slot := 1; slot <= s.max; slot++ {
			if _, used := desired[slot]; !used {
				desired[slot] = struct{}{}
				break
			}
		}
	}

	for slot := 1; slot <= s.max; slot++ {
		port := s.Port(slot)
		_, keep := desired[slot]
		if !keep && port != 0 && !PortHasLinks(s.w, port) {
			s.w.RemovePortLocked(port)
			s.ports[slot] = 0
			continue
		}
		if keep && port == 0 {
			port := s.newPort(slot)
			port.Node = s.node
			port.Direction = s.direction
			id, err := s.w.AddPortLocked(port)
			if err != nil {
				return err
			}
			s.ports[slot] = id
			continue
		}
		if keep && port != 0 {
			_ = s.w.SetPortNameLocked(port, s.name(slot))
		}
	}

	order := make([]uint64, 0, len(desired)+len(opts.ExtraOrder))
	for slot := 1; slot <= s.max; slot++ {
		if port := s.Port(slot); port != 0 {
			order = append(order, port)
		}
	}
	for _, port := range opts.ExtraOrder {
		if port != 0 {
			order = append(order, port)
		}
	}
	if len(order) == 0 {
		return nil
	}
	return s.w.SetNodePortOrderLocked(s.node, s.direction, order)
}

// TakeDeferredReview reports and clears a pending post-restore review.
func (s *SlotPorts) TakeDeferredReview() bool {
	if !s.deferredReview {
		return false
	}
	s.deferredReview = false
	return true
}

func (s *SlotPorts) slotFromPort(port uint64, fallback int) int {
	if s.w == nil || s.parseName == nil {
		return fallback
	}
	snapshot, ok := s.w.PortSnapshotLocked(port)
	if !ok {
		return fallback
	}
	if slot, ok := s.parseName(snapshot.Name); ok {
		return slot
	}
	return fallback
}

// SlotPortName returns the standard display name for a numbered slot.
func SlotPortName(slot int) string {
	return "slot " + strconv.Itoa(slot)
}

// SlotPortFromName parses a standard numbered slot name.
func SlotPortFromName(name string, max int) (int, bool) {
	parts := strings.Fields(strings.ToLower(name))
	if len(parts) != 2 || parts[0] != "slot" {
		return 0, false
	}
	slot, err := strconv.Atoi(parts[1])
	if err != nil || slot < 1 || slot > max {
		return 0, false
	}
	return slot, true
}
