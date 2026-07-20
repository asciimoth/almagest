package almagest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

// debugLogDNS logs DNS request/response traffic while preserving the
// channel-based dns.Interface contract of its wrapped per-link DNS interface.
type debugLogDNS struct {
	upstream dns.Interface
	logger   pasta.Logger
	requests chan dns.Request

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
}

func newDebugLogDNS(upstream dns.Interface, logger pasta.Logger) *debugLogDNS {
	ctx, cancel := context.WithCancel(context.Background())
	d := &debugLogDNS{
		upstream: upstream,
		logger:   logger,
		requests: make(chan dns.Request),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go d.run()
	return d
}

func (d *debugLogDNS) Requests() chan<- dns.Request { return d.requests }

func (d *debugLogDNS) Close() error {
	d.log("dns.Close")
	d.once.Do(func() {
		d.cancel()
		closeDNS(d.upstream)
		<-d.done
		d.wg.Wait()
	})
	return nil
}

func (d *debugLogDNS) GetWrapped() any { return d.upstream }

func (d *debugLogDNS) IsUp() (bool, error) {
	select {
	case <-d.ctx.Done():
		return false, nil
	default:
	}
	if d.upstream == nil {
		return false, nil
	}
	updown, ok := d.upstream.(interface {
		IsUp() (bool, error)
	})
	if !ok {
		return true, nil
	}
	return updown.IsUp()
}

func (d *debugLogDNS) run() {
	defer close(d.done)
	for {
		select {
		case req := <-d.requests:
			d.wg.Add(1)
			go d.forward(req)
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *debugLogDNS) forward(req dns.Request) {
	defer d.wg.Done()
	d.logf("dns.Request %s", dnsLogMessageSummary(req.Message))
	ctx, cancel := dnsRequestContext(d.ctx, req)
	defer cancel()
	resp, err := dns.Query(ctx, d.upstream, req.Message)
	if err != nil {
		d.logf("dns.Response %s error=%v", dnsLogMessageSummary(req.Message), err)
		sendDNSResponse(req, nil, err)
		return
	}
	d.logf("dns.Response %s", dnsLogMessageSummary(resp))
	sendDNSResponse(req, resp, nil)
}

func (d *debugLogDNS) log(message string) {
	if d.logger != nil {
		d.logger.Debug(message)
	}
}

func (d *debugLogDNS) logf(format string, args ...any) {
	if d.logger != nil {
		d.logger.Debugf(format, args...)
	}
}

func dnsLogMessageSummary(msg *dns.Message) string {
	if msg == nil {
		return "message=<nil>"
	}
	parts := []string{
		fmt.Sprintf("id=%d", msg.ID),
		fmt.Sprintf("response=%t", msg.Response),
	}
	if msg.Response {
		parts = append(parts, fmt.Sprintf("rcode=%s", dnsRCodeName(msg.RCode)))
	}
	if len(msg.Questions) > 0 {
		questions := make([]string, 0, len(msg.Questions))
		for _, q := range msg.Questions {
			questions = append(questions, q.Name+" "+dnsTypeName(q.Type))
		}
		parts = append(parts, "questions="+strings.Join(questions, ","))
	}
	if msg.Response {
		parts = append(parts,
			fmt.Sprintf("answers=%d", len(msg.Answers)),
			fmt.Sprintf("authorities=%d", len(msg.Authorities)),
			fmt.Sprintf("additionals=%d", len(msg.Additionals)),
		)
	}
	return strings.Join(parts, " ")
}

var _ dns.Interface = (*debugLogDNS)(nil)
