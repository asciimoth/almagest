package socksnodes

import (
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

type LocalProxyClass struct {
	Runtime Runtime
}

func (LocalProxyClass) ClassName() string        { return NodeTypeLocalProxy }
func (LocalProxyClass) ShortDescription() string { return "Local proxy" }
func (LocalProxyClass) LongDescription() string {
	return "Publishes a local SOCKS proxy for an attached Network."
}
func (LocalProxyClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "proxy"),
	}}
}
func (c LocalProxyClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultLocalProxyConfig()
	config.read(cfg)
	return &localProxyNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: config}, nil
}

// localProxyNode accepts SOCKS client connections on sysnet.System.LocalNet and
// executes requested proxy operations on the connected proxy Network port.
type localProxyNode struct {
	NodeBase

	proxy uint64

	config       localProxyConfig
	localNetwork NetworkCloser
	proxyNetwork NetworkCloser

	worker         *socksServerWorker
	workerStopping bool
	logs           []formular.LogLine
	formError      string
	ruleDraftType  map[string]string
}

type localProxyStart struct{ previous *socksServerWorker }

func (n *localProxyNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.config.server.commands == nil {
		n.config.server.commands = defaultSocksServerCommands()
	}
	if n.config.server.wsPath == "" {
		n.config.server.wsPath = DefaultSocksServerWSPath
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
		if payload.previous != nil && payload.previous == n.worker {
			n.worker = nil
			n.workerStopping = false
			n.startCurrentWorker()
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
	case formular.AutocompleteRequestMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigAutocompleteHints(msg)
	}
	return nil
}

func (n *localProxyNode) OnSave(cfg configer.Config) error {
	return saveLocalProxyConfig(cfg, n.config)
}

func (n *localProxyNode) applyConfigForm(values map[string]any) error {
	next := localProxyConfigWithValues(n.config, values)
	if err := n.validateLocalProxyConfig(next); err != nil {
		n.formError = err.Error()
		n.appendLog(formular.LogWarn, err.Error())
		n.sendConfigBlock()
		n.sendLogsBlock()
		return nil
	}

	n.formError = ""
	n.config = next
	n.ruleDraftType = nil
	_ = n.updateLabel()
	n.sendForcedMenuSnapshot()
	n.restartWorker()
	return nil
}

func (n *localProxyNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	if len(msg.Field.ElementPath) > 0 {
		sendNodeFieldStatus(n.W, n.ID, msg.Field, n.validateLocalProxyRuleField(msg))
		return
	}
	nextServer, ok := socksServerConfigWithFieldValue(n.config.server, msg)
	if ok {
		next := n.config
		next.server = nextServer
		sendNodeFieldStatus(n.W, n.ID, msg.Field, n.validateLocalProxyConfig(next))
	}
}

func (n *localProxyNode) restartWorker() {
	n.Logger.Debug("local proxy worker restarting")
	n.appendLog(formular.LogDebug, "worker restarting")
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		if !n.workerStopping {
			n.workerStopping = true
			old.Stop()
			go func() {
				<-old.Done()
				n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: localProxyStart{previous: old}})
			}()
		}
		return
	}
	n.startCurrentWorker()
}

func (n *localProxyNode) startCurrentWorker() {
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
		config:        n.config.server,
		listenNetwork: n.localNetwork,
		proxyNetwork:  proxy,
		pool:          enginePool(n.Runtime),
		clientSystem:  n.System(),
		clientExclude: n.config.exclude,
		clientInclude: n.config.include,
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
		if !n.workerStopping {
			n.worker.Stop()
		}
		n.worker = nil
		n.workerStopping = false
	}
}

func (n *localProxyNode) updateLabel() error {
	return n.SetLabel(socksServerConfigLabel(n.config.server))
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
	return localProxyConfigBlock(n.config, n.formError)
}

func (n *localProxyNode) logsBlock() formular.Block {
	return logsBlock("logs", "Worker", 20, n.logs)
}

func (n *localProxyNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 60, level, text)
}
