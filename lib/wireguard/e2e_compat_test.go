package wireguardnodes_test

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	networknodes "github.com/asciimoth/almagest/lib/network"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/almagest/lib/testkit/sockstest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	E2EWorkspace = e2etest.E2EWorkspace
	TypeNetwork  = links.TypeNetwork
	TypeTun      = links.TypeTun
	TypeWGCtrl   = wireguardnodes.TypeWGCtrl

	NodeTypeMockNetwork = networknodes.NodeTypeMockNetwork
	NodeTypeOutNetwork  = networknodes.NodeTypeOutNetwork

	NodeTypeSocksClient = socksnodes.NodeTypeSocksClient

	NodeTypeTunConfigStripper = tunnodes.NodeTypeTunConfigStripper
	NodeTypeVTun              = tunnodes.NodeTypeVTun
	NodeTypeWireGuard         = wireguardnodes.NodeTypeWireGuard
	NodeTypeWGConfig          = wireguardnodes.NodeTypeWGConfig
)

type (
	Engine             = almagest.Engine
	Secret             = storage.Secret
	SecretsStorage     = storage.SecretsStorage
	SecretValueChanged = storage.SecretValueChanged
	TunConfig          = links.TunConfig
	e2eSecretsStorage  = e2etest.SecretsStorage
)

func BuildEngine(ctx context.Context, cfg configer.Config, logf logging.LogFactory, sys sysnet.System, kv storage.KVManager, dns any, secrets storage.SecretsStorage, tlsConfigs storage.TlsConfigStorage, extra []pasta.NodeClass, pool bufpool.Pool, debug bool) (almagest.Engine, error) {
	return almagest.BuildEngine(ctx, cfg, logf, sys, kv, nil, secrets, tlsConfigs, extra, pool, debug)
}

func NewDebugLogFactory(buffer int) *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewNodeE2E(t *testing.T) *NodeE2E {
	return e2etest.NewNodeE2E(t)
}

func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System, secrets *e2eSecretsStorage) *NodeE2E {
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}

func newE2EDebugSystem() *sysnetdebug.System {
	return e2etest.NewDebugSystem()
}

func newE2ETestDebugPool(t *testing.T) *e2etest.DebugPool {
	return e2etest.NewTestDebugPool(t)
}

func newE2ESecretsStorage() *e2eSecretsStorage {
	return e2etest.NewSecretsStorage()
}

func newNodeE2EWithConfig(t *testing.T, cfg configer.Config) *NodeE2E {
	return e2etest.NewNodeE2EWithConfig(t, cfg)
}

func newNodeE2EWithConfigAndSecrets(t *testing.T, cfg configer.Config, secrets *e2eSecretsStorage) *NodeE2E {
	return e2etest.NewNodeE2EWithConfigAndSecrets(t, cfg, secrets)
}

func httpTestPort() int {
	return e2etest.TestPort()
}

func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func TunPort(direction, name string) pasta.Port {
	return links.TunPort(direction, name)
}

func TunPreLinkAdd(w *pasta.Workspace, port uint64, linkType string) error {
	return links.TunPreLinkAdd(w, port, linkType)
}

type NodeE2E = e2etest.NodeE2E

type httpE2E struct {
	*NodeE2E
	*httpe2etest.HTTPOps
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(NewNodeE2E(t))
}

func newHTTPE2EFromNodeE2E(e *NodeE2E) *httpE2E {
	return &httpE2E{NodeE2E: e, HTTPOps: httpe2etest.NewHTTPOps(e)}
}

func (e *httpE2E) addHTTPServerOnHost(name, host string, port int) uint64 {
	e.T.Helper()
	id := e.AddNode(httpnodes.NodeTypeHTTPServer, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "listen",
		Values: map[string]any{
			"host": host,
			"port": port,
		},
	})
	e.WaitNodeLabel(id, host+":"+itoa(port))
	return id
}

func (e *httpE2E) addHTTPClientToURL(name, url string) uint64 {
	e.T.Helper()
	id := e.AddNode(httpnodes.NodeTypeHTTPClient, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, id),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       url,
	})
	return id
}

func (e *httpE2E) addSocksServer(name string, port int, users []sockstest.User) uint64 {
	return e.AddSocksServer(name, port, users)
}

func (e *httpE2E) addStringConstant(name, value string) uint64 {
	return e.AddStringConstant(name, value)
}

