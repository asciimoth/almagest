package nodekit

import (
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

func TestSlotPortNameRoundTrip(t *testing.T) {
	for slot := 1; slot <= 3; slot++ {
		name := SlotPortName(slot)
		got, ok := SlotPortFromName(name, 3)
		if !ok || got != slot {
			t.Fatalf("SlotPortFromName(%q) = %d, %v", name, got, ok)
		}
	}
	for _, name := range []string{"", "slot", "port 1", "slot zero", "slot 0", "slot 4"} {
		if slot, ok := SlotPortFromName(name, 3); ok || slot != 0 {
			t.Errorf("SlotPortFromName(%q) = %d, %v; want 0, false", name, slot, ok)
		}
	}
	if slot, ok := SlotPortFromName(" SLOT 2 ", 3); !ok || slot != 2 {
		t.Fatalf("case-insensitive parse = %d, %v", slot, ok)
	}
}

func TestSlotPortsReviewLifecycle(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(16))
	t.Cleanup(func() { w.Close() })
	node, err := w.AddNode(&pasta.BasicNode{}, "test/Slots", "slots")
	if err != nil {
		t.Fatal(err)
	}
	slots := NewSlotPorts(3, "right", func(slot int) pasta.Port {
		return pasta.Port{Name: SlotPortName(slot), Types: []string{"pasta/string"}}
	}, SlotPortName, func(name string) (int, bool) {
		return SlotPortFromName(name, 3)
	})
	if slots.Max() != 3 {
		t.Fatalf("Max() = %d, want 3", slots.Max())
	}

	w.Lock()
	slots.Init(w, node, nil)
	err = slots.Review(map[int]struct{}{2: {}, 99: {}}, SlotPortReview{EnsureFree: true})
	w.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if slots.Port(1) == 0 || slots.Port(2) == 0 || slots.Port(3) != 0 {
		t.Fatalf("ports after review = %v", slots.ports)
	}
	if slots.Slot(slots.Port(2)) != 2 || slots.Port(0) != 0 || slots.Port(4) != 0 {
		t.Fatalf("slot lookup failed for ports %v", slots.ports)
	}

	var visited []int
	slots.ForEach(func(slot int, _ uint64) { visited = append(visited, slot) })
	slots.ForEach(nil)
	if !reflect.DeepEqual(visited, []int{1, 2}) {
		t.Fatalf("visited slots = %v, want [1 2]", visited)
	}

	removedPort := slots.Port(1)
	w.Lock()
	err = slots.Review(map[int]struct{}{2: {}}, SlotPortReview{})
	w.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if slots.Port(1) != 0 {
		t.Fatalf("slot 1 port = %d after removal", slots.Port(1))
	}
	w.Lock()
	_, exists := w.PortSnapshotLocked(removedPort)
	w.Unlock()
	if exists {
		t.Fatalf("removed port %d still exists", removedPort)
	}
	if got := slots.Remove(slots.Port(2)); got != 2 || slots.Remove(999) != 0 {
		t.Fatalf("Remove() results are invalid: first=%d", got)
	}
}

func TestSlotPortsRestoreAndDeferredReview(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(16))
	t.Cleanup(func() { w.Close() })
	node, err := w.AddNode(&pasta.BasicNode{}, "test/RestoredSlots", "slots")
	if err != nil {
		t.Fatal(err)
	}
	w.Lock()
	port, err := w.AddPortLocked(pasta.Port{Node: node, Direction: "right", Name: "slot 3", Types: []string{"pasta/string"}})
	w.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	slots := NewSlotPorts(3, "right", func(slot int) pasta.Port {
		return pasta.Port{Name: SlotPortName(slot), Types: []string{"pasta/string"}}
	}, SlotPortName, func(name string) (int, bool) {
		return SlotPortFromName(name, 3)
	})
	w.Lock()
	slots.Init(w, node, []uint64{port})
	err = slots.Review(nil, SlotPortReview{PreserveExisting: true, DefaultSlot: 1})
	w.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if slots.Port(3) != port || !slots.TakeDeferredReview() || slots.TakeDeferredReview() {
		t.Fatalf("restored ports or deferred review invalid: ports=%v", slots.ports)
	}
	if !reflect.DeepEqual(RestoredRightPorts(&pasta.NodeInitData{RightPorts: []uint64{1, 2}}), []uint64{1, 2}) {
		t.Fatal("RestoredRightPorts did not return restored ports")
	}
	if RestoredRightPorts(nil) != nil {
		t.Fatal("RestoredRightPorts(nil) is not nil")
	}
}
