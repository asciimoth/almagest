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
	return &socksClientNode{pool: enginePool(c.engine), proxyURL: readConfigString(cfg, "proxyURL", DefaultSocksClientProxyURL)}, nil
}

type socksClientNode struct {
	pasta.BasicNode

	pool   bufpool.Pool
	logger pasta.Logger

	w   *pasta.Workspace
	id  uint64
	in  uint64
	out uint64
	url uint64

	outNetwork NetworkCloser
	client     *socksgo.Client
	sent       map[uint64]NetworkCloser
	proxyURL   string
	errText    string
}

func (n *socksClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	if n.proxyURL == "" {
		n.proxyURL = DefaultSocksClientProxyURL
	}

	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.in = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.out = restored.RightPorts[0]
		}
		if len(restored.LeftPorts) > 1 {
			n.url = restored.LeftPorts[1]
		}
	}

	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
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
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}
	case n.url:
		if portDirection != "left" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
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
		std.Request(n.w, n.id, link)
	case n.url:
		n.sendConfigBlock()
		std.Request(n.w, n.id, link)
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
		n.closeSentNetwork(link)
	}
	return nil
}

func (n *socksClientNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendNetworkToLink(event.Link)
		return nil
	}

	if event.ReceiverPort == n.out && receiverPortDirection == "right" && linkType == TypeNetwork {
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil {
			return nil
		}
		if NetworkIsReady(n.outNetwork) && SameNetworkSource(n.outNetwork, payload.Network) {
			if !SameNetworkInstance(n.outNetwork, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.outNetwork = payload.Network
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
	if n.portHasLinks(n.url) {
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
	n.closeSentNetworks()
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
	n.requestPort(n.out)
}

func (n *socksClientNode) requestProxyURL() {
	n.requestPort(n.url)
}

func (n *socksClientNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *socksClientNode) sendAll() {
	port, ok := n.w.PortSnapshot(n.in)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendNetworkToLink(link)
	}
}

func (n *socksClientNode) sendNetworkToLink(link uint64) {
	if n.client == nil || n.outNetwork == nil {
		return
	}
	n.closeSentNetwork(link)
	wrapper := gonnect.DetachNetwork(n.client, nil)
	if n.sent == nil {
		n.sent = make(map[uint64]NetworkCloser)
	}
	n.sent[link] = wrapper
	BindNetworkResource(n.w, n.id, link, wrapper)
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}

func (n *socksClientNode) closeSentNetwork(link uint64) {
	if n.sent == nil {
		return
	}
	network := n.sent[link]
	delete(n.sent, link)
	if network != nil {
		_ = network.Close()
	}
}

func (n *socksClientNode) closeSentNetworks() {
	for link, network := range n.sent {
		delete(n.sent, link)
		if network != nil {
			_ = network.Close()
		}
	}
}

func (n *socksClientNode) updateLabel() error {
	if n.errText != "" {
		return n.w.SetNodeLabel(n.id, "invalid proxy URL")
	}
	if n.outNetwork == nil {
		return n.w.SetNodeLabel(n.id, "waiting for out")
	}
	return n.w.SetNodeLabel(n.id, n.proxyURL)
}

func (n *socksClientNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *socksClientNode) sendConfigBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.configBlock(),
	})
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
				Readonly: n.portHasLinks(n.url),
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

func (n *socksClientNode) portHasLinks(port uint64) bool {
	if n.w == nil || port == 0 {
		return false
	}
	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}
