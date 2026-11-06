package httpnodes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	fetchAddrTestNetworkClass = "test/FetchAddrNetwork"
	fetchAddrTestSourceClass  = "test/FetchAddrTriggerSource"
	fetchAddrTestSinkClass    = "test/FetchAddrSink"
)

func TestFetchAddrIntegrationAttachDetachReattachAndInputTrigger(t *testing.T) {
	t.Parallel()
	harness := newFetchAddrHarness(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ipify" {
			_, _ = response.Write([]byte(`{"ip":"2001:db8::9"}`))
			return
		}
		_, _ = response.Write([]byte(`{"ip":"203.0.113.9","country_iso":"ge","city":"Tbilisi","latitude":41.7,"longitude":44.8}`))
	}))

	networkLink := harness.linkNetwork()
	harness.linkTriggerInput()
	harness.linkAllOutputs()
	harness.fireInputTrigger()
	first := harness.sink.success(t)
	assertFetchAddrCapture(t, first, fetchAddrData{
		PublicIP: "203.0.113.9", CountryCode: "GE", City: "Tbilisi", Latitude: 41.7, Longitude: 44.8,
	})

	// A detached scalar output must receive the cached current value when it is
	// reattached. The other result ports stay live during this operation.
	publicIPLink := harness.outputLinks["Public IP"]
	harness.w.RemoveLink(publicIPLink)
	harness.sink.setPublicIP("detached")
	harness.outputLinks["Public IP"] = harness.addLink(
		harness.port(harness.fetch, "right", "Public IP"),
		harness.port(harness.sinkID, "left", "Public IP"),
	)
	harness.sink.waitPublicIP(t, "203.0.113.9")

	// Removing the graph network cancels its runtime value. A trigger reports a
	// failure, and reattachment makes the next trigger succeed again.
	harness.w.RemoveLink(networkLink)
	harness.fireInputTrigger()
	failure := harness.sink.failure(t)
	assertFetchAddrCapture(t, failure, fetchAddrData{})
	if snapshot, ok := harness.w.NodeSnapshot(harness.fetch); !ok || snapshot.Label != fetchAddrStatusFail {
		t.Fatalf("FetchAddr label after network failure = %#v, want fail", snapshot)
	}

	harness.linkNetwork()
	harness.fireInputTrigger()
	assertFetchAddrCapture(t, harness.sink.success(t), first.data)
}

func TestFetchAddrIntegrationProviderSelectionAndManualCheck(t *testing.T) {
	t.Parallel()
	harness := newFetchAddrHarness(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ipify":
			_, _ = response.Write([]byte(`{"ip":"192.0.2.44"}`))
		case "/ifconfig":
			_, _ = response.Write([]byte(`{"ip":"198.51.100.77","country_iso":"us","city":"Boston","latitude":42.36,"longitude":-71.06}`))
		case "/yggdrasil":
			response.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = response.Write([]byte("200:1ef0:cde7:15c3:f720:2804:9f39:e863\n"))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	harness.linkNetwork()
	harness.linkAllOutputs()

	harness.selectProvider(FetchAddrProviderIPify)
	harness.pressCheck()
	assertFetchAddrCapture(t, harness.sink.success(t), fetchAddrData{PublicIP: "192.0.2.44"})

	harness.selectProvider(FetchAddrProviderIfConfig)
	harness.pressCheck()
	assertFetchAddrCapture(t, harness.sink.success(t), fetchAddrData{
		PublicIP: "198.51.100.77", CountryCode: "US", City: "Boston", Latitude: 42.36, Longitude: -71.06,
	})

	harness.selectProvider(FetchAddrProviderYggdrasil)
	harness.pressCheck()
	assertFetchAddrCapture(t, harness.sink.success(t), fetchAddrData{
		PublicIP: "200:1ef0:cde7:15c3:f720:2804:9f39:e863",
	})
}

func TestFetchAddrIntegrationConcurrentNodes(t *testing.T) {
	t.Parallel()
	harness := newFetchAddrHarness(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		_, _ = response.Write([]byte(`{"ip":"203.0.113.20","country_iso":"de","city":"Berlin","latitude":52.52,"longitude":13.405}`))
	}))

	const nodeCount = 8
	type pair struct {
		fetch uint64
		sink  *fetchAddrTestSink
	}
	pairs := make([]pair, 0, nodeCount)
	for index := 0; index < nodeCount; index++ {
		fetch := harness.addFetchNode(fmt.Sprintf("fetch-parallel-%d", index))
		sinkID, sink := harness.addSinkNode(fmt.Sprintf("sink-parallel-%d", index))
		harness.linkNetworkTo(fetch)
		harness.linkOutputsTo(fetch, sinkID)
		pairs = append(pairs, pair{fetch: fetch, sink: sink})
	}

	for _, pair := range pairs {
		harness.pressCheckFor(pair.fetch)
	}
	for _, pair := range pairs {
		assertFetchAddrCapture(t, pair.sink.success(t), fetchAddrData{
			PublicIP: "203.0.113.20", CountryCode: "DE", City: "Berlin", Latitude: 52.52, Longitude: 13.405,
		})
	}
}

