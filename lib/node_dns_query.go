package almagest

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return dnsQueryClass{engine: e}
	}, false)
}

const (
	// NodeTypeDNSQuery is the node class name for an interactive DNS query.
	NodeTypeDNSQuery = "almagest/DnsQuery"

	DefaultDNSQueryName = "example.test."
	DefaultDNSQueryType = "A"

	dnsQueryStatusReady   = "ready"
	dnsQueryStatusRunning = "running..."
	dnsQueryStatusFail    = "fail"
)

type dnsQueryClass struct {
	engine *engine
}

func (dnsQueryClass) ClassName() string        { return NodeTypeDNSQuery }
func (dnsQueryClass) ShortDescription() string { return "DNS query" }
func (dnsQueryClass) LongDescription() string {
	return "Runs DNS requests over a received almagest/dns interface in a background worker."
}
func (dnsQueryClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("right", "DNS"),
	}}
}
func (c dnsQueryClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &dnsQueryNode{
		NodeBase:   NodeBase{engine: c.engine},
		name:       configer.String(cfg, configer.Path{"name"}, DefaultDNSQueryName),
		qtype:      configer.String(cfg, configer.Path{"type"}, DefaultDNSQueryType),
		statusText: dnsQueryStatusReady,
	}, nil
}

type dnsQueryNode struct {
	NodeBase

	dnsp uint64

	resolver dns.Interface
	name     string
	qtype    string

	requestID  uint64
	pending    bool
	statusText string
	resp       string
	errText    string

	jobs chan dnsQueryRequest
	stop chan struct{}
}

func (n *dnsQueryNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.name = normalizeDNSQueryName(n.name)
	n.qtype = normalizeDNSQueryType(n.qtype)
	n.dnsp = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.startWorker()
	n.sendMenuSnapshot()
	return nil
}

func (n *dnsQueryNode) OnReady() error {
	n.RequestPort(n.dnsp)
	return nil
}

func (n *dnsQueryNode) OnStop() {
	n.stopWorker()
}

func (n *dnsQueryNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.dnsp || portDirection != "right" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	if n.PortHasLinks(port) {
		return pasta.ErrLinkDup
	}
	return nil
}

func (n *dnsQueryNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.dnsp && portDirection == "right" && linkType == TypeDNS {
		std.RequestLocked(n.w, n.id, link)
	}
	return nil
}

func (n *dnsQueryNode) OnLinkRemoved(_ uint64, port uint64, linkType, portDirection string) error {
	if port == n.dnsp && portDirection == "right" && linkType == TypeDNS {
		n.resolver = nil
		n.resetStatusReady()
	}
	return nil
}

func (n *dnsQueryNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.dnsp || receiverPortDirection != "right" || linkType != TypeDNS {
		return nil
	}
	next, changed := AcceptDNSPayload(n.w, n.id, event, n.resolver)
	if !changed {
		return nil
	}
	n.resolver = next
	n.resetStatusReady()
	return nil
}

func (n *dnsQueryNode) OnInbox(message pasta.InboxMessage) error {
	result, ok := message.Payload.(dnsQueryResult)
	if !ok || result.id != n.requestID {
		return nil
	}
	n.pending = false
	n.resp = result.body
	n.errText = result.err
	if result.err != "" {
		n.statusText = dnsQueryStatusFail
	} else if result.status != "" {
		n.statusText = result.status
	} else {
		n.statusText = dnsQueryStatusReady
	}
	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()
	return nil
}

func (n *dnsQueryNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FieldUpdateMessage:
		return n.onFieldUpdate(msg)
	case formular.ButtonPressMessage:
		return n.onButtonPress(msg)
	default:
		return nil
	}
}

func (n *dnsQueryNode) onFieldUpdate(msg formular.FieldUpdateMessage) error {
	if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "request" || len(msg.Field.ElementPath) > 0 {
		return nil
	}
	value, ok := std.StringFromPayload(msg.Value)
	if !ok {
		n.sendRequestBlock()
		return nil
	}
	switch msg.Field.FieldID {
	case "name":
		n.setName(value)
	case "type":
		n.setType(value)
	}
	return nil
}

func (n *dnsQueryNode) onButtonPress(msg formular.ButtonPressMessage) error {
	if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "request" || msg.ButtonID != "send" {
		return nil
	}
	n.sendRequest()
	return nil
}

func (n *dnsQueryNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"name": n.name,
		"type": n.qtype,
	})
}

