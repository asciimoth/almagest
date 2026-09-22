package dnsnodes

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTun2DNSPassesConfiguredRequestTimeout(t *testing.T) {
	upstream := newDeadlineRecordingDNS()
	t.Cleanup(func() { _ = upstream.Close() })
	n := &tun2DNSNode{requestTimeoutMS: 1000}
	adapter := n.newAdapterLocked(0, nil, upstream)
	t.Cleanup(func() { _ = adapter.Close() })

	adapter.FeedPacket(tun2DNSInternalTestPacket(t, dnsTestQuery("timeout.tun2dns.test.", dns.TypeA)))
	select {
	case remaining := <-upstream.deadlines:
		if remaining <= 0 || remaining > 1500*time.Millisecond {
			t.Fatalf("upstream request deadline = %v, want configured 1s timeout", remaining)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream DNS request")
	}
}

func TestTun2DnsStalePumpFailureCannotOverwriteReplacementLabel(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(16))
	t.Cleanup(w.Close)
	id, err := w.AddNode(&tun2DNSLabelTestNode{}, "test/Tun2DNSLabel", "label owner")
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := w.SetNodeLabel(id, "adapting DNS"); err != nil {
		t.Fatalf("SetNodeLabel: %v", err)
	}
	failedTun, failedPeer := tun.Pipe(1, 1500, 0, 0)
	replacementTun, replacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = failedTun.Close()
		_ = failedPeer.Close()
		_ = replacementTun.Close()
		_ = replacementPeer.Close()
	})
	n := &tun2DNSNode{
		currentTun: failedTun,
		generation: 1,
	}
	n.W = w
	n.ID = id

	// Hold the workspace lock after the failure state changes but before its
	// label can publish. This models a replacement payload delivered between
	// the old pump failure and its asynchronous label update.
	w.Lock()
	done := make(chan struct{})
	go func() {
		n.failTunPump(1, failedTun, errors.New("old pump failed"))
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		failed := n.tunFailed
		n.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			w.Unlock()
			t.Fatal("old pump did not record its failure")
		}
		time.Sleep(time.Millisecond)
	}
	n.mu.Lock()
	n.currentTun = replacementTun
	n.generation = 2
	n.tunFailed = false
	n.lastErr = ""
	n.mu.Unlock()
	w.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old pump failure did not finish")
	}
	snapshot, ok := w.NodeSnapshot(id)
	if !ok {
		t.Fatal("label owner node is missing")
	}
	if snapshot.Label != "adapting DNS" {
		t.Fatalf("node label = %q, want replacement label", snapshot.Label)
	}
}

type tun2DNSLabelTestNode struct {
	pasta.BasicNode
}

type deadlineRecordingDNS struct {
	provider  *dnsProvider
	deadlines chan time.Duration
}

func newDeadlineRecordingDNS() *deadlineRecordingDNS {
	d := &deadlineRecordingDNS{deadlines: make(chan time.Duration, 1)}
	d.provider = newDNSProvider(d.handle)
	return d
}

func (d *deadlineRecordingDNS) Requests() chan<- dns.Request { return d.provider.Requests() }
func (d *deadlineRecordingDNS) Close() error                 { return d.provider.Close() }

func (d *deadlineRecordingDNS) handle(_ context.Context, req dns.Request) {
	deadline, ok := req.Context.Deadline()
	if !ok {
		d.deadlines <- -1
	} else {
		d.deadlines <- time.Until(deadline)
	}
	sendDNSResponse(req, nil, errors.New("recorded request deadline"))
}

func tun2DNSInternalTestPacket(t *testing.T, msg *dns.Message) []byte {
	t.Helper()
	payload, err := dns.Pack(msg)
	if err != nil {
		t.Fatalf("dns.Pack: %v", err)
	}
	udpLength := 8 + len(payload)
	totalLength := 20 + udpLength
	packet := make([]byte, totalLength)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLength))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], []byte{10, 0, 0, 2})
	copy(packet[16:20], []byte{10, 0, 0, 53})
	binary.BigEndian.PutUint16(packet[20:22], 53000)
	binary.BigEndian.PutUint16(packet[22:24], 53)
	binary.BigEndian.PutUint16(packet[24:26], uint16(udpLength))
	copy(packet[28:], payload)
	return packet
}
