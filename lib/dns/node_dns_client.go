package dnsnodes

import (
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeDNSClient is the node class name for a DNS transport
	// client backed by gonnect/dns.Client.
	NodeTypeDNSClient = "almagest/DnsClient"

	DNSClientConfigBlockID = "config"
	DNSClientServersField  = "servers"
	DNSClientServerField   = "url"
	// DNSClientRequestTimeoutMSField is the Formular and saved-config key for
	// the DNS client request timeout in milliseconds.
	DNSClientRequestTimeoutMSField = "requestTimeoutMS"
)

//go:embed node_dns_client.md
var dnsClientClassLongDescription string

// DNSClientFactory creates the transport client owned by a DNS client node.
// It is retained for classes that do not need client options. Use
// DNSClientOptionsFactory when a custom factory must receive the menu timeout.
type DNSClientFactory func(
	dial gonnect.Dial,
	bootstrap dns.Interface,
	spawner gonnect.Spawner,
	tlsConfig *tls.Config,
	servers ...string,
) dns.Interface

// DNSClientOptionsFactory creates a DNS transport client with request options.
// DNSClientClass prefers this factory when both factory fields are set.
type DNSClientOptionsFactory func(
	dial gonnect.Dial,
	bootstrap dns.Interface,
	spawner gonnect.Spawner,
	options dns.ClientOptions,
	tlsConfig *tls.Config,
	servers ...string,
) dns.Interface

func defaultDNSClientFactory(
	dial gonnect.Dial,
	bootstrap dns.Interface,
	spawner gonnect.Spawner,
	options dns.ClientOptions,
	tlsConfig *tls.Config,
	servers ...string,
) dns.Interface {
	client := dns.NewClientWithOptions(dial, bootstrap, spawner, options, servers...)
	client.TLSConfig = nodekit.CloneTLSClientConfig(tlsConfig)
	return client
}

// DNSClientClass builds DNS transport client nodes.
type DNSClientClass struct {
	Runtime nodekit.Runtime
	// NewClient is the legacy factory hook. A class that sets it does not
	// receive DNS client options. NewClientWithOptions takes precedence.
	NewClient DNSClientFactory
	// NewClientWithOptions receives all gonnect DNS client options.
	NewClientWithOptions DNSClientOptionsFactory
}

func (DNSClientClass) ClassName() string { return NodeTypeDNSClient }
func (DNSClientClass) ShortDescription() string {
	return "DNS client"
}
func (DNSClientClass) LongDescription() string {
	return dnsClientClassLongDescription
}
func (DNSClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		nodekit.NetworkPort("right", "Network"),
		DNSPort("right", "Bootstrap DNS"),
	}}
}
func (c DNSClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultDNSClientConfig()
	config.read(cfg)
	newClient := c.NewClientWithOptions
	if newClient == nil && c.NewClient != nil {
		legacyFactory := c.NewClient
		newClient = func(dial gonnect.Dial, bootstrap dns.Interface, spawner gonnect.Spawner, _ dns.ClientOptions, tlsConfig *tls.Config, servers ...string) dns.Interface {
			return legacyFactory(dial, bootstrap, spawner, tlsConfig, servers...)
		}
	}
	if newClient == nil {
		newClient = defaultDNSClientFactory
	}
	return &dnsClientNode{Base: nodekit.Base{Runtime: c.Runtime}, config: config, newClient: newClient}, nil
}

type dnsClientConfig struct {
	servers          []string
	tlsConfigName    string
	requestTimeoutMS int
}

func defaultDNSClientConfig() dnsClientConfig {
	return dnsClientConfig{}
}

func (c *dnsClientConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	if servers, ok := readDNSClientServersConfig(cfg); ok {
		c.servers = servers
	}
	c.tlsConfigName = nodekit.ReadTLSConfigName(cfg)
	c.requestTimeoutMS = configer.Int(cfg, configer.Path{DNSClientRequestTimeoutMSField}, 0)
	c.normalize()
}

func (c *dnsClientConfig) normalize() {
	c.servers = NormalizeDNSClientServers(c.servers)
	c.tlsConfigName = nodekit.NormalizeTLSConfigName(c.tlsConfigName)
}

