package tunnodes_test

import (
	"fmt"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	tunSplitterE2EClientAddr1 = "10.113.212.1"
	tunSplitterE2EClientAddr2 = "10.2.0.1"
	tunSplitterE2EServerAddr  = "10.210.148.1"
	tunSplitterE2ESlot1Resp   = "Server 1"
	tunSplitterE2ESlot2Resp   = "Server 2"
)

type tunSplitterVTunHTTPE2EFixture struct {
	*httpE2E

	splitter   uint64
	copy       [3]uint64
	client     [3]uint64
	server     uint64
	servers    [3]uint64
	serverVTun uint64
	clientVTun [3]uint64
	port       int
	ports      [3]int
}

func TestTunSplitterE2EVTunHTTPRouting(t *testing.T) {
	f := setupTunSplitterVTunHTTPE2E(t)
	f.assertClientRequestWorks(f.client[1])
	f.assertClientRequestWorks(f.client[2])
}

func TestTunSplitterE2EVTunHTTPPortRulesRouteToSlots(t *testing.T) {
	f := setupTunSplitterPortRulesE2E(t)
	f.assertPortRuleRequest(f.ports[1], tunSplitterE2ESlot1Resp)
	f.assertPortRuleRequest(f.ports[2], tunSplitterE2ESlot2Resp)
	f.assertServerLog(f.servers[1], "GET /")
	f.assertServerLog(f.servers[2], "GET /")
}

func TestTunSplitterE2ERoutesByReverseDNSCacheHostname(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	dnsE := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), manager)
	e := newHTTPE2EFromNodeE2E(dnsE.NodeE2E)
	const (
		storageName = "tun-splitter-routing-cache"
		client1Name = "splitter-client-one.test."
		client2Name = "splitter-client-two.test."
	)
	warmDNSCacheReverseLookupsForRouting(
		t,
		e.NodeE2E,
		"tun-splitter",
		storageName,
		fmt.Sprintf(
			"splitter-client-one.test A %s\nsplitter-client-two.test A %s",
			tunSplitterE2EClientAddr1,
			tunSplitterE2EClientAddr2,
		),
		dnsCacheWarmQuery{name: client1Name, wantA: tunSplitterE2EClientAddr1},
		dnsCacheWarmQuery{name: client2Name, wantA: tunSplitterE2EClientAddr2},
	)

	f := tunSplitterVTunHTTPE2EFixture{httpE2E: e, port: httpTestPort()}
	f.splitter = e.AddNode(NodeTypeTunSplitter, "splitter")
	f.copy[1] = e.AddNode(NodeTypeTunCopy, "copy-1")
	f.copy[2] = e.AddNode(NodeTypeTunCopy, "copy-2")
	f.clientVTun[1] = e.AddNode(NodeTypeVTun, "client-vtun-1")
	f.clientVTun[2] = e.AddNode(NodeTypeVTun, "client-vtun-2")
	f.serverVTun = e.AddNode(NodeTypeVTun, "server-vtun")
	f.server = e.addHTTPServerOnHost("server", tunSplitterE2EServerAddr, f.port)
	f.client[1] = e.addHTTPClientToURL("client-1", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))
	f.client[2] = e.addHTTPClientToURL("client-2", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))
	e.SubscribeMenus(f.splitter, f.clientVTun[1], f.clientVTun[2], f.serverVTun, f.server, f.client[1], f.client[2])
	e.waitClientRequestField(f.client[1], "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))
	e.waitClientRequestField(f.client[2], "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))

	e.Engine.SendNodeFormularMsg(E2EWorkspace, f.splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, f.splitter),
		BlockID:     tunSplitterConfigBlockID,
		Values: map[string]any{
			tunSplitterRulesField: fmt.Sprintf(
				"ADDR_S %s\nSLOT 1\nADDR_S %s\nSLOT 2",
				client1Name,
				client2Name,
			),
			tunSplitterDNSCacheStorageNameField: storageName,
		},
	})
	e.applyTunCopyVTunConfig(f.clientVTun[1], "client-vtun-1", tunSplitterE2EClientAddr1+"/24")
	e.applyTunCopyVTunConfig(f.clientVTun[2], "client-vtun-2", tunSplitterE2EClientAddr2+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "server-vtun", tunSplitterE2EServerAddr+"/24")

	e.connectTunByPortName(f.splitter, "left", "Tun", f.serverVTun, "right", "Tun")
	e.connectTunByPortName(f.copy[1], "left", "TunA", f.splitter, "right", "slot 1")
	e.connectTunByPortName(f.copy[1], "left", "TunB", f.clientVTun[1], "right", "Tun")
	e.connectTunByPortName(f.copy[2], "left", "TunA", f.splitter, "right", "slot 2")
	e.connectTunByPortName(f.copy[2], "left", "TunB", f.clientVTun[2], "right", "Tun")

	e.connectByPortName(f.clientVTun[1], "left", "Network", f.client[1], "right", "Network")
	e.connectByPortName(f.clientVTun[2], "left", "Network", f.client[2], "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.server, "right", "Network")

	e.WaitNodeLabel(f.splitter, "splitting")
	e.WaitNodeLabel(f.copy[1], "copying")
	e.WaitNodeLabel(f.copy[2], "copying")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", tunSplitterE2EServerAddr, f.port))
	e.assertTunSplitterPorts(f.splitter, []string{"slot 1", "slot 2", "slot 3"})
	f.assertClientRequestWorks(f.client[1])
	f.assertClientRequestWorks(f.client[2])
}

