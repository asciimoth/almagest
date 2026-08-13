package almagest

import (
	_ "embed"
	"fmt"
	"net"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(*engine) pasta.NodeClass {
		return remapperClass{}
	}, false)
}

const (
	// NodeTypeRemapper is the node class name for network address remapping
	// middleware.
	NodeTypeRemapper = "almagest/Remapper"

	remapperConfigBlockID = "config"
	remapperRulesField    = "rules"
)

const remapperRulesPlaceholder = `# endpoint field replacement [MATCH pattern] [OP operation[,operation...]]
DST PORT 8080 MATCH example.test:80
SRC ADDR 127.0.0.1 OP Listen,ListenTCP
DST ADDRPORT 127.0.0.1 8080`

var (
	//go:embed node_remapper.md
	remapperClassLongDescription string
)

type remapperClass struct{}

func (remapperClass) ClassName() string        { return NodeTypeRemapper }
func (remapperClass) ShortDescription() string { return "Network remapper" }
func (remapperClass) LongDescription() string  { return remapperClassLongDescription }
func (remapperClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "Network"),
	}}
}
func (remapperClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &remapperNode{
		rulesText: configer.String(cfg, configer.Path{remapperRulesField}, ""),
	}, nil
}

// remapperNode is transparent network middleware. Its right-directed Network
// port accepts one upstream network, and its left-directed Network port emits
// per-link detached gonnect.Remapper wrappers around that upstream network.
// The node keeps wrappers per output link so std.Request refreshes can replace
// a closed routed payload without rebuilding unrelated consumers.
type remapperNode struct {
	NodeBase

	out uint64
	in  uint64

	network   NetworkCloser
	networkID uint64
	rulesText string
	rules     []gonnect.RemapRule
	formError string
	outputs   NetworkOutputCache
}

func (n *remapperNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.in = restoredRight(restored, 0)
	rules, err := parseRemapperRules(n.rulesText)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.rules = rules
	}
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return n.updateLabel()
}

func (n *remapperNode) OnReady() error {
	n.RequestPort(n.in)
	n.sendAll()
	return nil
}

func (n *remapperNode) OnStop() {
	n.outputs.CloseAll()
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkID = 0
}

func (n *remapperNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
		if portDirection != "right" || linkType != TypeNetwork {
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

func (n *remapperNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendOutputToLink(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		n.networkID = link
		std.RequestLocked(n.w, n.id, link)
	}
	return n.updateLabel()
}

func (n *remapperNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.outputs.Close(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork && link == n.networkID:
		n.outputs.CloseAll()
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkID = 0
	}
	return n.updateLabel()
}

func (n *remapperNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshOutputToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork || event.Link != n.networkID {
		return nil
	}
	next, changed := AcceptNetworkPayload(n.w, n.id, event, n.network)
	if !changed {
		return nil
	}
	n.outputs.CloseAll()
	n.network = next
	n.sendAll()
	return n.updateLabel()
}

func (n *remapperNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != remapperConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != remapperConfigBlockID || msg.Field.FieldID != remapperRulesField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendRulesFieldStatus(msg)
	}
	return nil
}

func (n *remapperNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{remapperRulesField}, n.rulesText)
}

func (n *remapperNode) applyConfigForm(values map[string]any) error {
	text, _ := formular.StringValue(values[remapperRulesField])
	rules, err := parseRemapperRules(text)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return n.updateLabel()
	}
	n.rulesText = text
	n.rules = rules
	n.formError = ""
	n.outputs.CloseAll()
	n.sendForcedMenuSnapshot()
	n.sendAll()
	return n.updateLabel()
}

func (n *remapperNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	_, err := parseRemapperRules(text)
	sendNodeFieldStatus(n.w, n.id, msg.Field, err)
}

func (n *remapperNode) ready() bool {
	return n.formError == "" && NetworkIsReady(n.network)
}

func (n *remapperNode) sendAll() {
	if !n.ready() {
		return
	}
	rules := append([]gonnect.RemapRule(nil), n.rules...)
	n.outputs.SendWrappedPortLocked(n.w, n.id, n.out, n.network, func(output NetworkCloser) NetworkCloser {
		return gonnect.NewRemapper(output, rules)
	})
}

