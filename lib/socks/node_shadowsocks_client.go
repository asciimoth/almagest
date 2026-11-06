package socksnodes

import (
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type ShadowsocksClientClass struct{ Runtime Runtime }

func (ShadowsocksClientClass) ClassName() string        { return NodeTypeShadowsocksClient }
func (ShadowsocksClientClass) ShortDescription() string { return "Shadowsocks client" }
func (ShadowsocksClientClass) LongDescription() string {
	return "Routes TCP and UDP through a Shadowsocks server on an attached upstream Network."
}
func (ShadowsocksClientClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{NetworkPort("left", "Network"), NetworkPort("right", "out")}}
}
func (c ShadowsocksClientClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &shadowsocksClientNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: readShadowsocksConfig(cfg)}, nil
}

type shadowsocksClientNode struct {
	NodeBase
	in, out      uint64
	upstream     NetworkCloser
	network      *shadowsocksNetwork
	outputs      NetworkOutputCache
	config       shadowsocksConfig
	formError    string
	runtimeError string
}

func (n *shadowsocksClientNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.in = restoredLeft(restored, 0)
	n.out = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.rebuild()
	n.sendMenu()
	return nil
}
func (n *shadowsocksClientNode) OnReady() error {
	if !NetworkIsReady(n.upstream) {
		n.RequestPort(n.out)
	}
	n.sendAll()
	return nil
}
func (n *shadowsocksClientNode) OnStop() {
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.network = nil
	_ = closeNetwork(n.upstream)
	n.upstream = nil
}
func (n *shadowsocksClientNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	switch port {
	case n.in:
		if direction == "left" {
			return nil
		}
	case n.out:
		if direction == "right" {
			if n.PortHasLinks(port) {
				return pasta.ErrLinkDup
			}
			return nil
		}
	}
	return pasta.LinkTypeErr(linkType)
}
func (n *shadowsocksClientNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.in:
		n.sendTo(link)
	case n.out:
		std.RequestLocked(n.W, n.ID, link)
	}
	return nil
}
func (n *shadowsocksClientNode) OnLinkRemoved(link, port uint64, _, _ string) error {
	switch port {
	case n.in:
		n.outputs.Close(link)
	case n.out:
		_ = closeNetwork(n.upstream)
		n.upstream = nil
		n.rebuild()
	}
	return nil
}
func (n *shadowsocksClientNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if linkType != TypeNetwork {
		return nil
	}
	if event.ReceiverPort == n.in && direction == "left" && std.IsRequest(event.Payload) {
		n.sendTo(event.Link)
		return nil
	}
	if event.ReceiverPort == n.out && direction == "right" {
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.upstream)
		if changed {
			n.upstream = next
			n.rebuild()
			n.sendConfig()
		}
	}
	return nil
}
func (n *shadowsocksClientNode) OnInbox(message pasta.InboxMessage) error {
	if msg, ok := message.Payload.(SecretValueChanged); ok && socksSecretChangeMatches(n.config.passwordSecret, msg) {
		prev, next := strings.TrimSpace(msg.SecretNamePrev), strings.TrimSpace(msg.SecretNameNew)
		if n.config.passwordSecret == prev && next != "" {
			n.config.passwordSecret = next
		}
		n.rebuild()
		n.sendConfig()
	}
	return nil
}
func (n *shadowsocksClientNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		next := n.config.withValues(msg.Values)
		if err := next.validate(); err != nil {
			n.formError = err.Error()
			n.sendConfig()
			return nil
		}
		if next == n.config {
			n.formError = ""
			n.sendConfig()
			return nil
		}
		n.config = next
		n.formError = ""
		n.rebuild()
		n.sendConfig()
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		next := n.config.withValues(map[string]any{msg.Field.FieldID: msg.Value})
		sendNodeFieldStatus(n.W, n.ID, msg.Field, next.validate())
	}
	return nil
}
func (n *shadowsocksClientNode) OnSave(cfg configer.Config) error {
	return saveShadowsocksConfig(cfg, n.config)
}
func (n *shadowsocksClientNode) rebuild() {
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.network = nil
	n.runtimeError = ""
	if err := n.config.validate(); err != nil {
		n.runtimeError = err.Error()
	}
	if n.upstream == nil {
		_ = n.SetLabel("waiting for out")
		return
	}
	if n.runtimeError != "" {
		_ = n.SetLabel("invalid Shadowsocks config")
		return
	}
	c := n.config.resolved(&n.NodeBase)
	network, err := newShadowsocksNetwork(n.upstream, c.address, c.method, c.password, c.tcp, c.udp)
	if err != nil {
		n.runtimeError = err.Error()
		_ = n.SetLabel("invalid Shadowsocks config")
		return
	}
	n.network = network
	_ = n.SetLabel(n.config.address)
	n.sendAll()
}
func (n *shadowsocksClientNode) sendAll() {
	if n.network != nil {
		n.outputs.SendPortLocked(n.W, n.ID, n.in, n.network)
	}
}
func (n *shadowsocksClientNode) sendTo(link uint64) {
	if n.network != nil {
		n.outputs.SendLocked(n.W, n.ID, link, n.network)
	}
}
func (n *shadowsocksClientNode) sendMenu() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.config.configBlock(shadowsocksErrorText(n.formError, n.runtimeError), false))
}
func (n *shadowsocksClientNode) sendConfig() {
	sendNodeBlockSnapshot(n.W, n.ID, n.config.configBlock(shadowsocksErrorText(n.formError, n.runtimeError), false))
}
