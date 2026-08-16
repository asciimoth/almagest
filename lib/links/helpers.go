package links

import (
	"reflect"

	"github.com/asciimoth/pasta/pasta"
)

// TypeDNS is the Pasta link type carrying DNS resolver values. The DNS node
// package owns the DNS payload contract; this package keeps the shared type
// identifier so package-neutral code can refer to it without importing nodes.
const TypeDNS = "almagest/dns"

func forEachPortLink(w *pasta.Workspace, port uint64, fn func(link uint64)) {
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

func emitIfLinked(w *pasta.Workspace, node, link uint64, payload any) bool {
	if w == nil || link == 0 {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	w.EmitEventLocked(node, link, payload)
	return true
}

// SameComparableValue reports whether a and b have the same comparable value.
//
// Values with different dynamic types, non-comparable values, and one-sided nil
// values are not equal.
func SameComparableValue(a, b any) bool {
	av := reflect.ValueOf(a)
	bv := reflect.ValueOf(b)
	if !av.IsValid() || !bv.IsValid() {
		return !av.IsValid() && !bv.IsValid()
	}
	if av.Type() != bv.Type() || !av.Type().Comparable() {
		return false
	}
	return av.Interface() == bv.Interface()
}
