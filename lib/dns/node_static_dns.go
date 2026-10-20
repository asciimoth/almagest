package dnsnodes

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeStaticDNS is the node class name for a static DNS provider.
	NodeTypeStaticDNS = "almagest/StaticDns"

	DefaultStaticDNSTTL = 60
)

// StaticDNSClass builds static DNS provider nodes.
type StaticDNSClass struct {
	Runtime nodekit.Runtime
}

func (StaticDNSClass) ClassName() string        { return NodeTypeStaticDNS }
func (StaticDNSClass) ShortDescription() string { return "Static DNS" }
func (StaticDNSClass) LongDescription() string {
	return "Serves static DNS records with optional fallback."
}
func (StaticDNSClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		DNSPort("right", "Fallback"),
	}}
}
func (c StaticDNSClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultStaticDNSConfig()
	config.read(cfg)
	return &staticDNSNode{Base: nodekit.Base{Runtime: c.Runtime}, config: config}, nil
}

type staticDNSConfig struct {
	recordsText string
	ttl         int
}

func defaultStaticDNSConfig() staticDNSConfig {
	return staticDNSConfig{ttl: DefaultStaticDNSTTL}
}

func (c *staticDNSConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.recordsText = configer.String(cfg, configer.Path{"records"}, c.recordsText)
	c.ttl = configer.Int(cfg, configer.Path{"ttl"}, c.ttl)
	c.normalize()
}

func (c *staticDNSConfig) normalize() {
	c.recordsText = NormalizeStaticRecordsText(c.recordsText)
	if c.ttl < 0 {
		c.ttl = DefaultStaticDNSTTL
	}
}

type staticDNSNode struct {
	nodekit.Base

	out      uint64
	fallback uint64

	config   staticDNSConfig
	records  staticDNSRecords
	resolver *staticDNSProvider

	fallbackDNS dns.Interface
	outputs     DNSOutputCache
	formError   string
}

func (n *staticDNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = nodekit.RestoredLeft(restored, 0)
	n.fallback = nodekit.RestoredRight(restored, 0)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	records, err := parseStaticDNSRecords(n.config.recordsText, n.config.ttl)
	if err != nil {
		n.formError = err.Error()
		records = staticDNSRecords{}
	}
	n.records = records
	n.resolver = newStaticDNSProvider(records)
	BindDNSResource(n.W, n.ID, 0, n.resolver)
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *staticDNSNode) OnReady() error {
	n.RequestPort(n.fallback)
	n.sendAll()
	return nil
}

func (n *staticDNSNode) OnStop() {
	n.outputs.CloseAll()
	CloseDNS(n.fallbackDNS)
	n.fallbackDNS = nil
	if n.resolver != nil {
		_ = n.resolver.Close()
		n.resolver = nil
	}
}

func (n *staticDNSNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
	case n.fallback:
		if portDirection != "right" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *staticDNSNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.sendToLink(link)
	case port == n.fallback && portDirection == "right" && linkType == TypeDNS:
		std.RequestLocked(n.W, n.ID, link)
	}
	return nil
}

func (n *staticDNSNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.fallback && portDirection == "right" && linkType == TypeDNS:
		CloseDNS(n.fallbackDNS)
		n.fallbackDNS = nil
		if n.resolver != nil {
			n.resolver.AttachFallback(nil)
		}
	}
	return nil
}

func (n *staticDNSNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeDNS && std.IsRequest(event.Payload) {
		n.refreshLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.fallback || receiverPortDirection != "right" || linkType != TypeDNS {
		return nil
	}
	next, changed := AcceptDNSPayload(n.W, n.ID, event, n.fallbackDNS)
	if !changed {
		return nil
	}
	n.fallbackDNS = next
	if n.resolver != nil {
		n.resolver.AttachFallback(next)
	}
	return nil
}

func (n *staticDNSNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *staticDNSNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"records": n.config.recordsText,
		"ttl":     n.config.ttl,
	})
}