func (e *httpE2E) connectByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.ConnectByPortName(leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) connectTunByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.MustAddTypedLinkByPortName(TypeTun, leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) mustTypedLink(linkType string, leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	left := e.NodePort(leftNode, leftDir, leftName)
	right := e.NodePort(rightNode, rightDir, rightName)
	link, snapshot, ok := e.Engine.LinkByPorts(E2EWorkspace, left, right)
	if !ok {
		e.T.Fatalf("missing link between %d/%s/%s and %d/%s/%s", leftNode, leftDir, leftName, rightNode, rightDir, rightName)
	}
	if snapshot.Type != linkType {
		e.T.Fatalf("link %d type = %q, want %q", link, snapshot.Type, linkType)
	}
	return link
}

func (e *httpE2E) mustNetworkLink(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.mustTypedLink(TypeNetwork, leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) mustNodeByName(name string) uint64 {
	e.T.Helper()
	id, ok := e.Engine.NodeByName(E2EWorkspace, name)
	if !ok {
		e.T.Fatalf("node %q missing", name)
	}
	return id
}

func (e *httpE2E) waitServerListening(server uint64, address string) {
	e.WaitServerListening(server, address)
}

func (e *httpE2E) waitSocksLog(socks uint64, wantPart string) {
	e.WaitSocksLog(socks, wantPart)
}

func (e *httpE2E) assertServerLog(server uint64, wantPart string) {
	e.AssertServerLog(server, wantPart)
}

func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.WaitClientRequestField(client, field, want)
}

func (e *httpE2E) clientStatus(client uint64) string {
	return e.ClientStatus(client)
}

func (e *httpE2E) clientResultField(client uint64, field string) string {
	return e.ClientResultField(client, field)
}

func (e *httpE2E) assertClientRequestReturns(client uint64, response string, timeout time.Duration) {
	e.AssertClientRequestReturns(client, response, timeout)
}

func (e *httpE2E) assertNodesLinked(a, b uint64) {
	e.T.Helper()
	if !e.Engine.NodesConnected(E2EWorkspace, a, b) {
		e.T.Fatalf("nodes %d and %d are not linked", a, b)
	}
}

func (e *httpE2E) applyTunCopyVTunConfig(node uint64, name, localAddrs string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"name":           name,
			"localAddrs":     localAddrs,
			"dnsServers":     "",
			"mtu":            1500,
			"endpointCh":     1024,
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
		},
	})
}

func newNetworkRouterConfigE2EWithSecrets(t *testing.T, cfg configer.Config, secrets *e2eSecretsStorage) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(e2etest.NewNodeE2EWithConfigAndSecrets(t, cfg, secrets))
}

const testTunConfigRecorderNodeType = "test/TunConfigRecorder"

type tunConfigRecorderRegistry struct {
	mu    sync.Mutex
	nodes map[uint64]*tunConfigRecorderNode
}

func newTunConfigRecorderRegistry() *tunConfigRecorderRegistry {
	return &tunConfigRecorderRegistry{nodes: map[uint64]*tunConfigRecorderNode{}}
}

func (r *tunConfigRecorderRegistry) register(id uint64, node *tunConfigRecorderNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[id] = node
}

type tunConfigRecorderClass struct {
	registry *tunConfigRecorderRegistry
}

func (tunConfigRecorderClass) ClassName() string        { return testTunConfigRecorderNodeType }
func (tunConfigRecorderClass) ShortDescription() string { return "test TunConfig recorder" }
func (tunConfigRecorderClass) LongDescription() string {
	return "Test-only right-directed almagest/tun node that records requests and TunConfig payloads."
}
func (tunConfigRecorderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{TunPort("right", "Tun")}}
}
func (c tunConfigRecorderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunConfigRecorderNode{registry: c.registry}, nil
}

type tunConfigRecorderNode struct {
	pasta.BasicNode

	registry *tunConfigRecorderRegistry
	mu       sync.Mutex
	out      uint64
	configs  []TunConfig
}

func (n *tunConfigRecorderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if restored != nil && len(restored.RightPorts) > 0 {
		n.out = restored.RightPorts[0]
	}
	if n.registry != nil {
		n.registry.register(id, n)
	}
	return w.SetNodePrimaryLocked(id, TypeTun)
}

func (n *tunConfigRecorderNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

func (n *tunConfigRecorderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != TypeTun || std.IsRequest(event.Payload) {
		return nil
	}
	if cfg, ok := event.Payload.(TunConfig); ok {
		n.mu.Lock()
		n.configs = append(n.configs, cfg.Clone())
		n.mu.Unlock()
	}
	return nil
}

func assertPortShape(t *testing.T, e *NodeE2E, port uint64, direction, name, linkType string) {
	t.Helper()
	snapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) missing", port)
	}
	if snapshot.Direction != direction || snapshot.Name != name || !reflect.DeepEqual(snapshot.Types, []string{linkType}) {
		t.Fatalf("port %d = %#v, want %s %q %q", port, snapshot, direction, name, linkType)
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
