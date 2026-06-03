package lib

import (
	"io"
	"reflect"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// TypeNetwork is the Pasta link type carrying Network values backed by
// gonnect.Network+io.Closer.
//
// Values flow from left-directed ports to right-directed ports, which
// corresponds to right-to-left graph data flow. A node owning a left-directed
// pasta/network port sends its current Network instance, whose underlying type
// implements gonnect.Network+io.Closer, on every connected link when a new
// link connects, when OnReady runs, when the owned Network instance is replaced,
// and when the peer sends std.RequestValue from the right-directed side.
// If a left-directed port does not yet have a ready Network instance, it
// defers sending until a value becomes available, then broadcasts to all
// connected peers. A node owning a right-directed pasta/network port may send
// RequestValue when it needs the current Network object again. Both sides must
// register any received Network object with the Workspace resource tracking
// system, bound to the owning node and link. The event payload implements
// std.ClosablePayload, so middleware nodes such as std.SelectClass can close a
// previously-routed wrapper when switching paths. Peers that only implement
// gonnect.Network without io.Closer should wrap their instance via
// gonnect.DetachNetwork before using it with this link type. Left-directed
// pasta/network ports allow an arbitrary count of incoming links; right-directed
// pasta/network ports allow at most one outgoing link.
const TypeNetwork = "almagest/network"

var (
	_ std.ClosablePayload = &NetworkPayload{}
)

type NetworkCloser interface {
	gonnect.Network
	io.Closer
}

type NetworkPayload struct {
	Network NetworkCloser
}

func (p NetworkPayload) Close() error {
	if p.Network == nil {
		return nil
	}
	return p.Network.Close()
}

func NetworkPort(direction, name string) pasta.Port {
	return pasta.Port{Direction: direction, Name: name, Types: []string{TypeNetwork}}
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	if w == nil || n == nil {
		return
	}
	_ = w.AddNodeResource(node, n)
	if link != 0 {
		_ = w.AddLinkResource(link, n)
	}
}

func SameNetworkSource(a, b NetworkCloser) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(NetworkSource(a), NetworkSource(b))
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

func NetworkSource(n any) any {
	for n != nil {
		wrapped := gonnect.GetWrapped(n)
		if wrapped == nil {
			return n
		}
		n = wrapped
	}
	return nil
}

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

func NetworkIsReady(n NetworkCloser) bool {
	if n == nil {
		return false
	}
	updown, ok := n.(interface {
		IsUp() (bool, error)
	})
	if !ok {
		return true
	}
	up, err := updown.IsUp()
	return err == nil && up
}