func (n *dnsQueryNode) updateLabel() error {
	return n.SetLabel(n.currentStatusText())
}

func (n *dnsQueryNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.requestBlock(), n.resultBlock())
}

func (n *dnsQueryNode) sendRequestBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.requestBlock())
}

func (n *dnsQueryNode) sendResultBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.resultBlock())
}

func (n *dnsQueryNode) requestBlock() formular.Block {
	return formular.Block{ID: "request", Order: 10, Generation: 1, Items: []formular.Item{
		formular.TextField("name", "Name", n.name, formular.Placeholder("example.test.")),
		formular.TextField("type", "Type", n.qtype, formular.Placeholder("A")),
		formular.Button("send", "Send request"),
	}}
}

func (n *dnsQueryNode) resultBlock() formular.Block {
	return formular.Block{ID: "result", Order: 20, Generation: 1, Items: []formular.Item{
		{
			Type:   formular.ItemLabel,
			ID:     "status",
			Text:   "Status: " + n.currentStatusText(),
			Format: formular.TextPlain,
		},
		formular.ReadonlyTextField("error", "Error", n.errText),
		formular.ReadonlyTextField("response", "Response", n.resp, formular.Multiline),
	}}
}

func (n *dnsQueryNode) currentStatusText() string {
	if n.statusText == "" {
		return dnsQueryStatusReady
	}
	return n.statusText
}

func (n *dnsQueryNode) setName(value string) {
	n.name = normalizeDNSQueryName(value)
	n.sendRequestBlock()
}

func (n *dnsQueryNode) setType(value string) {
	n.qtype = normalizeDNSQueryType(value)
	n.sendRequestBlock()
}

func (n *dnsQueryNode) sendRequest() {
	if n.pending {
		n.logger.Debug("request ignored: already pending")
		return
	}
	qtype, err := staticDNSType(n.qtype)
	if err != nil {
		n.requestID++
		n.pending = false
		n.statusText = dnsQueryStatusFail
		n.resp = ""
		n.errText = err.Error()
		_ = n.updateLabel()
		n.sendResultBlock()
		return
	}
	n.requestID++
	req := dnsQueryRequest{
		id:       n.requestID,
		resolver: n.resolver,
		name:     n.name,
		qtype:    qtype,
	}
	n.pending = true
	n.statusText = dnsQueryStatusRunning
	n.resp = ""
	n.errText = ""
	n.logger.Debugf("request %d: %s %s", req.id, req.name, dnsTypeName(req.qtype))
	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()
	if n.jobs == nil {
		n.startWorker()
	}
	select {
	case n.jobs <- req:
	default:
		n.logger.Debug("request failed: worker is busy")
		n.w.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.id,
			Payload:      dnsQueryResult{id: req.id, err: "worker is busy"},
		})
	}
}

func (n *dnsQueryNode) resetStatusReady() {
	n.requestID++
	n.pending = false
	n.statusText = dnsQueryStatusReady
	n.resp = ""
	n.errText = ""
	n.restartWorker()
	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()
}

func normalizeDNSQueryName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultDNSQueryName
	}
	return absDNSName(value)
}

func normalizeDNSQueryType(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return DefaultDNSQueryType
	}
	return value
}

type dnsQueryRequest struct {
	id       uint64
	resolver dns.Interface
	name     string
	qtype    uint16
}

type dnsQueryResult struct {
	id     uint64
	status string
	body   string
	err    string
}

func (n *dnsQueryNode) startWorker() {
	if n.jobs != nil || n.stop != nil {
		return
	}
	jobs := make(chan dnsQueryRequest, 1)
	stop := make(chan struct{})
	n.jobs = jobs
	n.stop = stop
	n.logger.Debug("worker started")
	_ = n.SpawnWorker("dns query", func() {
		runDNSQueryWorker(n.engine.ctx, n.logger, n.w, n.id, jobs, stop)
	})
}

func (n *dnsQueryNode) stopWorker() {
	if n.stop != nil {
		n.logger.Debug("worker stopping")
		close(n.stop)
		n.stop = nil
	}
	n.jobs = nil
}

func (n *dnsQueryNode) restartWorker() {
	n.stopWorker()
	n.startWorker()
}

func runDNSQueryWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, jobs <-chan dnsQueryRequest, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return
		case req := <-jobs:
			logger.Debugf("request %d: worker processing", req.id)
			ctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				select {
				case <-stop:
					cancel()
				case <-done:
				}
			}()
			result := doDNSRequest(ctx, req)
			cancel()
			close(done)
			if result.err != "" {
				logger.Debugf("request %d failed: %s", req.id, result.err)
			} else {
				logger.Debugf("request %d complete: %s", req.id, result.status)
			}
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: result})
		}
	}
}

func doDNSRequest(parent context.Context, req dnsQueryRequest) dnsQueryResult {
	if req.resolver == nil {
		return dnsQueryResult{id: req.id, err: "dns is not ready"}
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	resp, err := dns.Query(ctx, req.resolver, &dns.Message{
		ID:               dns.NextID(),
		RecursionDesired: true,
		Opcode:           dns.OpcodeQuery,
		Questions: []dns.Question{{
			Name:  req.name,
			Type:  req.qtype,
			Class: dns.ClassIN,
		}},
	})
	if err != nil {
		return dnsQueryResult{id: req.id, err: err.Error()}
	}
	if resp == nil {
		return dnsQueryResult{id: req.id, err: "empty DNS response"}
	}
	status := dnsRCodeName(resp.RCode)
	return dnsQueryResult{id: req.id, status: status, body: formatDNSMessage(resp)}
}

func formatDNSMessage(msg *dns.Message) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, q := range msg.Questions {
		fmt.Fprintf(&b, "QUESTION %s %s\n", q.Name, dnsTypeName(q.Type))
	}
	for _, rr := range msg.Answers {
		fmt.Fprintf(&b, "ANSWER %s %s %d %s\n", rr.Name, dnsTypeName(rr.Type), rr.TTL, dnsResourceValue(rr))
	}
	for _, rr := range msg.Authorities {
		fmt.Fprintf(&b, "AUTHORITY %s %s %d %s\n", rr.Name, dnsTypeName(rr.Type), rr.TTL, dnsResourceValue(rr))
	}
	for _, rr := range msg.Additionals {
		fmt.Fprintf(&b, "ADDITIONAL %s %s %d %s\n", rr.Name, dnsTypeName(rr.Type), rr.TTL, dnsResourceValue(rr))
	}
	return strings.TrimSpace(b.String())
}

func dnsResourceValue(rr dns.Resource) string {
	switch rr.Type {
	case dns.TypeA:
		if len(rr.Data) == net.IPv4len {
			return net.IPv4(rr.Data[0], rr.Data[1], rr.Data[2], rr.Data[3]).String()
		}
	case dns.TypeAAAA:
		if len(rr.Data) == net.IPv6len {
			return net.IP(append([]byte(nil), rr.Data...)).String()
		}
	case dns.TypeCNAME, dns.TypeNS, dns.TypePTR, dns.TypeTXT:
		return string(rr.Data)
	case dns.TypeMX:
		if len(rr.Data) >= 2 {
			return strconv.Itoa(int(binary.BigEndian.Uint16(rr.Data[:2]))) + " " + string(rr.Data[2:])
		}
	case dns.TypeSRV:
		if len(rr.Data) >= 6 {
			return strconv.Itoa(int(binary.BigEndian.Uint16(rr.Data[0:2]))) + " " +
				strconv.Itoa(int(binary.BigEndian.Uint16(rr.Data[2:4]))) + " " +
				strconv.Itoa(int(binary.BigEndian.Uint16(rr.Data[4:6]))) + " " +
				string(rr.Data[6:])
		}
	}
	return "0x" + hex.EncodeToString(rr.Data)
}

func dnsTypeName(typ uint16) string {
	switch typ {
	case dns.TypeA:
		return "A"
	case dns.TypeAAAA:
		return "AAAA"
	case dns.TypeCNAME:
		return "CNAME"
	case dns.TypeNS:
		return "NS"
	case dns.TypePTR:
		return "PTR"
	case dns.TypeMX:
		return "MX"
	case dns.TypeTXT:
		return "TXT"
	case dns.TypeSRV:
		return "SRV"
	default:
		return strconv.Itoa(int(typ))
	}
}

func dnsRCodeName(rcode uint8) string {
	switch rcode {
	case dns.RCodeSuccess:
		return "NOERROR"
	case dns.RCodeFormatError:
		return "FORMERR"
	case dns.RCodeServerFailure:
		return "SERVFAIL"
	case dns.RCodeNameError:
		return "NXDOMAIN"
	case dns.RCodeNotImplemented:
		return "NOTIMP"
	default:
		return "RCODE " + strconv.Itoa(int(rcode))
	}
}