func (n *remapperNode) sendOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	rules := append([]gonnect.RemapRule(nil), n.rules...)
	n.outputs.SendWrappedLocked(n.w, n.id, link, n.network, func(output NetworkCloser) NetworkCloser {
		return gonnect.NewRemapper(output, rules)
	})
}

func (n *remapperNode) refreshOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	rules := append([]gonnect.RemapRule(nil), n.rules...)
	n.outputs.RefreshWrappedLocked(n.w, n.id, link, n.network, func(output NetworkCloser) NetworkCloser {
		return gonnect.NewRemapper(output, rules)
	})
}

func (n *remapperNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case !NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case len(n.rules) == 0:
		return n.SetLabel("remapping network")
	case len(n.rules) == 1:
		return n.SetLabel("remapping 1 rule")
	default:
		return n.SetLabel(fmt.Sprintf("remapping %d rules", len(n.rules)))
	}
}

func (n *remapperNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock())
}

func (n *remapperNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.configBlock())
}

func (n *remapperNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.configBlock())
}

func (n *remapperNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: remapperRulesPlaceholder,
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: remapperConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: remapperRulesField, Label: "Remap rules", Field: field},
	}}
}

// parseRemapperRules parses a line-oriented rule language into gonnect remap
// rules. Each non-empty, non-comment line has this shape:
//
//	ENDPOINT FIELD REPLACEMENT [MATCH pattern] [OP operation[,operation...]]
//
// ENDPOINT is SRC or DST. FIELD is ADDR, PORT, or ADDRPORT. ADDRPORT accepts
// either "host port" or a single host:port token. MATCH uses gonnect's
// NO_PROXY-style FilterFromString against the selected endpoint. OP limits the
// rule to one or more gonnect.RemapOperation names, such as Dial, Listen,
// DialTCP, ListenTCP, DialUDP, or ListenUDP.
func parseRemapperRules(text string) ([]gonnect.RemapRule, error) {
	var rules []gonnect.RemapRule
	for lineNo, raw := range strings.Split(text, "\n") {
		line := stripRemapperRuleComment(raw)
		if line == "" {
			continue
		}
		rule, err := parseRemapperRuleLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func stripRemapperRuleComment(line string) string {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSpace(line)
}

func parseRemapperRuleLine(line string) (gonnect.RemapRule, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return gonnect.RemapRule{}, fmt.Errorf("expected ENDPOINT FIELD REPLACEMENT")
	}
	endpoint, err := parseRemapperEndpoint(fields[0])
	if err != nil {
		return gonnect.RemapRule{}, err
	}
	field, err := parseRemapperField(fields[1])
	if err != nil {
		return gonnect.RemapRule{}, err
	}
	rule := gonnect.RemapRule{Endpoint: endpoint, Field: field}
	idx, err := parseRemapperReplacement(&rule, fields, 2)
	if err != nil {
		return gonnect.RemapRule{}, err
	}
	ops := map[gonnect.RemapOperation]struct{}{}
	var addressFilter gonnect.RemapFilter
	for idx < len(fields) {
		key := strings.ToUpper(fields[idx])
		idx++
		if idx >= len(fields) {
			return gonnect.RemapRule{}, fmt.Errorf("%s missing value", key)
		}
		value := fields[idx]
		idx++
		switch key {
		case "MATCH", "FILTER":
			filter := gonnect.FilterFromString(value)
			addressFilter = gonnect.RemapAddressFilter(filter.Filter)
		case "OP", "OPS", "OPERATION", "OPERATIONS":
			parsed, err := parseRemapperOperations(value)
			if err != nil {
				return gonnect.RemapRule{}, err
			}
			ops = parsed
		default:
			return gonnect.RemapRule{}, fmt.Errorf("unknown option %q", key)
		}
	}
	rule.Filter = combineRemapperFilters(addressFilter, ops)
	return rule, nil
}

func parseRemapperEndpoint(value string) (gonnect.RemapEndpoint, error) {
	switch strings.ToUpper(value) {
	case "DST", "DEST", "DESTINATION":
		return gonnect.RemapDst, nil
	case "SRC", "SOURCE":
		return gonnect.RemapSrc, nil
	default:
		return 0, fmt.Errorf("unknown endpoint %q", value)
	}
}

func parseRemapperField(value string) (gonnect.RemapField, error) {
	switch strings.ToUpper(value) {
	case "ADDR", "ADDRESS", "HOST":
		return gonnect.RemapAddr, nil
	case "PORT":
		return gonnect.RemapPort, nil
	case "ADDRPORT", "ADDRESSPORT", "ENDPOINT":
		return gonnect.RemapAddrPort, nil
	default:
		return 0, fmt.Errorf("unknown field %q", value)
	}
}

func parseRemapperReplacement(rule *gonnect.RemapRule, fields []string, idx int) (int, error) {
	switch rule.Field {
	case gonnect.RemapAddr:
		rule.Addr = fields[idx]
		return idx + 1, nil
	case gonnect.RemapPort:
		rule.Port = fields[idx]
		return idx + 1, nil
	case gonnect.RemapAddrPort:
		if idx+1 < len(fields) && !isRemapperOption(fields[idx+1]) {
			rule.Addr = fields[idx]
			rule.Port = fields[idx+1]
			return idx + 2, nil
		}
		host, port, err := net.SplitHostPort(fields[idx])
		if err != nil {
			return 0, fmt.Errorf("ADDRPORT replacement must be host port or host:port")
		}
		rule.Addr = host
		rule.Port = port
		return idx + 1, nil
	default:
		return 0, fmt.Errorf("unknown remap field")
	}
}

func isRemapperOption(value string) bool {
	switch strings.ToUpper(value) {
	case "MATCH", "FILTER", "OP", "OPS", "OPERATION", "OPERATIONS":
		return true
	default:
		return false
	}
}

func parseRemapperOperations(value string) (map[gonnect.RemapOperation]struct{}, error) {
	out := map[gonnect.RemapOperation]struct{}{}
	for _, raw := range strings.Split(value, ",") {
		op, err := parseRemapperOperation(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		out[op] = struct{}{}
	}
	return out, nil
}

func parseRemapperOperation(value string) (gonnect.RemapOperation, error) {
	switch strings.ToLower(value) {
	case strings.ToLower(string(gonnect.RemapOpDial)):
		return gonnect.RemapOpDial, nil
	case strings.ToLower(string(gonnect.RemapOpListen)):
		return gonnect.RemapOpListen, nil
	case strings.ToLower(string(gonnect.RemapOpPacketDial)):
		return gonnect.RemapOpPacketDial, nil
	case strings.ToLower(string(gonnect.RemapOpListenPacket)):
		return gonnect.RemapOpListenPacket, nil
	case strings.ToLower(string(gonnect.RemapOpDialTCP)):
		return gonnect.RemapOpDialTCP, nil
	case strings.ToLower(string(gonnect.RemapOpListenTCP)):
		return gonnect.RemapOpListenTCP, nil
	case strings.ToLower(string(gonnect.RemapOpDialUDP)):
		return gonnect.RemapOpDialUDP, nil
	case strings.ToLower(string(gonnect.RemapOpListenUDP)):
		return gonnect.RemapOpListenUDP, nil
	case strings.ToLower(string(gonnect.RemapOpListenPacketConfig)):
		return gonnect.RemapOpListenPacketConfig, nil
	case strings.ToLower(string(gonnect.RemapOpListenUDPConfig)):
		return gonnect.RemapOpListenUDPConfig, nil
	case strings.ToLower(string(gonnect.RemapOpListenMulticastUDP)):
		return gonnect.RemapOpListenMulticastUDP, nil
	default:
		return "", fmt.Errorf("unknown operation %q", value)
	}
}

func combineRemapperFilters(addressFilter gonnect.RemapFilter, ops map[gonnect.RemapOperation]struct{}) gonnect.RemapFilter {
	if addressFilter == nil && len(ops) == 0 {
		return nil
	}
	return func(info gonnect.RemapInfo) bool {
		if len(ops) > 0 {
			if _, ok := ops[info.Operation]; !ok {
				return false
			}
		}
		return addressFilter == nil || addressFilter(info)
	}
}
