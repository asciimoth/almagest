package socksnodes

import (
	"crypto/tls"
	"strings"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

const (
	// NodeTypeSocksClient is the node class name for a SOCKS proxy client.
	NodeTypeSocksClient = "almagest/SocksClient"

	// DefaultSocksClientProxyURL is the default SOCKS proxy URL.
	DefaultSocksClientProxyURL = "socks5://127.0.0.1:1080"

	socksClientPasswordSecretFieldID = "passwordSecret"
)

type SocksClientClass struct {
	Runtime Runtime
}

func (SocksClientClass) ClassName() string        { return NodeTypeSocksClient }
func (SocksClientClass) ShortDescription() string { return "SOCKS client" }
func (SocksClientClass) LongDescription() string {
	return "Proxies network operations through a SOCKS server."
}
func (SocksClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "out"),
		{Direction: "left", Name: "Proxy URL", Types: []string{std.TypeString}},
	}}
}
func (c SocksClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &socksClientNode{
		NodeBase:       NodeBase{Runtime: c.Runtime},
		pool:           enginePool(c.Runtime),
		proxyURL:       configer.String(cfg, configer.Path{"proxyURL"}, DefaultSocksClientProxyURL),
		passwordSecret: strings.TrimSpace(configer.String(cfg, configer.Path{socksClientPasswordSecretFieldID}, "")),
		tlsConfigName:  readNodeTLSConfigName(cfg),
	}, nil
}

type socksClientNode struct {
	NodeBase

	pool bufpool.Pool

	in  uint64
	out uint64
	url uint64

	outNetwork NetworkCloser
	client     *socksgo.Client
	outputs    NetworkOutputCache
	proxyURL   string
	// passwordSecret is persisted, but its value is read only into the
	// short-lived socksgo client built from proxyURL.
	passwordSecret string
	errText        string

	tlsConfigName      string
	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string
}

func (n *socksClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.proxyURL == "" {
		n.proxyURL = DefaultSocksClientProxyURL
	}
	n.refreshTLSConfig()

	n.in = restoredLeft(restored, 0)
	n.out = restoredRight(restored, 0)
	n.url = restoredLeft(restored, 1)

	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}

	n.rebuildClient()
	n.sendMenuSnapshot()
	return nil
}

func (n *socksClientNode) OnReady() error {
	if !NetworkIsReady(n.outNetwork) {
		n.requestOutNetwork()
	}
	n.requestProxyURL()
	n.sendAll()
	return nil
}

func (n *socksClientNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.in:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.out:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	case n.url:
		if portDirection != "left" || linkType != std.TypeString {
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

func (n *socksClientNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.in:
		n.sendNetworkToLink(link)
	case n.out:
		std.RequestLocked(n.W, n.ID, link)
	case n.url:
		n.sendConfigBlock()
		std.RequestLocked(n.W, n.ID, link)
	}
	return nil
}

func (n *socksClientNode) OnLinkRemoved(link uint64, port uint64, _, _ string) error {
	switch port {
	case n.out:
		n.outNetwork = nil
		n.rebuildClient()
		n.sendAll()
	case n.url:
		n.sendConfigBlock()
	case n.in:
		n.outputs.Close(link)
	}
	return nil
}

func (n *socksClientNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshNetworkToLink(event.Link)
		return nil
	}

	if event.ReceiverPort == n.out && receiverPortDirection == "right" && linkType == TypeNetwork {
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.outNetwork)
		if !changed {
			return nil
		}
		n.outNetwork = next
		n.rebuildClient()
		n.sendAll()
		return nil
	}

	if event.ReceiverPort == n.url && receiverPortDirection == "left" && linkType == std.TypeString {
		value, ok := std.StringFromPayload(event.Payload)
		if !ok {
			return nil
		}
		n.setProxyURL(value)
	}
	return nil
}

func (n *socksClientNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case TlsConfigChanged:
		n.handleTLSConfigChanged(msg)
	case SecretValueChanged:
		n.handlePasswordSecretChanged(msg)
	}
	return nil
}

func (n *socksClientNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FieldUpdateMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" || len(msg.Field.ElementPath) > 0 {
		return nil
	}
	switch msg.Field.FieldID {
	case "proxyURL":
		value, ok := std.StringFromPayload(msg.Value)
		if !ok {
			n.sendConfigBlock()
			return nil
		}
		if n.PortHasLinks(n.url) {
			n.sendConfigBlock()
			return nil
		}
		n.setProxyURL(value)
	case socksClientPasswordSecretFieldID:
		value, ok := formular.StringValue(msg.Value)
		if !ok {
			n.sendConfigBlock()
			return nil
		}
		n.setPasswordSecret(value)
	case nodeTLSConfigFieldID:
		value, ok := nodeTLSConfigValueFromAny(msg.Value)
		if !ok {
			n.sendConfigBlock()
			return nil
		}
		n.setTLSConfig(value)
	}
	return nil
}

func (n *socksClientNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"proxyURL":       n.proxyURL,
		"passwordSecret": n.passwordSecret,
		"tlsConfig":      n.tlsConfigName,
	})
}

func (n *socksClientNode) setProxyURL(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultSocksClientProxyURL
	}
	if n.proxyURL == value {
		n.sendConfigBlock()
		return
	}
	n.proxyURL = value
	n.rebuildClient()
	n.sendConfigBlock()
	n.sendAll()
}

func (n *socksClientNode) setTLSConfig(value string) {
	value = normalizeNodeTLSConfigName(value)
	if n.tlsConfigName == value {
		n.sendConfigBlock()
		return
	}
	n.tlsConfigName = value
	n.rebuildClient()
	n.sendConfigBlock()
	n.sendAll()
}

