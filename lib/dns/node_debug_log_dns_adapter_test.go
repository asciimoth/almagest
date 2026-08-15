package dnsnodes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/dns"

	"github.com/asciimoth/almagest/lib/logging"
)

func TestDebugLogDNSLogsRequestAndResponse(t *testing.T) {
	records, err := parseStaticDNSRecords("logged.test A 192.0.2.55", DefaultStaticDNSTTL)
	if err != nil {
		t.Fatalf("parseStaticDNSRecords: %v", err)
	}
	upstream := newStaticDNSProvider(records)
	t.Cleanup(func() { _ = upstream.Close() })
	logf := logging.NewDebugLogFactory(16)
	logger := newDebugLogDNS(upstream, logf.NodeLogger(1, NodeTypeDebugLogDNS))
	t.Cleanup(func() { _ = logger.Close() })

	resp, err := dns.Query(context.Background(), logger, dnsTestQuery("logged.test.", dns.TypeA))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if resp == nil || len(resp.Answers) != 1 {
		t.Fatalf("response = %#v, want one answer", resp)
	}

	logs := debugLogMessages(logf.Drain(), NodeTypeDebugLogDNS)
	assertLogContains(t, logs, "dns.Request")
	assertLogContains(t, logs, "logged.test. A")
	assertLogContains(t, logs, "dns.Response")
	assertLogContains(t, logs, "rcode=NOERROR")
	assertLogContains(t, logs, "answers=1")
}

func TestDebugLogDNSLogsForwardError(t *testing.T) {
	wantErr := errors.New("upstream failed")
	logf := logging.NewDebugLogFactory(16)
	upstream := newFailingDNS(wantErr)
	logger := newDebugLogDNS(upstream, logf.NodeLogger(1, NodeTypeDebugLogDNS))
	t.Cleanup(func() { _ = logger.Close() })

	_, err := dns.Query(context.Background(), logger, dnsTestQuery("fail.test.", dns.TypeA))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Query error = %v, want %v", err, wantErr)
	}

	logs := debugLogMessages(logf.Drain(), NodeTypeDebugLogDNS)
	assertLogContains(t, logs, "dns.Response")
	assertLogContains(t, logs, "error=upstream failed")
}

func debugLogMessages(entries []logging.DebugLogEntry, class string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Source.NodeClass == class {
			out = append(out, entry.Message)
		}
	}
	return out
}

func assertLogContains(t *testing.T, logs []string, want string) {
	t.Helper()
	for _, log := range logs {
		if strings.Contains(log, want) {
			return
		}
	}
	t.Fatalf("logs %q do not contain %q", strings.Join(logs, "\n"), want)
}

type failingDNS struct {
	provider *dnsProvider
	err      error
}

func newFailingDNS(err error) *failingDNS {
	f := &failingDNS{err: err}
	f.provider = newDNSProvider(f.handle)
	return f
}

func (f *failingDNS) Requests() chan<- dns.Request { return f.provider.Requests() }
func (f *failingDNS) Close() error                 { return f.provider.Close() }

func (f *failingDNS) handle(_ context.Context, req dns.Request) {
	sendDNSResponse(req, nil, f.err)
}

func TestDebugLogDNSCloseCancelsBlockedRequest(t *testing.T) {
	upstream := newBlockingDNS()
	logger := newDebugLogDNS(upstream, nil)

	errs := make(chan error, 1)
	go func() {
		_, err := dns.Query(context.Background(), logger, dnsTestQuery("blocked.test.", dns.TypeA))
		errs <- err
	}()

	upstream.waitAccepted(t)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("Query error = nil, want cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked query to return")
	}
}

type blockingDNS struct {
	accepted chan struct{}
	release  chan struct{}
	once     chan struct{}
	provider *dnsProvider
}

func newBlockingDNS() *blockingDNS {
	b := &blockingDNS{
		accepted: make(chan struct{}),
		release:  make(chan struct{}),
		once:     make(chan struct{}),
	}
	b.provider = newDNSProvider(b.handle)
	return b
}

func (b *blockingDNS) Requests() chan<- dns.Request { return b.provider.Requests() }
func (b *blockingDNS) Close() error {
	close(b.release)
	return b.provider.Close()
}

func (b *blockingDNS) waitAccepted(t *testing.T) {
	t.Helper()
	select {
	case <-b.accepted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream request")
	}
}

func (b *blockingDNS) handle(ctx context.Context, req dns.Request) {
	select {
	case <-b.once:
	default:
		close(b.once)
		close(b.accepted)
	}
	select {
	case <-ctx.Done():
		sendDNSResponse(req, nil, ctx.Err())
	case <-b.release:
		sendDNSResponse(req, nil, context.Canceled)
	}
}
