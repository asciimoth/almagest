package socksnodes

import (
	"context"
	"crypto/tls"
	"sync"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const testNativeNetworkNodeType = "test/NativeNetwork"

type testRuntime struct {
	tlsConfigs storage.TlsConfigStorage
}

func (r *testRuntime) Context() context.Context {
	return context.Background()
}

func (r *testRuntime) Pool() bufpool.Pool {
	return nil
}

func (r *testRuntime) System() sysnet.System {
	return nil
}

func (r *testRuntime) KVManager() storage.KVManager {
	return nil
}

func (r *testRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}

func (r *testRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}

func (r *testRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	if r == nil {
		return nil
	}
	return r.tlsConfigs
}

func (r *testRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return nil
}

func (r *testRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

type nodeTestTLSConfigStorage struct {
	mu      sync.Mutex
	configs map[string]string
	errs    map[string]error
}

func newNodeTestTLSConfigStorage(configs map[string]string) *nodeTestTLSConfigStorage {
	return &nodeTestTLSConfigStorage{configs: configs}
}

func (s *nodeTestTLSConfigStorage) ListConfigs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	return names
}

func (s *nodeTestTLSConfigStorage) GetClientConfig(name string, _ string) (tls.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errs[name]; err != nil {
		return tls.Config{}, err
	}
	serverName, ok := s.configs[name]
	if !ok {
		return tls.Config{}, storage.ErrTlsConfigNotFound
	}
	return tls.Config{ServerName: serverName}, nil
}

func (s *nodeTestTLSConfigStorage) GetLocalCA(string) (tls.Certificate, error) {
	return tls.Certificate{}, storage.ErrTlsConfigNotFound
}

type testNativeNetworkClass struct{}

func (testNativeNetworkClass) ClassName() string        { return testNativeNetworkNodeType }
func (testNativeNetworkClass) ShortDescription() string { return "Native network" }
func (testNativeNetworkClass) LongDescription() string  { return "Test-only native network source." }
func (testNativeNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{NetworkPort("left", "Network")}}
}
func (testNativeNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testNativeNetworkNode{}, nil
}

type testNativeNetworkNode struct {
	pasta.BasicNode
	w    *pasta.Workspace
	id   uint64
	port uint64
	net  gonnect.Network
}

func (n *testNativeNetworkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.net = gonnect.DefaultNetwork(nil)
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.port = restored.LeftPorts[0]
	}
	if err := n.w.SetNodePrimaryLocked(n.id, TypeNetwork); err != nil {
		return err
	}
	return n.w.SetNodeLabelLocked(n.id, "native")
}

func (n *testNativeNetworkNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *testNativeNetworkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.port || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *testNativeNetworkNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.port && portDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(link)
	}
	return nil
}

func (n *testNativeNetworkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.port && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendToLink(event.Link)
	}
	return nil
}

func (n *testNativeNetworkNode) sendAll() {
	snapshot, ok := n.w.PortSnapshotLocked(n.port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		n.sendToLink(link)
	}
}

func (n *testNativeNetworkNode) sendToLink(link uint64) {
	wrapper := gonnect.DetachNetwork(n.net, nil, nil)
	BindNetworkResource(n.w, n.id, link, wrapper)
	n.w.EmitEventLocked(n.id, link, NetworkPayload{Network: wrapper})
}

type testNetworkWrapper struct {
	NetworkCloser
	wrapped any
	up      bool
	closed  bool
}

func (w *testNetworkWrapper) GetWrapped() any { return w.wrapped }

func (w *testNetworkWrapper) IsUp() (bool, error) { return w.up, nil }

func (w *testNetworkWrapper) Close() error {
	w.closed = true
	w.up = false
	if w.NetworkCloser == nil {
		return nil
	}
	return w.NetworkCloser.Close()
}

func closeTestNetwork(n NetworkCloser) {
	if n != nil {
		_ = n.Close()
	}
}

type testWorkerSpawner struct{}

func (testWorkerSpawner) Spawn(worker func(), _ string) (uint64, error) {
	go worker()
	return 1, nil
}

func (testWorkerSpawner) SpawnWg(worker func(), wg *sync.WaitGroup, _ string) (uint64, error) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker()
	}()
	return 1, nil
}

func newTestLogFactory() *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(1024)
}