func (n *socksClientNode) setPasswordSecret(value string) {
	value = strings.TrimSpace(value)
	if n.passwordSecret == value {
		n.sendConfigBlock()
		return
	}
	n.passwordSecret = value
	n.rebuildClient()
	n.sendConfigBlock()
	n.sendAll()
}

func (n *socksClientNode) rebuildClient() {
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.refreshTLSConfig()
	client, err := buildSocksGoClient(n.proxyURL, n.outNetwork, n.pool, n.currentTLSConfig(), n.tlsConfigErr)
	if err != nil {
		n.client = nil
		n.errText = err.Error()
		_ = n.updateLabel()
		return
	}
	n.overwriteClientPasswordFromSecret(client)
	n.client = client
	n.errText = ""
	_ = n.updateLabel()
}

// overwriteClientPasswordFromSecret replaces only the password parsed from
// the proxy URL. The URL must still define a username so socksgo creates the
// RFC 1929 authentication method. If the selected secret is unavailable, the
// URL password remains the runtime fallback.
func (n *socksClientNode) overwriteClientPasswordFromSecret(client *socksgo.Client) {
	password, ok := socksSecretValue(&n.NodeBase, n.passwordSecret)
	if ok {
		overwriteSocksClientPassword(client, password)
	}
}

// overwriteSocksClientPassword updates the RFC 1929 method that socksgo
// created from URL user information. It returns false when the URL did not
// create username/password authentication.
func overwriteSocksClientPassword(client *socksgo.Client, password string) bool {
	if client == nil || client.Auth == nil {
		return false
	}
	auth, ok := client.Auth.Get(protocol.PassAuthCode).(*protocol.PassAuthMethod)
	if ok {
		auth.Pass = password
	}
	return ok
}

func (n *socksClientNode) handlePasswordSecretChanged(msg SecretValueChanged) {
	if !socksSecretChangeMatches(n.passwordSecret, msg) {
		return
	}
	prev := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	if n.passwordSecret == prev && next != "" && next != prev {
		n.passwordSecret = next
	}
	n.rebuildClient()
	n.sendConfigBlock()
	n.sendAll()
}

func buildSocksGoClient(proxyURL string, outNetwork NetworkCloser, pool bufpool.Pool, tlsConfig *tls.Config, tlsConfigErr string) (*socksgo.Client, error) {
	client, err := socksgo.ClientFromURL(proxyURL)
	if err != nil {
		return nil, err
	}
	if client.IsTLS() && tlsConfigErr != "" {
		return nil, errNodeTLSConfig(tlsConfigErr)
	}
	client.TLSConfig = cloneTLSClientConfig(tlsConfig)
	client.WithNetwork(outNetwork)
	client.Pool = pool
	client.Filter = gonnect.FalseFilter
	return client, nil
}

func (n *socksClientNode) handleTLSConfigChanged(msg TlsConfigChanged) {
	nextName, relevant := handleNodeTLSConfigChanged(n.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.sendConfigBlock()
		return
	}
	n.tlsConfigName = nextName
	n.rebuildClient()
	n.sendConfigBlock()
	n.sendAll()
}

func (n *socksClientNode) refreshTLSConfig() {
	result, err := resolveNodeTLSClientConfig(&n.NodeBase, n.tlsConfigName)
	n.tlsConfig = result.config
	n.tlsConfigEffective = result.effectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
}

func (n *socksClientNode) currentTLSConfig() *tls.Config {
	return cloneTLSClientConfig(n.tlsConfig)
}

func (n *socksClientNode) requestOutNetwork() {
	n.RequestPort(n.out)
}

func (n *socksClientNode) requestProxyURL() {
	n.RequestPort(n.url)
}

func (n *socksClientNode) sendAll() {
	if n.client != nil && n.outNetwork != nil {
		n.outputs.SendPortLocked(n.W, n.ID, n.in, n.client)
	}
}

func (n *socksClientNode) sendNetworkToLink(link uint64) {
	if n.client != nil && n.outNetwork != nil {
		n.outputs.SendLocked(n.W, n.ID, link, n.client)
	}
}

func (n *socksClientNode) refreshNetworkToLink(link uint64) {
	if n.client != nil && n.outNetwork != nil {
		n.outputs.SendLocked(n.W, n.ID, link, n.client)
	}
}

func (n *socksClientNode) updateLabel() error {
	if n.errText != "" {
		if strings.HasPrefix(n.errText, "TLS config:") {
			return n.SetLabel("invalid TLS config")
		}
		return n.SetLabel("invalid proxy URL")
	}
	if n.outNetwork == nil {
		return n.SetLabel("waiting for out")
	}
	return n.SetLabel(n.proxyURL)
}

func (n *socksClientNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *socksClientNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *socksClientNode) configBlock() formular.Block {
	items := []formular.Item{
		{
			Type:  formular.ItemField,
			ID:    "proxyURL",
			Label: "Proxy URL",
			Field: &formular.Field{
				Kind:     formular.FieldText,
				Value:    n.proxyURL,
				Readonly: n.PortHasLinks(n.url),
			},
		},
		configSecretNameField(socksClientPasswordSecretFieldID, "Password secret", n.passwordSecret),
		nodeTLSConfigSelectorItem(&n.NodeBase, n.tlsConfigName, n.tlsConfigErr),
	}
	if n.errText != "" {
		items = append(items, formular.Item{
			Type:   formular.ItemLabel,
			ID:     "error",
			Text:   "Error: " + n.errText,
			Format: formular.TextPlain,
		})
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Items: items}
}
