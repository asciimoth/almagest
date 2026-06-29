package almagest

import (
	"strconv"
	"strings"

	"github.com/asciimoth/pasta/pasta"
)

type slotPortReview struct {
	DefaultSlot int
	EnsureFree  bool
}

type slotPorts struct {
	w         *pasta.Workspace
	node      uint64
	direction string
	max       int
	ports     []uint64
	newPort   func(slot int) pasta.Port
	name      func(slot int) string
	parseName func(name string) (int, bool)
}

func newSlotPorts(max int, direction string, newPort func(slot int) pasta.Port, name func(slot int) string, parseName func(name string) (int, bool)) slotPorts {
	return slotPorts{
		direction: direction,
		max:       max,
		ports:     make([]uint64, max+1),
		newPort:   newPort,
		name:      name,
		parseName: parseName,
	}
}

func restoredRightPorts(restored *pasta.NodeInitData) []uint64 {
	if restored == nil {
		return nil
	}
	return restored.RightPorts
}

func (s *slotPorts) Init(w *pasta.Workspace, node uint64, restored []uint64) {
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

func (s *slotPorts) Slot(port uint64) int {
	for slot, id := range s.ports {
		if id == port {
			return slot
		}
	}
	return 0
}

func (s *slotPorts) Port(slot int) uint64 {
	if slot < 1 || slot > s.max || slot >= len(s.ports) {
		return 0
	}
	return s.ports[slot]
}

func (s *slotPorts) Remove(port uint64) int {
	slot := s.Slot(port)
	if slot != 0 {
		s.ports[slot] = 0
	}
	return slot
}

func (s *slotPorts) HasLinks() bool {
	for slot := 1; slot <= s.max; slot++ {
		if s.portHasLinks(s.Port(slot)) {
			return true
		}
	}
	return false
}

func (s *slotPorts) ForEach(fn func(slot int, port uint64)) {
	if fn == nil {
		return
	}
	for slot := 1; slot <= s.max; slot++ {
		if port := s.Port(slot); port != 0 {
			fn(slot, port)
		}
	}
}

func (s *slotPorts) Review(desired map[int]struct{}, opts slotPortReview) error {
	if desired == nil {
		desired = map[int]struct{}{}
	}
	for slot := range desired {
		if slot < 1 || slot > s.max {
			delete(desired, slot)
		}
	}
	for slot := 1; slot <= s.max; slot++ {
		if port := s.Port(slot); port != 0 && s.portHasLinks(port) {
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
		if !keep && port != 0 && !s.portHasLinks(port) {
			s.w.RemovePort(port)
			s.ports[slot] = 0
			continue
		}
		if keep && port == 0 {
			port := s.newPort(slot)
			port.Node = s.node
			port.Direction = s.direction
			id, err := s.w.AddPort(port)
			if err != nil {
				return err
			}
			s.ports[slot] = id
			continue
		}
		if keep && port != 0 {
			_ = s.w.SetPortName(port, s.name(slot))
		}
	}

	order := make([]uint64, 0, len(desired))
	for slot := 1; slot <= s.max; slot++ {
		if port := s.Port(slot); port != 0 {
			order = append(order, port)
		}
	}
	if len(order) == 0 {
		return nil
	}
	return s.w.SetNodePortOrder(s.node, s.direction, order)
}

func (s *slotPorts) slotFromPort(port uint64, fallback int) int {
	if s.w == nil || s.parseName == nil {
		return fallback
	}
	snapshot, ok := s.w.PortSnapshot(port)
	if !ok {
		return fallback
	}
	if slot, ok := s.parseName(snapshot.Name); ok {
		return slot
	}
	return fallback
}

func (s *slotPorts) portHasLinks(port uint64) bool {
	return portHasLinks(s.w, port)
}

func slotPortName(slot int) string {
	return "slot " + strconv.Itoa(slot)
}

func slotPortFromName(name string, max int) (int, bool) {
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
