package socksnodes

import (
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type ShadowsocksServerClass struct{ Runtime Runtime }

func (ShadowsocksServerClass) ClassName() string        { return NodeTypeShadowsocksServer }
func (ShadowsocksServerClass) ShortDescription() string { return "Shadowsocks server" }
func (ShadowsocksServerClass) LongDescription() string {
	return "Accepts Shadowsocks TCP and UDP on a listen Network and routes destinations through a proxy Network."
}
func (ShadowsocksServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{NetworkPort("right", "listen"), NetworkPort("right", "proxy")}}
}
func (c ShadowsocksServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &shadowsocksServerNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: readShadowsocksConfig(cfg)}, nil
}

type shadowsocksServerNode struct {
	NodeBase
	listen, proxy               uint64
	listenNetwork, proxyNetwork NetworkCloser
	config                      shadowsocksConfig
	formError                   string
	worker                      *restartableWorker
	stopping                    *restartableWorker
	replay                      *shadowsocksReplayGuard
	generation                  uint64
	ready                       bool
	runtimeError                string
}
type shadowsocksServerStopped struct{ worker *restartableWorker }
type shadowsocksServerStatus struct {
	generation uint64
	err        error
	ready      bool
}

func (n *shadowsocksServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.listen = restoredRight(restored, 0)
	n.proxy = restoredRight(restored, 1)
	if err := n.config.validate(); err != nil {
		n.runtimeError = err.Error()
	}
	n.replay = newShadowsocksReplayGuard()
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.updateLabel()
	n.sendMenu()
	return nil
}
func (n *shadowsocksServerNode) OnReady() error {
	if !NetworkIsReady(n.listenNetwork) {
		n.RequestPort(n.listen)
	}
	if !NetworkIsReady(n.proxyNetwork) {
		n.RequestPort(n.proxy)
	}
	n.restart()
	return nil
}
func (n *shadowsocksServerNode) OnStop() {
	n.generation++
	n.stopWorker()
	_ = closeNetwork(n.listenNetwork)
	_ = closeNetwork(n.proxyNetwork)
	n.listenNetwork = nil
	n.proxyNetwork = nil
}
func (n *shadowsocksServerNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if (port != n.listen && port != n.proxy) || linkType != TypeNetwork || direction != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	if n.PortHasLinks(port) {
		return pasta.ErrLinkDup
	}
	return nil
}
func (n *shadowsocksServerNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.listen || port == n.proxy {
		std.RequestLocked(n.W, n.ID, link)
	}
	return nil
}
func (n *shadowsocksServerNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	switch port {
	case n.listen:
		_ = closeNetwork(n.listenNetwork)
		n.listenNetwork = nil
	case n.proxy:
		_ = closeNetwork(n.proxyNetwork)
		n.proxyNetwork = nil
	default:
		return nil
	}
	n.restart()
	return nil
}
func (n *shadowsocksServerNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if linkType != TypeNetwork || direction != "right" {
		return nil
	}
	switch event.ReceiverPort {
	case n.listen:
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.listenNetwork)
		if changed {
			n.listenNetwork = next
			n.restart()
		}
	case n.proxy:
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.proxyNetwork)
		if changed {
			n.proxyNetwork = next
			n.restart()
		}
	}
	return nil
}
func (n *shadowsocksServerNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case shadowsocksServerStopped:
		if msg.worker == n.stopping {
			n.stopping = nil
			n.start()
		}
	case shadowsocksServerStatus:
		if msg.generation == n.generation {
			n.ready = msg.ready
			n.runtimeError = ""
			if msg.err != nil {
				n.runtimeError = msg.err.Error()
			}
			n.updateLabel()
			n.sendConfig()
			if msg.err != nil {
				n.Logger.Errf("Shadowsocks server: %v", msg.err)
			}
		}
	case SecretValueChanged:
		if socksSecretChangeMatches(n.config.passwordSecret, msg) {
			prev, next := strings.TrimSpace(msg.SecretNamePrev), strings.TrimSpace(msg.SecretNameNew)
			if n.config.passwordSecret == prev && next != "" {
				n.config.passwordSecret = next
			}
			n.sendConfig()
			n.restart()
		}
	}
	return nil
}
func (n *shadowsocksServerNode) OnFormularMsg(message any) error {
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
		n.sendConfig()
		n.restart()
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		next := n.config.withValues(map[string]any{msg.Field.FieldID: msg.Value})
		sendNodeFieldStatus(n.W, n.ID, msg.Field, next.validate())
	}
	return nil
}
func (n *shadowsocksServerNode) OnSave(cfg configer.Config) error {
	return saveShadowsocksConfig(cfg, n.config)
}
func (n *shadowsocksServerNode) restart() {
	n.generation++
	n.ready = false
	n.runtimeError = ""
	if err := n.config.validate(); err != nil {
		n.runtimeError = err.Error()
	}
	n.updateLabel()
	if n.worker != nil {
		old := n.worker
		n.worker = nil
		n.stopping = old
		old.Stop()
		go func() {
			<-old.Done()
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: shadowsocksServerStopped{old}})
		}()
	}
	if n.listenNetwork == nil || n.proxyNetwork == nil || n.config.validate() != nil {
		return
	}
	n.start()
}
func (n *shadowsocksServerNode) start() {
	if n.worker != nil || n.stopping != nil || n.listenNetwork == nil || n.proxyNetwork == nil || n.config.validate() != nil {
		return
	}
	cfg := n.config.resolved(&n.NodeBase)
	generation := n.generation
	listenNetwork, proxyNetwork := n.listenNetwork, n.proxyNetwork
	if n.replay == nil {
		n.replay = newShadowsocksReplayGuard()
	}
	replay := n.replay
	worker, err := n.NewRestartableWorker("Shadowsocks server", func(stop <-chan struct{}) {
		runShadowsocksServer(n.Context(), n.W, n.ID, generation, stop, cfg, listenNetwork, proxyNetwork, n.WorkerSpawner, replay)
	})
	if err != nil {
		n.Logger.Errf("Shadowsocks server worker: %v", err)
		return
	}
	n.worker = worker
}
func (n *shadowsocksServerNode) stopWorker() {
	if n.worker != nil {
		n.worker.Stop()
		n.worker = nil
	}
	if n.stopping != nil {
		n.stopping.Stop()
		n.stopping = nil
	}
}
func (n *shadowsocksServerNode) updateLabel() {
	label := "starting"
	switch {
	case n.config.validate() != nil:
		label = "invalid Shadowsocks config"
	case n.listenNetwork == nil:
		label = "waiting for listen"
	case n.proxyNetwork == nil:
		label = "waiting for proxy"
	case n.runtimeError != "":
		label = "Shadowsocks error"
	case n.ready:
		label = n.config.listenAddress()
	}
	_ = n.SetLabel(label)
}
func (n *shadowsocksServerNode) sendMenu() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.config.configBlock(shadowsocksErrorText(n.formError, n.runtimeError), true))
}
func (n *shadowsocksServerNode) sendConfig() {
	sendNodeBlockSnapshot(n.W, n.ID, n.config.configBlock(shadowsocksErrorText(n.formError, n.runtimeError), true))
}

const shadowsocksServerRetryDelay = time.Second
