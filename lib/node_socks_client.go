package almagest

import (
	"strings"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return socksClientClass{engine: e}
	}, false)
}

const (
	// NodeTypeSocksClient is the node class name for a SOCKS proxy client.
	NodeTypeSocksClient = "almagest/SocksClient"

	// DefaultSocksClientProxyURL is the default SOCKS proxy URL.
	DefaultSocksClientProxyURL = "socks5://127.0.0.1:1080"
)

type socksClientClass struct {
	engine *engine
}

func (socksClientClass) ClassName() string        { return NodeTypeSocksClient }
func (socksClientClass) ShortDescription() string { return "SOCKS client" }
func (socksClientClass) LongDescription() string {
	return "Provides a network that proxies all operations through a SOCKS server reached over the out network."
}
func (socksClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "out"),
		{Direction: "left", Name: "Proxy URL", Types: []string{std.TypeString}},
	}}
}
func (c socksClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &socksClientNode{pool: enginePool(c.engine), proxyURL: configer.String(cfg, configer.Path{"proxyURL"}, DefaultSocksClientProxyURL)}, nil
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
	errText    string
}

func (n *socksClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.proxyURL == "" {
		n.proxyURL = DefaultSocksClientProxyURL
	}

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
	n.requestOutNetwork()
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
		std.RequestLocked(n.w, n.id, link)
	case n.url:
		n.sendConfigBlock()
		std.RequestLocked(n.w, n.id, link)
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
		next, changed := AcceptNetworkPayload(n.w, n.id, event, n.outNetwork)
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

func (n *socksClientNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FieldUpdateMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" || msg.Field.FieldID != "proxyURL" || len(msg.Field.ElementPath) > 0 {
		return nil
	}
	if n.PortHasLinks(n.url) {
		n.sendConfigBlock()
		return nil
	}
	value, ok := std.StringFromPayload(msg.Value)
	if !ok {
		n.sendConfigBlock()
		return nil
	}
	n.setProxyURL(value)
	return nil
}

func (n *socksClientNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{"proxyURL"}, n.proxyURL)
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

func (n *socksClientNode) rebuildClient() {
	n.outputs.CloseAll()
	client, err := buildSocksGoClient(n.proxyURL, n.outNetwork, n.pool)
	if err != nil {
		n.client = nil
		n.errText = err.Error()
		_ = n.updateLabel()
		return
	}
	n.client = client
	n.errText = ""
	_ = n.updateLabel()
}

func buildSocksGoClient(proxyURL string, outNetwork NetworkCloser, pool bufpool.Pool) (*socksgo.Client, error) {
	client, err := socksgo.ClientFromURL(proxyURL)
	if err != nil {
		return nil, err
	}
	client.WithNetwork(outNetwork)
	client.Pool = pool
	client.Filter = gonnect.FalseFilter
	return client, nil
}

func (n *socksClientNode) requestOutNetwork() {
	n.RequestPort(n.out)
}

func (n *socksClientNode) requestProxyURL() {
	n.RequestPort(n.url)
}

func (n *socksClientNode) sendAll() {
	n.ForEachPortLink(n.in, func(link uint64) {
		n.sendNetworkToLink(link)
	})
}

func (n *socksClientNode) sendNetworkToLink(link uint64) {
	if n.client == nil || n.outNetwork == nil {
		return
	}
	n.outputs.SendLocked(n.w, n.id, link, n.client)
}

func (n *socksClientNode) refreshNetworkToLink(link uint64) {
	if n.client == nil || n.outNetwork == nil {
		return
	}
	n.outputs.RefreshLocked(n.w, n.id, link, n.client)
}

func (n *socksClientNode) updateLabel() error {
	if n.errText != "" {
		return n.SetLabel("invalid proxy URL")
	}
	if n.outNetwork == nil {
		return n.SetLabel("waiting for out")
	}
	return n.SetLabel(n.proxyURL)
}

func (n *socksClientNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock())
}

func (n *socksClientNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.configBlock())
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