func TestTunSplitterE2EConfigSaveRestore(t *testing.T) {
	f := setupTunSplitterVTunHTTPE2E(t)
	splitterName := f.nodeName(f.splitter)
	copy1Name := f.nodeName(f.copy[1])
	copy2Name := f.nodeName(f.copy[2])
	clientVTun1Name := f.nodeName(f.clientVTun[1])
	clientVTun2Name := f.nodeName(f.clientVTun[2])
	serverVTunName := f.nodeName(f.serverVTun)

	cfg := configer.NewMemory(nil)
	if err := f.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got, err := cfg.Get(configer.Path{"nodes", splitterName, "rules"}); err != nil || got != f.splitterRules() {
		t.Fatalf("saved splitter rules = %q, %v; want %q", got, err, f.splitterRules())
	}
	if err := f.Engine.Close(); err != nil {
		t.Fatalf("close saved engine: %v", err)
	}

	restored := newNetworkRouterConfigE2E(t, cfg)
	splitter := restored.mustNodeByName(splitterName)
	copy1 := restored.mustNodeByName(copy1Name)
	copy2 := restored.mustNodeByName(copy2Name)
	clientVTun1 := restored.mustNodeByName(clientVTun1Name)
	clientVTun2 := restored.mustNodeByName(clientVTun2Name)
	serverVTun := restored.mustNodeByName(serverVTunName)
	restored.assertTunSplitterPorts(splitter, []string{"slot 1", "slot 2", "slot 3"})
	restored.assertNodesLinked(splitter, copy1)
	restored.assertNodesLinked(splitter, copy2)
	restored.assertNodesLinked(splitter, serverVTun)
	restored.mustTypedLink(TypeTun, copy1, "left", "TunA", splitter, "right", "slot 1")
	restored.mustTypedLink(TypeTun, copy2, "left", "TunA", splitter, "right", "slot 2")
	restored.mustTypedLink(TypeTun, splitter, "left", "Tun", serverVTun, "right", "Tun")
	restored.WaitNodeLabel(splitter, "splitting")
	restored.WaitNodeLabel(copy1, "copying")
	restored.WaitNodeLabel(copy2, "copying")
	restored.WaitNodeLabel(clientVTun1, "client-vtun-1")
	restored.WaitNodeLabel(clientVTun2, "client-vtun-2")
	restored.WaitNodeLabel(serverVTun, "server-vtun")
}