func (n *staticDNSNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.StringValue(values["records"]); ok {
		next.recordsText = value
	}
	if value, ok := formular.IntValue(values["ttl"]); ok {
		next.ttl = value
	}
	next.normalize()
	records, err := parseStaticDNSRecords(next.recordsText, next.ttl)
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	n.formError = ""
	n.config = next
	n.records = records
	if n.resolver != nil {
		n.resolver.SetRecords(records)
	}
	n.sendForcedMenuSnapshot()
	return n.updateLabel()
}

func (n *staticDNSNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	next := n.config
	switch msg.Field.FieldID {
	case "records":
		value, _ := formular.StringValue(msg.Value)
		next.recordsText = value
	case "ttl":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value < 0 {
			nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, errors.New("ttl value must be a non-negative integer"))
			return
		}
		next.ttl = value
	default:
		return
	}
	next.normalize()
	_, err := parseStaticDNSRecords(next.recordsText, next.ttl)
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *staticDNSNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.resolver)
}

func (n *staticDNSNode) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.resolver)
}

func (n *staticDNSNode) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.resolver)
}

func (n *staticDNSNode) updateLabel() error {
	count := n.records.Count()
	if count == 1 {
		return n.SetLabel("1 DNS record")
	}
	if count > 1 {
		return n.SetLabel(strconv.Itoa(count) + " DNS records")
	}
	return n.SetLabel("static DNS")
}

func (n *staticDNSNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *staticDNSNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *staticDNSNode) configBlock() formular.Block {
	status := []formular.FieldOption{formular.Validation}
	if n.formError != "" {
		status = append(status, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		formular.MultilineTextField("records", "Records", n.config.recordsText, append(status, formular.Placeholder("example.test A 192.0.2.10\nexample.test AAAA 2001:db8::10\nexample.test TXT \"hello\""))...),
		formular.IntField("ttl", "TTL", n.config.ttl, status...),
	}}
}

type staticDNSProvider struct {
	provider *dnsProvider

	mu       sync.Mutex
	records  staticDNSRecords
	fallback dns.Interface
}

// NewStaticProvider returns a standalone provider for static DNS records.
func NewStaticProvider(recordsText string, ttl int) (dns.Interface, error) {
	records, err := parseStaticDNSRecords(recordsText, ttl)
	if err != nil {
		return nil, err
	}
	return newStaticDNSProvider(records), nil
}

func newStaticDNSProvider(records staticDNSRecords) *staticDNSProvider {
	s := &staticDNSProvider{records: records.Clone()}
	s.provider = newDNSProvider(s.handle)
	return s
}

func (s *staticDNSProvider) Requests() chan<- dns.Request { return s.provider.Requests() }
func (s *staticDNSProvider) Close() error                 { return s.provider.Close() }
func (s *staticDNSProvider) IsUp() (bool, error)          { return s.provider.IsUp() }

func (s *staticDNSProvider) SetRecords(records staticDNSRecords) {
	s.mu.Lock()
	s.records = records.Clone()
	s.mu.Unlock()
}

func (s *staticDNSProvider) AttachFallback(fallback dns.Interface) {
	s.mu.Lock()
	s.fallback = fallback
	s.mu.Unlock()
}

func (s *staticDNSProvider) handle(root context.Context, req dns.Request) {
	ctx, cancel := dnsRequestContext(root, req)
	defer cancel()

	resp, found := s.staticResponse(req.Message)
	if found {
		sendDNSResponse(req, resp, nil)
		return
	}

	fallback := s.currentFallback()
	if fallback != nil {
		msg, err := dns.Query(ctx, fallback, req.Message)
		sendDNSResponse(req, msg, err)
		return
	}

	sendDNSResponse(req, dnsNameErrorResponse(req.Message), nil)
}

