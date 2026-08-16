package dnse2etest

import (
	"strings"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta/std"
)

// E2EWorkspace is the default workspace used by root and moved e2e tests.
const E2EWorkspace = "main"

// DNSBase is the NodeE2E surface required by DNS helpers.
type DNSBase interface {
	TestingT() *testing.T
	SendNodeFormularMsg(workspace string, node uint64, msg any)
	MenuItem(node uint64, blockID, itemID string) (formular.Item, bool)
	AssertNodeLabel(node uint64, want string)
}

// DNSOps provides static DNS and DNS query e2e helpers.
type DNSOps struct {
	DNSBase
}

// NewDNSOps returns DNS helper methods for base.
func NewDNSOps(base DNSBase) *DNSOps {
	return &DNSOps{DNSBase: base}
}

func (e *DNSOps) t() *testing.T {
	return e.TestingT()
}

// TestQuery builds a DNS query message for tests.
func TestQuery(name string, typ uint16) *dns.Message {
	return &dns.Message{
		ID:               dns.NextID(),
		Opcode:           dns.OpcodeQuery,
		RecursionDesired: true,
		Questions: []dns.Question{{
			Name:  name,
			Type:  typ,
			Class: dns.ClassIN,
		}},
	}
}

// ApplyStaticRecords applies StaticDNS records and waits for normalization.
func (e *DNSOps) ApplyStaticRecords(node uint64, records string) {
	e.t().Helper()
	e.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"records": records,
			"ttl":     dnsnodes.DefaultStaticDNSTTL,
		},
	})
	e.WaitStaticField(node, "records", dnsnodes.NormalizeStaticRecordsText(records))
}

// WaitStaticField waits for a StaticDNS config field value.
func (e *DNSOps) WaitStaticField(node uint64, field, want string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		item, ok := e.MenuItem(node, "config", field)
		return ok && item.Field != nil && item.Value == want
	}, "StaticDns field %s = %q", field, want)
}

// SetQueryRequest sets the DNS query name and type.
func (e *DNSOps) SetQueryRequest(client uint64, name, qtype string) {
	e.t().Helper()
	e.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "name"},
		Value:       name,
	})
	e.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "type"},
		Value:       qtype,
	})
	e.WaitQueryRequestField(client, "name", dnsnodes.AbsoluteName(name))
	e.WaitQueryRequestField(client, "type", strings.ToUpper(qtype))
}

// WaitQueryRequestField waits for a DNS query request field.
func (e *DNSOps) WaitQueryRequestField(client uint64, field, want string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		item, ok := e.MenuItem(client, "request", field)
		return ok && item.Field != nil && item.Value == want
	}, "DnsQuery field %s = %q", field, want)
}

// SendRequest presses the DNS query send button.
func (e *DNSOps) SendRequest(client uint64) {
	e.t().Helper()
	e.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "request",
		ButtonID:    "send",
	})
}

// WaitResponse waits for a DNS query response status and content.
func (e *DNSOps) WaitResponse(client uint64, status string, wantParts ...string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		if e.QueryStatus(client) != "Status: "+status || e.QueryResultField(client, "error") != "" {
			return false
		}
		resp := e.QueryResultField(client, "response")
		for _, want := range wantParts {
			if !strings.Contains(resp, want) {
				return false
			}
		}
		return true
	}, "DNS response %s containing %v", status, wantParts)
	e.AssertNodeLabel(client, status)
}

// WaitFailure waits for a DNS query failure containing text.
func (e *DNSOps) WaitFailure(client uint64, wantErrPart string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.QueryStatus(client) == "Status: fail" &&
			strings.Contains(e.QueryResultField(client, "error"), wantErrPart)
	}, "DNS failure containing %q", wantErrPart)
	e.AssertNodeLabel(client, "fail")
}

// WaitReady waits for a DNS query node to be ready with empty result fields.
func (e *DNSOps) WaitReady(client uint64) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.QueryStatus(client) == "Status: ready" &&
			e.QueryResultField(client, "error") == "" &&
			e.QueryResultField(client, "response") == ""
	}, "DNS query ready")
}

// QueryStatus returns the DNS query status text.
func (e *DNSOps) QueryStatus(client uint64) string {
	item, ok := e.MenuItem(client, "result", "status")
	if !ok {
		return ""
	}
	return item.Text
}

// QueryResultField returns a string DNS query result field.
func (e *DNSOps) QueryResultField(client uint64, field string) string {
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := std.StringFromPayload(item.Value)
	return value
}
