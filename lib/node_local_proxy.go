package almagest

import (
	_ "embed"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return localProxyClass{engine: e}
	}, false)
}

const (
	// NodeTypeLocalProxy is the node class name for a local SOCKS proxy backed
	// by system LocalNet for incoming client connections.
	NodeTypeLocalProxy = "almagest/LocalProxy"
)

var (
	//go:embed node_local_proxy.md
	localProxyClassLongDescription string
)

type localProxyClass struct {
	engine *engine
}

func (localProxyClass) ClassName() string        { return NodeTypeLocalProxy }
func (localProxyClass) ShortDescription() string { return "Local proxy" }
func (localProxyClass) LongDescription() string  { return localProxyClassLongDescription }
func (localProxyClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "proxy"),
	}}
}
func (c localProxyClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSocksServerConfig()
	config.read(cfg)
	return &localProxyNode{NodeBase: NodeBase{engine: c.engine}, config: config}, nil
}

// localProxyNode accepts SOCKS client connections on sysnet.System.LocalNet and
// executes requested proxy operations on the connected proxy Network port.
type localProxyNode struct {
	NodeBase

	proxy uint64

	config       socksServerConfig
	localNetwork NetworkCloser
	proxyNetwork NetworkCloser

	worker    *socksServerWorker
	restartID uint64
	logs      []formular.LogLine
	formError string
}

type localProxyStart struct {
	id  uint64
	cfg socksServerWorkerConfig
}

func (n *localProxyNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.config.commands == nil {
		n.config.commands = defaultSocksServerCommands()
	}
	if n.config.wsPath == "" {
		n.config.wsPath = DefaultSocksServerWSPath
	}

	if restored != nil && len(restored.RightPorts) > 0 {
		n.proxy = restored.RightPorts[0]
	}
	if n.engine != nil && n.engine.sys != nil {
		// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
		n.localNetwork = gonnect.DetachNetwork(n.engine.sys.LocalNet(), nil, nil)
		BindNetworkResource(n.w, n.id, 0, n.localNetwork)
	}

	if err := n.SetPrimary(TypeNetwork); err != nil {
		n.logger.Errf("failed to set node primary type: %v", err)
		return err
	}

	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *localProxyNode) OnReady() error {
	n.RequestPort(n.proxy)
	n.restartWorker()
	return nil
}

func (n *localProxyNode) OnStop() {
	n.stopWorker()
}

func (n *localProxyNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.proxy || portDirection != "right" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	if n.PortHasLinks(port) {
		return pasta.ErrLinkDup
	}
	return nil
}

func (n *localProxyNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.proxy {
		std.RequestLocked(n.w, n.id, link)
	}
	return nil
}

func (n *localProxyNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	if port == n.proxy {
		n.proxyNetwork = nil
		n.restartWorker()
	}
	return nil
}

func (n *localProxyNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.proxy || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	next, changed := AcceptNetworkPayload(n.w, n.id, event, n.proxyNetwork)
	if !changed {
		return nil
	}
	n.proxyNetwork = next
	n.restartWorker()
	return nil
}

func (n *localProxyNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case socksServerLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	case localProxyStart:
		if payload.id == n.restartID && n.worker == nil && n.localNetwork != nil {
			n.startWorker(payload.cfg)
		}
	}
	return nil
}

func (n *localProxyNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *localProxyNode) OnSave(cfg configer.Config) error {
	return saveSocksServerConfig(cfg, n.config)
}

func (n *localProxyNode) applyConfigForm(values map[string]any) error {
	next := socksServerConfigWithValues(n.config, values)
	if err := next.validate(); err != nil {
		n.formError = err.Error()
		n.appendLog(formular.LogWarn, err.Error())
		n.sendConfigBlock()
		n.sendLogsBlock()
		return nil
	}

	n.formError = ""
	n.config = next
	_ = n.updateLabel()
	n.sendForcedMenuSnapshot()
	n.restartWorker()
	return nil
}

func (n *localProxyNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	next, ok := socksServerConfigWithFieldValue(n.config, msg)
	if !ok {
		return
	}
	sendNodeFieldStatus(n.w, n.id, msg.Field, next.validate())
}

func (n *localProxyNode) restartWorker() {
	n.restartID++
	id := n.restartID
	n.logger.Debug("local proxy worker restarting")
	n.appendLog(formular.LogDebug, "worker restarting")
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		n.worker = nil
		old.Stop()
	}

	if n.localNetwork == nil {
		n.logger.Debug("local proxy worker waiting for local network")
		n.appendLog(formular.LogWarn, "waiting for local network")
		n.sendLogsBlock()
		return
	}

	proxy := gonnect.Network(&gonnect.RejectNetwork{})
	if n.proxyNetwork != nil {
		proxy = n.proxyNetwork
	}
	cfg := socksServerWorkerConfig{
		config:        n.config,
		listenNetwork: n.localNetwork,
		proxyNetwork:  proxy,
		pool:          enginePool(n.engine),
	}
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: localProxyStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

func (n *localProxyNode) startWorker(cfg socksServerWorkerConfig) {
	n.logger.Debug("local proxy worker starting")
	worker, err := n.NewRestartableWorker("local proxy", func(stop <-chan struct{}) {
		runSocksServerWorker(n.engine.ctx, n.logger, n.w, n.id, stop, cfg, n.workerSpawner)
	})
	if err != nil {
		return
	}
	n.worker = worker
}

func (n *localProxyNode) stopWorker() {
	if n.worker != nil {
		n.logger.Debug("local proxy worker stopping")
		n.worker.Stop()
		n.worker = nil
	}
}

func (n *localProxyNode) updateLabel() error {
	return n.SetLabel(socksServerConfigLabel(n.config))
}

func (n *localProxyNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock(), n.logsBlock())
}

func (n *localProxyNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.configBlock(), n.logsBlock())
}

func (n *localProxyNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.configBlock())
}

func (n *localProxyNode) sendLogsBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.logsBlock())
}

func (n *localProxyNode) configBlock() formular.Block {
	return socksServerConfigBlock(n.config, n.formError)
}

func (n *localProxyNode) logsBlock() formular.Block {
	return logsBlock("logs", "Worker", 20, n.logs)
}

func (n *localProxyNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 60, level, text)
}