func TestTunSplitterE2ECopyPaste(t *testing.T) {
	f := setupTunSplitterVTunHTTPE2E(t)

	clip := f.Engine.Copy(E2EWorkspace, []uint64{
		f.splitter,
		f.copy[1],
		f.copy[2],
		f.clientVTun[1],
		f.clientVTun[2],
		f.serverVTun,
	})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := f.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 6 {
		t.Fatalf("Paste returned %d nodes (%v), want 6", len(pasted), pasted)
	}
	splitter := pasted[0]
	copy1 := pasted[1]
	copy2 := pasted[2]
	clientVTun1 := pasted[3]
	clientVTun2 := pasted[4]
	serverVTun := pasted[5]
	f.assertTunSplitterPorts(splitter, []string{"slot 1", "slot 2", "slot 3"})
	f.assertNodesLinked(splitter, copy1)
	f.assertNodesLinked(splitter, copy2)
	f.assertNodesLinked(splitter, serverVTun)

	port := httpTestPort()
	server := f.addHTTPServerOnHost("pasted-server", tunSplitterE2EServerAddr, port)
	client1 := f.addHTTPClientToURL("pasted-client-1", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, port))
	client2 := f.addHTTPClientToURL("pasted-client-2", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, port))
	f.SubscribeMenus(server, client1, client2)
	f.waitClientRequestField(client1, "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, port))
	f.waitClientRequestField(client2, "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, port))

	f.connectByPortName(clientVTun1, "left", "Network", client1, "right", "Network")
	f.connectByPortName(clientVTun2, "left", "Network", client2, "right", "Network")
	f.connectByPortName(serverVTun, "left", "Network", server, "right", "Network")
	f.waitServerListening(server, fmt.Sprintf("%s:%d", tunSplitterE2EServerAddr, port))
	f.assertClientRequestWorksWithResponse(client1, "Debug http server response")
	f.assertClientRequestWorksWithResponse(client2, "Debug http server response")
}

func setupTunSplitterVTunHTTPE2E(t *testing.T) tunSplitterVTunHTTPE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := tunSplitterVTunHTTPE2EFixture{httpE2E: e, port: httpTestPort()}
	f.splitter = e.AddNode(NodeTypeTunSplitter, "splitter")
	f.copy[1] = e.AddNode(NodeTypeTunCopy, "copy-1")
	f.copy[2] = e.AddNode(NodeTypeTunCopy, "copy-2")
	f.clientVTun[1] = e.AddNode(NodeTypeVTun, "client-vtun-1")
	f.clientVTun[2] = e.AddNode(NodeTypeVTun, "client-vtun-2")
	f.serverVTun = e.AddNode(NodeTypeVTun, "server-vtun")
	f.server = e.addHTTPServerOnHost("server", tunSplitterE2EServerAddr, f.port)
	f.client[1] = e.addHTTPClientToURL("client-1", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))
	f.client[2] = e.addHTTPClientToURL("client-2", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))
	e.SubscribeMenus(f.splitter, f.clientVTun[1], f.clientVTun[2], f.serverVTun, f.server, f.client[1], f.client[2])
	e.waitClientRequestField(f.client[1], "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))
	e.waitClientRequestField(f.client[2], "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.port))

	f.applySplitterRules()
	e.applyTunCopyVTunConfig(f.clientVTun[1], "client-vtun-1", tunSplitterE2EClientAddr1+"/24")
	e.applyTunCopyVTunConfig(f.clientVTun[2], "client-vtun-2", tunSplitterE2EClientAddr2+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "server-vtun", tunSplitterE2EServerAddr+"/24")

	e.connectTunByPortName(f.splitter, "left", "Tun", f.serverVTun, "right", "Tun")
	e.connectTunByPortName(f.copy[1], "left", "TunA", f.splitter, "right", "slot 1")
	e.connectTunByPortName(f.copy[1], "left", "TunB", f.clientVTun[1], "right", "Tun")
	e.connectTunByPortName(f.copy[2], "left", "TunA", f.splitter, "right", "slot 2")
	e.connectTunByPortName(f.copy[2], "left", "TunB", f.clientVTun[2], "right", "Tun")

	e.connectByPortName(f.clientVTun[1], "left", "Network", f.client[1], "right", "Network")
	e.connectByPortName(f.clientVTun[2], "left", "Network", f.client[2], "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.server, "right", "Network")

	e.WaitNodeLabel(f.splitter, "splitting")
	e.WaitNodeLabel(f.copy[1], "copying")
	e.WaitNodeLabel(f.copy[2], "copying")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", tunSplitterE2EServerAddr, f.port))
	e.assertTunSplitterPorts(f.splitter, []string{"slot 1", "slot 2", "slot 3"})
	return f
}

func setupTunSplitterPortRulesE2E(t *testing.T) tunSplitterVTunHTTPE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := tunSplitterVTunHTTPE2EFixture{httpE2E: e}
	f.ports[1] = httpTestPort()
	f.ports[2] = httpTestPort()
	f.splitter = e.AddNode(NodeTypeTunSplitter, "splitter")
	f.copy[1] = e.AddNode(NodeTypeTunCopy, "copy-1")
	f.copy[2] = e.AddNode(NodeTypeTunCopy, "copy-2")
	f.clientVTun[1] = e.AddNode(NodeTypeVTun, "client-vtun")
	f.serverVTun = e.AddNode(NodeTypeVTun, "server-vtun-1")
	f.clientVTun[2] = e.AddNode(NodeTypeVTun, "server-vtun-2")
	response1 := e.addStringConstant("server-1-response", tunSplitterE2ESlot1Resp)
	response2 := e.addStringConstant("server-2-response", tunSplitterE2ESlot2Resp)
	f.servers[1] = e.addHTTPServerOnHost("server-1", tunSplitterE2EServerAddr, f.ports[1])
	f.servers[2] = e.addHTTPServerOnHost("server-2", tunSplitterE2EServerAddr, f.ports[2])
	f.client[1] = e.addHTTPClientToURL("client", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.ports[1]))
	e.SubscribeMenus(f.splitter, f.copy[1], f.copy[2], f.clientVTun[1], f.serverVTun, f.clientVTun[2], response1, response2, f.servers[1], f.servers[2], f.client[1])
	e.waitClientRequestField(f.client[1], "url", fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, f.ports[1]))

	e.MustAddTypedLinkByPortName(std.TypeString, response1, "right", "output", f.servers[1], "left", "Response")
	e.MustAddTypedLinkByPortName(std.TypeString, response2, "right", "output", f.servers[2], "left", "Response")
	f.applyPortSplitterRules()
	e.applyTunCopyVTunConfig(f.clientVTun[1], "client-vtun", tunSplitterE2EClientAddr1+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "server-vtun-1", tunSplitterE2EServerAddr+"/24")
	e.applyTunCopyVTunConfig(f.clientVTun[2], "server-vtun-2", tunSplitterE2EServerAddr+"/24")

	e.connectTunByPortName(f.splitter, "left", "Tun", f.clientVTun[1], "right", "Tun")
	e.connectTunByPortName(f.copy[1], "left", "TunA", f.splitter, "right", "slot 1")
	e.connectTunByPortName(f.copy[1], "left", "TunB", f.serverVTun, "right", "Tun")
	e.connectTunByPortName(f.copy[2], "left", "TunA", f.splitter, "right", "slot 2")
	e.connectTunByPortName(f.copy[2], "left", "TunB", f.clientVTun[2], "right", "Tun")

	e.connectByPortName(f.clientVTun[1], "left", "Network", f.client[1], "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.servers[1], "right", "Network")
	e.connectByPortName(f.clientVTun[2], "left", "Network", f.servers[2], "right", "Network")

	e.WaitNodeLabel(f.splitter, "splitting")
	e.WaitNodeLabel(f.copy[1], "copying")
	e.WaitNodeLabel(f.copy[2], "copying")
	e.WaitNodeLabel(f.clientVTun[1], "client-vtun")
	e.WaitNodeLabel(f.serverVTun, "server-vtun-1")
	e.WaitNodeLabel(f.clientVTun[2], "server-vtun-2")
	e.waitServerListening(f.servers[1], fmt.Sprintf("%s:%d", tunSplitterE2EServerAddr, f.ports[1]))
	e.waitServerListening(f.servers[2], fmt.Sprintf("%s:%d", tunSplitterE2EServerAddr, f.ports[2]))
	e.assertTunSplitterPorts(f.splitter, []string{"slot 1", "slot 2", "slot 3"})
	return f
}