func TestFetchAddrIntegrationProviderChangeInvalidatesPendingRequest(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	harness := newFetchAddrHarness(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ipify" {
			close(started)
			<-release
			_, _ = response.Write([]byte(`{"ip":"192.0.2.1"}`))
			return
		}
		_, _ = response.Write([]byte(`{"ip":"198.51.100.8","country_iso":"fr","city":"Paris","latitude":48.8566,"longitude":2.3522}`))
	}))
	harness.linkNetwork()
	harness.linkAllOutputs()
	harness.selectProvider(FetchAddrProviderIPify)
	harness.pressCheck()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first provider request did not start")
	}
	harness.selectProvider(FetchAddrProviderIfConfig)
	harness.pressCheck()
	assertFetchAddrCapture(t, harness.sink.success(t), fetchAddrData{
		PublicIP: "198.51.100.8", CountryCode: "FR", City: "Paris", Latitude: 48.8566, Longitude: 2.3522,
	})
	releaseOnce.Do(func() { close(release) })
	harness.sink.assertNoTrigger(t, 100*time.Millisecond)
}

type fetchAddrCapture struct {
	data fetchAddrData
}

type fetchAddrHarness struct {
	t      *testing.T
	w      *pasta.Workspace
	server *httptest.Server

	network  uint64
	source   uint64
	fetch    uint64
	sinkID   uint64
	sink     *fetchAddrTestSink
	registry *fetchAddrTestSinkRegistry

	triggerLink uint64
	outputLinks map[string]uint64
}

func newFetchAddrHarness(t *testing.T, handler http.Handler) *fetchAddrHarness {
	t.Helper()
	server := httptest.NewServer(handler)
	ctx, cancel := context.WithCancel(context.Background())
	spawner := &fetchAddrTestSpawner{}
	runtime := fetchAddrTestRuntime{ctx: ctx, spawner: spawner}
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(4096))
	sinks := &fetchAddrTestSinkRegistry{nodes: make(map[uint64]*fetchAddrTestSink)}
	classes := []pasta.NodeClass{
		FetchAddrClass{Runtime: runtime, providers: []fetchAddrProvider{
			{name: FetchAddrProviderIPify, url: server.URL + "/ipify", parse: parseIPifyFetchAddr},
			{name: FetchAddrProviderIfConfig, url: server.URL + "/ifconfig", parse: parseIfConfigFetchAddr},
			{name: FetchAddrProviderYggdrasil, url: server.URL + "/yggdrasil", parse: parseYggdrasilFetchAddr},
		}},
		fetchAddrTestNetworkClassDef{network: gonnect.DefaultNetwork(nil)},
		fetchAddrTestTriggerSourceClass{},
		fetchAddrTestSinkClassDef{registry: sinks},
	}
	for _, class := range classes {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass(%s): %v", class.ClassName(), err)
		}
	}
	h := &fetchAddrHarness{t: t, w: w, server: server, registry: sinks, outputLinks: make(map[string]uint64)}
	h.network = h.addNode(fetchAddrTestNetworkClass, "network")
	h.source = h.addNode(fetchAddrTestSourceClass, "source")
	h.fetch = h.addFetchNode("fetch")
	h.sinkID, h.sink = h.addSinkNode("sink")
	t.Cleanup(func() {
		w.Close()
		cancel()
		server.Close()
		spawner.wait(t)
	})
	return h
}

