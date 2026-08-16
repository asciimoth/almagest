package dnstest

import (
	"testing"
	"time"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/pasta/pasta"
)

// Receiver is a test node that receives almagest/dns payloads.
type Receiver struct {
	pasta.BasicNode
	In     uint64
	Events chan dnsnodes.DNSPayload
}

// NewReceiver returns a DNS receiver test node.
func NewReceiver(size int) *Receiver {
	return &Receiver{Events: make(chan dnsnodes.DNSPayload, size)}
}

func (n *Receiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
		Node:      id,
		Direction: "right",
		Name:      "DNS",
		Types:     []string{dnsnodes.TypeDNS},
	})
	if err != nil {
		return err
	}
	n.In = port
	return nil
}

func (n *Receiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.In || portDirection != "right" || linkType != dnsnodes.TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *Receiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.In || receiverPortDirection != "right" || linkType != dnsnodes.TypeDNS {
		return nil
	}
	payload, ok := event.Payload.(dnsnodes.DNSPayload)
	if ok {
		n.Events <- payload
	}
	return nil
}

// ReceivePayload returns one DNS payload or fails the test.
func ReceivePayload(t *testing.T, ch <-chan dnsnodes.DNSPayload) dnsnodes.DNSPayload {
	t.Helper()
	select {
	case payload := <-ch:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DNS payload")
		return dnsnodes.DNSPayload{}
	}
}