func (f tunSplitterVTunHTTPE2EFixture) splitterRules() string {
	return fmt.Sprintf("ADDR4 %s\nSLOT 1\nADDR4 %s\nSLOT 2", tunSplitterE2EClientAddr1, tunSplitterE2EClientAddr2)
}

func (f tunSplitterVTunHTTPE2EFixture) applySplitterRules() {
	f.T.Helper()
	f.Engine.SendNodeFormularMsg(E2EWorkspace, f.splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, f.splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": f.splitterRules()},
	})
}

func (f tunSplitterVTunHTTPE2EFixture) portSplitterRules() string {
	return fmt.Sprintf("PORT %d\nSLOT 1\n\nPORT %d\nSLOT 2", f.ports[1], f.ports[2])
}

func (f tunSplitterVTunHTTPE2EFixture) applyPortSplitterRules() {
	f.T.Helper()
	f.Engine.SendNodeFormularMsg(E2EWorkspace, f.splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, f.splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": f.portSplitterRules()},
	})
}

func (f tunSplitterVTunHTTPE2EFixture) assertClientRequestWorks(client uint64) {
	f.T.Helper()
	f.assertClientRequestWorksWithResponse(client, "Debug http server response")
}

func (f tunSplitterVTunHTTPE2EFixture) assertPortRuleRequest(port int, response string) {
	f.T.Helper()
	url := fmt.Sprintf("http://%s:%d/", tunSplitterE2EServerAddr, port)
	f.Engine.SendNodeFormularMsg(E2EWorkspace, f.client[1], formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, f.client[1]),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       url,
	})
	f.waitClientRequestField(f.client[1], "url", url)
	f.assertClientRequestWorksWithResponse(f.client[1], response)
}

func (e *httpE2E) assertTunSplitterPorts(splitter uint64, want []string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		got := e.tunSplitterPorts(splitter)
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}, "tun splitter %d ports %v", splitter, want)
}

func (e *httpE2E) tunSplitterPorts(splitter uint64) []string {
	e.T.Helper()
	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, splitter)
	if !ok {
		return nil
	}
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
		if ok {
			got = append(got, ps.Name)
		}
	}
	return got
}