func (h *fetchAddrHarness) addNode(class, name string) uint64 {
	h.t.Helper()
	id, err := h.w.AddNodeByClass(class, name)
	if err != nil {
		h.t.Fatalf("AddNodeByClass(%s): %v", class, err)
	}
	return id
}

func (h *fetchAddrHarness) addFetchNode(name string) uint64 {
	return h.addNode(NodeTypeFetchAddr, name)
}

func (h *fetchAddrHarness) addSinkNode(name string) (uint64, *fetchAddrTestSink) {
	id := h.addNode(fetchAddrTestSinkClass, name)
	sink := h.registry.nodes[id]
	if sink != nil {
		return id, sink
	}
	h.t.Fatalf("sink node %d was not registered", id)
	return 0, nil
}

func (h *fetchAddrHarness) linkNetwork() uint64 { return h.linkNetworkTo(h.fetch) }

func (h *fetchAddrHarness) linkNetworkTo(fetch uint64) uint64 {
	return h.addLink(h.port(h.network, "left", "Network"), h.port(fetch, "right", "Network"))
}

func (h *fetchAddrHarness) linkTriggerInput() {
	h.triggerLink = h.addLink(h.port(h.source, "right", "Trigger"), h.port(h.fetch, "left", "Check"))
}

func (h *fetchAddrHarness) linkAllOutputs() {
	h.linkOutputsTo(h.fetch, h.sinkID)
}

func (h *fetchAddrHarness) linkOutputsTo(fetch, sink uint64) {
	for _, port := range []string{"Success", "Fail", "Public IP", "Country code", "City", "Latitude", "Longitude"} {
		link := h.addLink(h.port(fetch, "right", port), h.port(sink, "left", port))
		if fetch == h.fetch && sink == h.sinkID {
			h.outputLinks[port] = link
		}
	}
}

func (h *fetchAddrHarness) addLink(first, second uint64) uint64 {
	h.t.Helper()
	link, _, err := h.w.AddLink(first, second)
	if err != nil {
		h.t.Fatalf("AddLink(%d, %d): %v", first, second, err)
	}
	return link
}

func (h *fetchAddrHarness) port(node uint64, direction, name string) uint64 {
	h.t.Helper()
	snapshot, ok := h.w.NodeSnapshot(node)
	if !ok {
		h.t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	ports := snapshot.LeftPorts
	if direction == "right" {
		ports = snapshot.RightPorts
	}
	for _, id := range ports {
		port, ok := h.w.PortSnapshot(id)
		if ok && port.Name == name && port.Direction == direction {
			return id
		}
	}
	h.t.Fatalf("node %d has no %s port %q", node, direction, name)
	return 0
}

func (h *fetchAddrHarness) fireInputTrigger() {
	h.w.EmitEvent(h.source, h.triggerLink, std.Trigger{})
}

func (h *fetchAddrHarness) pressCheck() { h.pressCheckFor(h.fetch) }

func (h *fetchAddrHarness) pressCheckFor(fetch uint64) {
	h.w.SendNodeFormularMsg(fetch, formular.ButtonPressMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(fetch)},
		BlockID:     "check", ButtonID: "check",
	})
}

func (h *fetchAddrHarness) selectProvider(provider string) {
	h.w.SendNodeFormularMsg(h.fetch, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(h.fetch)},
		Field:       formular.FieldRef{BlockID: "check", FieldID: "provider"}, Value: provider,
	})
}