func (c dnsClientConfig) valid() error {
	if err := validateDNSTimeoutMilliseconds(c.requestTimeoutMS); err != nil {
		return err
	}
	for i, server := range c.servers {
		if err := validateDNSClientServerURL(server); err != nil {
			return fmt.Errorf("server %d: %w", i+1, err)
		}
	}
	return nil
}

func (c dnsClientConfig) copy() dnsClientConfig {
	return dnsClientConfig{
		servers:          slices.Clone(c.servers),
		tlsConfigName:    c.tlsConfigName,
		requestTimeoutMS: c.requestTimeoutMS,
	}
}

// dnsClientNode owns one gonnect/dns.Client for the current Network,
// optional bootstrap DNS, and normalized server list. Every material input or
// config change closes emitted wrappers and the old client before publishing
// detached wrappers around the new client to existing consumers.
type dnsClientNode struct {
	nodekit.Base

	out       uint64
	networkIn uint64
	bootstrap uint64

	networkLink   uint64
	bootstrapLink uint64

	config    dnsClientConfig
	newClient DNSClientOptionsFactory

	network      nodekit.NetworkCloser
	bootstrapDNS dns.Interface
	client       dns.Interface
	outputs      DNSOutputCache

	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string

	formError string
}

func (n *dnsClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = nodekit.RestoredLeft(restored, 0)
	n.networkIn = nodekit.RestoredRight(restored, 0)
	n.bootstrap = nodekit.RestoredRight(restored, 1)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	n.refreshTLSConfig()
	if err := n.config.valid(); err != nil {
		n.formError = err.Error()
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *dnsClientNode) OnReady() error {
	n.RequestPort(n.networkIn)
	n.RequestPort(n.bootstrap)
	n.sendAll()
	return nil
}

func (n *dnsClientNode) OnStop() {
	n.outputs.CloseAll()
	CloseDNS(n.client)
	n.client = nil
	CloseDNS(n.bootstrapDNS)
	n.bootstrapDNS = nil
	_ = nodekit.CloseNetwork(n.network)
	n.network = nil
	n.networkLink = 0
	n.bootstrapLink = 0
}

func (n *dnsClientNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
	case n.networkIn:
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	case n.bootstrap:
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

func (n *dnsClientNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.sendToLink(link)
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork:
		n.networkLink = link
		std.RequestLocked(n.W, n.ID, link)
	case port == n.bootstrap && portDirection == "right" && linkType == TypeDNS:
		n.bootstrapLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *dnsClientNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.networkLink:
		n.outputs.CloseAll()
		CloseDNS(n.client)
		n.client = nil
		_ = nodekit.CloseNetwork(n.network)
		n.network = nil
		n.networkLink = 0
	case port == n.bootstrap && portDirection == "right" && linkType == TypeDNS && link == n.bootstrapLink:
		CloseDNS(n.bootstrapDNS)
		n.bootstrapDNS = nil
		n.bootstrapLink = 0
		n.rebuildClient()
	}
	return n.updateLabel()
}

func (n *dnsClientNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeDNS && std.IsRequest(event.Payload) {
		n.refreshLink(event.Link)
		return nil
	}
	if event.ReceiverPort == n.networkIn && receiverPortDirection == "right" && linkType == nodekit.TypeNetwork {
		if event.Link != n.networkLink {
			return nil
		}
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if !changed {
			return nil
		}
		n.network = next
		n.rebuildClient()
		return n.updateLabel()
	}
	if event.ReceiverPort == n.bootstrap && receiverPortDirection == "right" && linkType == TypeDNS {
		if event.Link != n.bootstrapLink {
			return nil
		}
		next, changed := AcceptDNSPayload(n.W, n.ID, event, n.bootstrapDNS)
		if !changed {
			return nil
		}
		n.bootstrapDNS = next
		n.rebuildClient()
		return n.updateLabel()
	}
	return nil
}

func (n *dnsClientNode) OnInbox(message pasta.InboxMessage) error {
	if msg, ok := message.Payload.(nodekit.TlsConfigChanged); ok {
		n.handleTLSConfigChanged(msg)
	}
	return nil
}

func (n *dnsClientNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != DNSClientConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != DNSClientConfigBlockID {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *dnsClientNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"servers":                      slices.Clone(n.config.servers),
		"tlsConfig":                    n.config.tlsConfigName,
		DNSClientRequestTimeoutMSField: n.config.requestTimeoutMS,
	})
}

