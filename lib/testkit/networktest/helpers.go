package networktest

import (
	"testing"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/pasta/pasta"
)

// Receiver is a test node that receives almagest/network payloads.
type Receiver struct {
	pasta.BasicNode
	ID     uint64
	In     uint64
	Events chan links.NetworkPayload
}

// NewReceiver returns a network receiver test node.
func NewReceiver(size int) *Receiver {
	return &Receiver{Events: make(chan links.NetworkPayload, size)}
}

func (n *Receiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.ID = id
	port, err := w.AddPortLocked(pasta.Port{
		Node:      id,
		Direction: "right",
		Name:      "Network",
		Types:     []string{links.TypeNetwork},
	})
	if err != nil {
		return err
	}
	n.In = port
	return nil
}

func (n *Receiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.In || portDirection != "right" || linkType != links.TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *Receiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.In || receiverPortDirection != "right" || linkType != links.TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(links.NetworkPayload)
	if ok {
		n.Events <- payload
	}
	return nil
}

// ReceivePayload returns one network payload or fails the test.
func ReceivePayload(t *testing.T, events <-chan links.NetworkPayload) links.NetworkPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Network == nil {
			t.Fatal("payload.Network = nil, want network")
		}
		return payload
	default:
		t.Fatal("no network payload received")
		return links.NetworkPayload{}
	}
}

// AssertNoEvent fails if a network payload is ready.
func AssertNoEvent(t *testing.T, events <-chan links.NetworkPayload) {
	t.Helper()
	select {
	case payload := <-events:
		t.Fatalf("unexpected network payload: %#v", payload)
	default:
	}
}