func assertFetchAddrCapture(t *testing.T, got fetchAddrCapture, want fetchAddrData) {
	t.Helper()
	if got.data != want {
		t.Fatalf("captured data = %#v, want %#v", got.data, want)
	}
}

type fetchAddrTestRuntime struct {
	ctx     context.Context
	spawner *fetchAddrTestSpawner
}

func (r fetchAddrTestRuntime) Context() context.Context                     { return r.ctx }
func (fetchAddrTestRuntime) Pool() bufpool.Pool                             { return nil }
func (fetchAddrTestRuntime) System() sysnet.System                          { return nil }
func (fetchAddrTestRuntime) KVManager() storage.KVManager                   { return nil }
func (fetchAddrTestRuntime) DnsCacheManager() storage.DnsCacheManager       { return nil }
func (fetchAddrTestRuntime) SecretsStorage() storage.SecretsStorage         { return nil }
func (fetchAddrTestRuntime) TlsConfigStorage() storage.TlsConfigStorage     { return nil }
func (r fetchAddrTestRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner   { return r.spawner }
func (fetchAddrTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

type fetchAddrTestSpawner struct{ wg sync.WaitGroup }

func (s *fetchAddrTestSpawner) Spawn(worker func(), _ string) (uint64, error) {
	s.wg.Add(1)
	go func() { defer s.wg.Done(); worker() }()
	return 1, nil
}

func (s *fetchAddrTestSpawner) SpawnWg(worker func(), waitgroup *sync.WaitGroup, _ string) (uint64, error) {
	waitgroup.Add(1)
	go func() { defer waitgroup.Done(); worker() }()
	return 1, nil
}

func (s *fetchAddrTestSpawner) wait(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FetchAddr test workers did not stop")
	}
}

type fetchAddrTestNetworkClassDef struct{ network gonnect.Network }

func (fetchAddrTestNetworkClassDef) ClassName() string        { return fetchAddrTestNetworkClass }
func (fetchAddrTestNetworkClassDef) ShortDescription() string { return "test network" }
func (fetchAddrTestNetworkClassDef) LongDescription() string  { return "Provides a test network." }
func (fetchAddrTestNetworkClassDef) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{nodekit.NetworkPort("left", "Network")}}
}
func (c fetchAddrTestNetworkClassDef) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &fetchAddrTestNetworkNode{network: c.network}, nil
}

type fetchAddrTestNetworkNode struct {
	nodekit.SimpleNetworkProvider
	network gonnect.Network
}

func (n *fetchAddrTestNetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	return n.InitSimpleNetworkProvider(w, logger, id, restored, n.network, "test network")
}

type fetchAddrTestTriggerSourceClass struct{}

func (fetchAddrTestTriggerSourceClass) ClassName() string        { return fetchAddrTestSourceClass }
func (fetchAddrTestTriggerSourceClass) ShortDescription() string { return "test trigger source" }
func (fetchAddrTestTriggerSourceClass) LongDescription() string  { return "Emits test triggers." }
func (fetchAddrTestTriggerSourceClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{{Direction: "right", Name: "Trigger", Types: []string{std.TypeTrigger}}}}
}
func (fetchAddrTestTriggerSourceClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &fetchAddrTestTriggerSourceNode{}, nil
}

type fetchAddrTestTriggerSourceNode struct {
	pasta.BasicNode
	out uint64
}

func (n *fetchAddrTestTriggerSourceNode) OnInit(_ *pasta.Workspace, _ pasta.Logger, _ uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.out = nodekit.RestoredRight(restored, 0)
	return nil
}

func (n *fetchAddrTestTriggerSourceNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || linkType != std.TypeTrigger || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

type fetchAddrTestSinkRegistry struct {
	nodes map[uint64]*fetchAddrTestSink
}

type fetchAddrTestSinkClassDef struct{ registry *fetchAddrTestSinkRegistry }

func (fetchAddrTestSinkClassDef) ClassName() string        { return fetchAddrTestSinkClass }
func (fetchAddrTestSinkClassDef) ShortDescription() string { return "test FetchAddr sink" }
func (fetchAddrTestSinkClassDef) LongDescription() string {
	return "Captures FetchAddr values and triggers."
}
func (fetchAddrTestSinkClassDef) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{
		{Direction: "left", Name: "Success", Types: []string{std.TypeTrigger}},
		{Direction: "left", Name: "Fail", Types: []string{std.TypeTrigger}},
		{Direction: "left", Name: "Public IP", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Country code", Types: []string{std.TypeString}},
		{Direction: "left", Name: "City", Types: []string{std.TypeString}},
		{Direction: "left", Name: "Latitude", Types: []string{std.TypeFloat}},
		{Direction: "left", Name: "Longitude", Types: []string{std.TypeFloat}},
	}}
}
func (c fetchAddrTestSinkClassDef) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &fetchAddrTestSink{registry: c.registry, successEvents: make(chan fetchAddrCapture, 4), failEvents: make(chan fetchAddrCapture, 4), publicIPEvents: make(chan string, 16)}, nil
}

type fetchAddrTestSink struct {
	pasta.BasicNode

	mu             sync.Mutex
	registry       *fetchAddrTestSinkRegistry
	ports          map[uint64]string
	data           fetchAddrData
	successEvents  chan fetchAddrCapture
	failEvents     chan fetchAddrCapture
	publicIPEvents chan string
}

func (n *fetchAddrTestSink) OnInit(_ *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.ports = make(map[uint64]string)
	names := []string{"Success", "Fail", "Public IP", "Country code", "City", "Latitude", "Longitude"}
	for index, port := range restored.LeftPorts {
		n.ports[port] = names[index]
	}
	n.registry.nodes[id] = n
	return nil
}

func (n *fetchAddrTestSink) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if _, ok := n.ports[port]; !ok || portDirection != "left" {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *fetchAddrTestSink) OnEvent(event pasta.Event, _ string, _ []string, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch n.ports[event.ReceiverPort] {
	case "Public IP":
		if value, ok := std.StringFromPayload(event.Payload); ok {
			n.data.PublicIP = value
			n.publicIPEvents <- value
		}
	case "Country code":
		if value, ok := std.StringFromPayload(event.Payload); ok {
			n.data.CountryCode = value
		}
	case "City":
		if value, ok := std.StringFromPayload(event.Payload); ok {
			n.data.City = value
		}
	case "Latitude":
		if value, ok := event.Payload.(std.Float); ok {
			n.data.Latitude = float64(value)
		}
	case "Longitude":
		if value, ok := event.Payload.(std.Float); ok {
			n.data.Longitude = float64(value)
		}
	case "Success":
		n.successEvents <- fetchAddrCapture{data: n.data}
	case "Fail":
		n.failEvents <- fetchAddrCapture{data: n.data}
	}
	return nil
}

func (n *fetchAddrTestSink) success(t *testing.T) fetchAddrCapture {
	t.Helper()
	select {
	case value := <-n.successEvents:
		return value
	case <-time.After(time.Second):
		t.Fatal("FetchAddr success trigger not received")
		return fetchAddrCapture{}
	}
}

func (n *fetchAddrTestSink) failure(t *testing.T) fetchAddrCapture {
	t.Helper()
	select {
	case value := <-n.failEvents:
		return value
	case <-time.After(time.Second):
		t.Fatal("FetchAddr fail trigger not received")
		return fetchAddrCapture{}
	}
}

func (n *fetchAddrTestSink) assertNoTrigger(t *testing.T, duration time.Duration) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-n.successEvents:
		t.Fatal("unexpected additional FetchAddr success trigger")
	case <-n.failEvents:
		t.Fatal("unexpected FetchAddr fail trigger")
	case <-timer.C:
	}
}

func (n *fetchAddrTestSink) setPublicIP(value string) {
	n.mu.Lock()
	n.data.PublicIP = value
	n.mu.Unlock()
}

func (n *fetchAddrTestSink) waitPublicIP(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-n.publicIPEvents:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("Public IP %q not received", want)
		}
	}
}
