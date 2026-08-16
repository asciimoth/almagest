package socksnodes

import (
	_ "embed"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeLocalProxy is the node class name for a local SOCKS proxy backed
	// by system LocalNet for incoming client connections.
	NodeTypeLocalProxy = "almagest/LocalProxy"
)

var (
	//go:embed node_local_proxy.md
	localProxyClassLongDescription string
)

type LocalProxyClass struct {
	Runtime Runtime
}

func (LocalProxyClass) ClassName() string        { return NodeTypeLocalProxy }
func (LocalProxyClass) ShortDescription() string { return "Local proxy" }
func (LocalProxyClass) LongDescription() string  { return localProxyClassLongDescription }
func (LocalProxyClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "proxy"),
	}}
}
func (c LocalProxyClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSocksServerConfig()
	config.read(cfg)
	return &localProxyNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: config}, nil
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
	if sys := n.System(); sys != nil {
		// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
		n.localNetwork = gonnect.DetachNetwork(sys.LocalNet(), nil, nil)
		BindNetworkResource(n.W, n.ID, 0, n.localNetwork)
	}

	if err := n.SetPrimary(TypeNetwork); err != nil {
		n.Logger.Errf("failed to set node primary type: %v", err)
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
		std.RequestLocked(n.W, n.ID, link)
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
	next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.proxyNetwork)
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
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
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
	sendNodeFieldStatus(n.W, n.ID, msg.Field, next.validate())
}

func (n *localProxyNode) restartWorker() {
	n.restartID++
	id := n.restartID
	n.Logger.Debug("local proxy worker restarting")
	n.appendLog(formular.LogDebug, "worker restarting")
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		n.worker = nil
		old.Stop()
	}

	if n.localNetwork == nil {
		n.Logger.Debug("local proxy worker waiting for local network")
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
		pool:          enginePool(n.Runtime),
	}
	if old != nil {
		go func() {
			<-old.Done()
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: localProxyStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

func (n *localProxyNode) startWorker(cfg socksServerWorkerConfig) {
	n.Logger.Debug("local proxy worker starting")
	worker, err := n.NewRestartableWorker("local proxy", func(stop <-chan struct{}) {
		runSocksServerWorker(n.Context(), n.Logger, n.W, n.ID, stop, cfg, n.WorkerSpawner)
	})
	if err != nil {
		return
	}
	n.worker = worker
}

func (n *localProxyNode) stopWorker() {
	if n.worker != nil {
		n.Logger.Debug("local proxy worker stopping")
		n.worker.Stop()
		n.worker = nil
	}
}

func (n *localProxyNode) updateLabel() error {
	return n.SetLabel(socksServerConfigLabel(n.config))
}

func (n *localProxyNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock(), n.logsBlock())
}

func (n *localProxyNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock(), n.logsBlock())
}

func (n *localProxyNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *localProxyNode) sendLogsBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.logsBlock())
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