func (s *staticDNSProvider) staticResponse(req *dns.Message) (*dns.Message, bool) {
	if req == nil || req.Opcode != dns.OpcodeQuery || len(req.Questions) == 0 {
		resp := staticDNSResponseFor(req)
		resp.RCode = dns.RCodeFormatError
		return resp, true
	}
	s.mu.Lock()
	records := s.records.Clone()
	s.mu.Unlock()
	return records.Response(req)
}

func (s *staticDNSProvider) currentFallback() dns.Interface {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fallback
}

type staticDNSKey struct {
	name string
	typ  uint16
}

type staticDNSRecords struct {
	entries map[staticDNSKey][]dns.Resource
}

func (r staticDNSRecords) Clone() staticDNSRecords {
	out := staticDNSRecords{entries: make(map[staticDNSKey][]dns.Resource, len(r.entries))}
	for key, values := range r.entries {
		copied := make([]dns.Resource, len(values))
		for i := range values {
			copied[i] = values[i]
			copied[i].Data = append([]byte(nil), values[i].Data...)
		}
		out.entries[key] = copied
	}
	return out
}

func (r staticDNSRecords) Count() int {
	var count int
	for _, values := range r.entries {
		count += len(values)
	}
	return count
}

func (r staticDNSRecords) Response(req *dns.Message) (*dns.Message, bool) {
	resp := staticDNSResponseFor(req)
	var answers []dns.Resource
	for _, q := range req.Questions {
		if q.Class != 0 && q.Class != dns.ClassIN {
			resp.RCode = dns.RCodeNotImplemented
			return resp, true
		}
		key := staticDNSKey{name: canonicalDNSName(q.Name), typ: q.Type}
		matched := r.entries[key]
		if len(matched) == 0 {
			return nil, false
		}
		for _, rr := range matched {
			copied := rr
			copied.Data = append([]byte(nil), rr.Data...)
			answers = append(answers, copied)
		}
	}
	resp.Answers = answers
	return resp, true
}

