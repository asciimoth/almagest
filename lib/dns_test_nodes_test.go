package almagest

import (
	"testing"
	"time"

	"github.com/asciimoth/pasta/pasta"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
)

type dnsReceiverNode struct {
	pasta.BasicNode
	in     uint64
	events chan dnsnodes.DNSPayload
}

func (n *dnsReceiverNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
		Node: id, Direction: "right", Name: "DNS", Types: []string{dnsnodes.TypeDNS},
	})
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *dnsReceiverNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "right" || linkType != dnsnodes.TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *dnsReceiverNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != dnsnodes.TypeDNS {
		return nil
	}
	payload, ok := event.Payload.(dnsnodes.DNSPayload)
	if ok {
		n.events <- payload
	}
	return nil
}

func receiveDNSPayload(t *testing.T, ch <-chan dnsnodes.DNSPayload) dnsnodes.DNSPayload {
	t.Helper()
	select {
	case payload := <-ch:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DNS payload")
		return dnsnodes.DNSPayload{}
	}
}