func (n *dnsClientNode) applyConfigForm(values map[string]any) error {
	next := n.config.copy()
	if value, ok := dnsClientServersFromAny(values[DNSClientServersField]); ok {
		next.servers = value
	}
	if value, ok := nodekit.TLSConfigValueFromAny(values[nodekit.TLSConfigFieldID]); ok {
		next.tlsConfigName = value
	}
	if value, ok := formular.IntValue(values[DNSClientRequestTimeoutMSField]); ok {
		next.requestTimeoutMS = value
	}
	next.normalize()
	if err := next.valid(); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	changed := !slices.Equal(n.config.servers, next.servers) ||
		n.config.tlsConfigName != next.tlsConfigName ||
		n.config.requestTimeoutMS != next.requestTimeoutMS ||
		n.formError != ""
	n.formError = ""
	n.config = next
	n.refreshTLSConfig()
	n.sendForcedMenuSnapshot()
	if changed {
		n.rebuildClient()
	}
	return n.updateLabel()
}

func (n *dnsClientNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	var err error
	if msg.Field.FieldID == DNSClientRequestTimeoutMSField && len(msg.Field.ElementPath) == 0 {
		err = validateDNSTimeoutField(msg.Value)
	} else {
		err = validateDNSClientServerField(msg)
	}
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *dnsClientNode) rebuildClient() {
	n.outputs.CloseAll()
	CloseDNS(n.client)
	n.client = nil
	if n.formError != "" || n.tlsConfigErr != "" || len(n.config.servers) == 0 || !nodekit.NetworkIsReady(n.network) {
		return
	}
	client := n.newClient(
		n.network.Dial,
		n.bootstrapDNS,
		n.dnsSpawner(),
		dns.ClientOptions{RequestTimeout: dnsTimeoutDuration(n.config.requestTimeoutMS)},
		n.currentTLSConfig(),
		n.config.servers...,
	)
	n.client = client
	BindDNSResource(n.W, n.ID, 0, client)
	n.sendAll()
}

func (n *dnsClientNode) handleTLSConfigChanged(msg nodekit.TlsConfigChanged) {
	nextName, relevant := nodekit.HandleTLSConfigChanged(n.config.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.sendForcedMenuSnapshot()
		return
	}
	n.config.tlsConfigName = nextName
	n.refreshTLSConfig()
	n.sendForcedMenuSnapshot()
	n.rebuildClient()
	_ = n.updateLabel()
}

func (n *dnsClientNode) refreshTLSConfig() {
	result, err := nodekit.ResolveTLSClientConfig(&n.Base, n.config.tlsConfigName)
	n.tlsConfig = result.Config
	n.tlsConfigEffective = result.EffectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
}

func (n *dnsClientNode) currentTLSConfig() *tls.Config {
	return nodekit.CloneTLSClientConfig(n.tlsConfig)
}

func (n *dnsClientNode) dnsSpawner() gonnect.Spawner {
	if n.WorkerSpawner == nil {
		return nil
	}
	return n.WorkerSpawner
}

func (n *dnsClientNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.client)
}

func (n *dnsClientNode) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.client)
}

func (n *dnsClientNode) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.client)
}

func (n *dnsClientNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case n.tlsConfigErr != "":
		return n.SetLabel("invalid TLS config")
	case !nodekit.NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case len(n.config.servers) == 0:
		return n.SetLabel("no DNS servers")
	case len(n.config.servers) == 1:
		return n.SetLabel("1 DNS server")
	default:
		return n.SetLabel(fmt.Sprintf("%d DNS servers", len(n.config.servers)))
	}
}