func parseStaticDNSRecords(text string, ttl int) (staticDNSRecords, error) {
	if ttl < 0 {
		return staticDNSRecords{}, errors.New("ttl value must be a non-negative integer")
	}
	out := staticDNSRecords{entries: map[staticDNSKey][]dns.Resource{}}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rr, err := parseStaticDNSRecordLine(line, uint32(ttl))
		if err != nil {
			return staticDNSRecords{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		key := staticDNSKey{name: canonicalDNSName(rr.Name), typ: rr.Type}
		out.entries[key] = append(out.entries[key], rr)
	}
	return out, nil
}

func parseStaticDNSRecordLine(line string, ttl uint32) (dns.Resource, error) {
	fields, err := splitStaticDNSFields(line)
	if err != nil {
		return dns.Resource{}, err
	}
	if len(fields) < 3 {
		return dns.Resource{}, errors.New("record must be NAME TYPE VALUE")
	}
	name := AbsoluteName(fields[0])
	typ, err := staticDNSType(fields[1])
	if err != nil {
		return dns.Resource{}, err
	}
	rr := dns.Resource{Name: name, Type: typ, Class: dns.ClassIN, TTL: ttl}
	switch typ {
	case dns.TypeA:
		addr, err := netip.ParseAddr(fields[2])
		if err != nil || !addr.Is4() {
			return dns.Resource{}, errors.New("a record value must be an IPv4 address")
		}
		rr.Data = append([]byte(nil), addr.AsSlice()...)
	case dns.TypeAAAA:
		addr, err := netip.ParseAddr(fields[2])
		if err != nil || !addr.Is6() || addr.Is4() {
			return dns.Resource{}, errors.New("aaaa record value must be an IPv6 address")
		}
		rr.Data = append([]byte(nil), addr.AsSlice()...)
	case dns.TypeCNAME, dns.TypeNS, dns.TypePTR:
		rr.Data = []byte(AbsoluteName(fields[2]))
	case dns.TypeTXT:
		rr.Data = []byte(strings.Join(fields[2:], " "))
	case dns.TypeMX:
		if len(fields) != 4 {
			return dns.Resource{}, errors.New("mx record value must be PREF HOST")
		}
		pref, err := parseDNSUint16(fields[2], "mx preference")
		if err != nil {
			return dns.Resource{}, err
		}
		data := make([]byte, 2, 2+len(AbsoluteName(fields[3])))
		binary.BigEndian.PutUint16(data, pref)
		rr.Data = append(data, AbsoluteName(fields[3])...)
	case dns.TypeSRV:
		if len(fields) != 6 {
			return dns.Resource{}, errors.New("srv record value must be PRIORITY WEIGHT PORT TARGET")
		}
		priority, err := parseDNSUint16(fields[2], "srv priority")
		if err != nil {
			return dns.Resource{}, err
		}
		weight, err := parseDNSUint16(fields[3], "srv weight")
		if err != nil {
			return dns.Resource{}, err
		}
		port, err := parseDNSUint16(fields[4], "srv port")
		if err != nil {
			return dns.Resource{}, err
		}
		data := make([]byte, 6, 6+len(AbsoluteName(fields[5])))
		binary.BigEndian.PutUint16(data[0:2], priority)
		binary.BigEndian.PutUint16(data[2:4], weight)
		binary.BigEndian.PutUint16(data[4:6], port)
		rr.Data = append(data, AbsoluteName(fields[5])...)
	default:
		return dns.Resource{}, fmt.Errorf("unsupported type %s", fields[1])
	}
	return rr, nil
}

func splitStaticDNSFields(line string) ([]string, error) {
	var fields []string
	var b strings.Builder
	var quote bool
	var escape bool
	for _, r := range line {
		switch {
		case escape:
			b.WriteRune(r)
			escape = false
		case quote && r == '\\':
			escape = true
		case r == '"':
			quote = !quote
		case !quote && (r == ' ' || r == '\t'):
			if b.Len() > 0 {
				fields = append(fields, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if escape {
		b.WriteRune('\\')
	}
	if quote {
		return nil, errors.New("unterminated quote")
	}
	if b.Len() > 0 {
		fields = append(fields, b.String())
	}
	return fields, nil
}

func staticDNSType(value string) (uint16, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "A":
		return dns.TypeA, nil
	case "AAAA", "AAA":
		return dns.TypeAAAA, nil
	case "CNAME":
		return dns.TypeCNAME, nil
	case "NS":
		return dns.TypeNS, nil
	case "PTR":
		return dns.TypePTR, nil
	case "MX":
		return dns.TypeMX, nil
	case "TXT":
		return dns.TypeTXT, nil
	case "SRV":
		return dns.TypeSRV, nil
	default:
		return 0, fmt.Errorf("unsupported DNS type %q", value)
	}
}

func parseDNSUint16(value, label string) (uint16, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("%s must be between 0 and 65535", label)
	}
	return uint16(n), nil
}

// NormalizeStaticRecordsText normalizes static DNS record configuration text.
func NormalizeStaticRecordsText(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func staticDNSResponseFor(req *dns.Message) *dns.Message {
	resp := &dns.Message{
		Response:      true,
		RCode:         dns.RCodeSuccess,
		Authoritative: true,
	}
	if req != nil {
		resp.ID = req.ID
		resp.Opcode = req.Opcode
		resp.RecursionDesired = req.RecursionDesired
		resp.Questions = append([]dns.Question(nil), req.Questions...)
	}
	return resp
}

func dnsNameErrorResponse(req *dns.Message) *dns.Message {
	resp := staticDNSResponseFor(req)
	resp.RCode = dns.RCodeNameError
	return resp
}

func AbsoluteName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return "."
	}
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func canonicalDNSName(name string) string {
	return strings.ToLower(AbsoluteName(name))
}