func (n *dnsClientNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *dnsClientNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *dnsClientNode) configBlock() formular.Block {
	status := []formular.FieldOption{formular.Validation}
	if n.formError != "" {
		status = append(status, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return formular.Block{ID: DNSClientConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		dnsClientServersFieldItem(DNSClientServersField, "DNS servers", n.config.servers, status...),
		nodekit.TLSConfigSelectorItem(&n.Base, n.config.tlsConfigName, n.tlsConfigErr),
		dnsTimeoutField(
			DNSClientRequestTimeoutMSField,
			"Request timeout (ms)",
			n.config.requestTimeoutMS,
			"Maximum time for one request, including bootstrap lookup, dial, TLS handshake, and network I/O.",
			n.formError,
		),
	}}
}

func dnsClientServersFieldItem(id, label string, values []string, status ...formular.FieldOption) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{dnsClientServerTemplate(status...)},
		Elements:  dnsClientServerElements(values, status...),
	}}
}

func dnsClientServerTemplate(status ...formular.FieldOption) formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "server",
		Label: "Server",
		Items: []formular.Item{
			formular.TextField(DNSClientServerField, "URL", "", append(status, formular.Placeholder("udp://127.0.0.1:53"))...),
		},
	}
}

func dnsClientServerElements(values []string, status ...formular.FieldOption) []formular.ArrayElement {
	template := dnsClientServerTemplate(status...)
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, fmt.Sprintf("server-%d", i), map[string]any{DNSClientServerField: value}))
	}
	return out
}

func readDNSClientServersConfig(cfg configer.Config) ([]string, bool) {
	if values, ok := configer.SliceAt(cfg, configer.Path{"servers"}); ok {
		servers := make([]string, 0, len(values))
		for _, raw := range values {
			value, ok := configer.StringValue(raw)
			if !ok {
				continue
			}
			servers = append(servers, value)
		}
		return NormalizeDNSClientServers(servers), true
	}
	if value, ok := configer.StringAt(cfg, configer.Path{"servers"}); ok {
		return splitDNSClientServersText(value), true
	}
	return nil, false
}

func dnsClientServersFromAny(raw any) ([]string, bool) {
	if value, ok := formular.StringValue(raw); ok {
		return splitDNSClientServersText(value), true
	}
	values, ok := formular.ArrayElementValuesFromAny(raw)
	if !ok {
		return nil, false
	}
	servers := make([]string, 0, len(values))
	for _, value := range values {
		server, ok := formular.StringValue(value.Values[DNSClientServerField])
		if !ok {
			continue
		}
		servers = append(servers, server)
	}
	return NormalizeDNSClientServers(servers), true
}

func validateDNSClientServerField(msg formular.FieldValidateMessage) error {
	if msg.Field.FieldID == DNSClientServersField {
		if servers, ok := dnsClientServersFromAny(msg.Value); ok {
			config := dnsClientConfig{servers: servers}
			config.normalize()
			return config.valid()
		}
		return nil
	}
	if msg.Field.FieldID != DNSClientServerField {
		return nil
	}
	value, ok := formular.StringValue(msg.Value)
	if !ok {
		return nil
	}
	value = normalizeDNSClientServer(value)
	if value == "" {
		return nil
	}
	return validateDNSClientServerURL(value)
}

func splitDNSClientServersText(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	return NormalizeDNSClientServers(fields)
}

// NormalizeDNSClientServers normalizes and removes empty DNS server URLs.
func NormalizeDNSClientServers(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeDNSClientServer(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeDNSClientServer(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "udp://" + value
	}
	return value
}

func validateDNSClientServerURL(server string) error {
	if strings.TrimSpace(server) == "" {
		return errors.New("empty server URL")
	}
	u, err := url.Parse(server)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "udp", "tcp", "dot":
	default:
		return fmt.Errorf("unsupported DNS server scheme %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("server URL must not include user info")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("server URL must not include query or fragment")
	}
	addr := u.Host
	if addr == "" {
		addr = u.Path
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("empty server address")
	}
	if strings.ContainsAny(addr, " \t\r\n") {
		return errors.New("server address must not contain whitespace")
	}
	return nil
}
