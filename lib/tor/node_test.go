package tornodes

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	tor "github.com/asciimoth/tor-driver"
)

func TestTorDriverStableClassIsRuntimeBound(t *testing.T) {
	logf := logging.NewDebugLogFactory(64)
	system := e2etest.NewDebugSystem()
	pool := e2etest.NewTestDebugPool(t)
	engine, err := almagest.BuildEngine(context.Background(), configer.NewMemory(nil), logf, system, nil, nil, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.NodeClassSnapshot(almagest.MainWorkspace, NodeTypeTorDriver); ok {
		t.Fatal("host-facing Tor class is registered without a host class")
	}
	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}

	test := newTorTestEngine(t)
	class, ok := test.e.Engine.NodeClassSnapshot(almagest.MainWorkspace, NodeTypeTorDriver)
	if !ok {
		t.Fatal("stable Tor class is missing")
	}
	if class.PrimaryType != links.TypeNetwork {
		t.Fatalf("class snapshot = %#v", class)
	}
}

func TestTorDriverLinksDisconnectReconnectAndFullLifecycle(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor")
	test.e.SubscribeMenus(torNode)
	driver := test.factory.waitDriver(t, 0)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor waits for outbound Network")

	out := test.e.AddNode("almagest/MockNetwork", "out")
	outLink := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "Tor becomes ready after outbound attach")

	// Bootstrap progress cannot recover a latched outbound gate. Only an
	// explicit attachment replacement can make the driver usable again.
	state := driver.outbound()
	driver.setOutboundAvailable(false)
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "error" }, "Tor reports the current outbound fault")
	driver.events <- tor.BootstrapEvent{Progress: 47, Stage: "loading_descriptors", Severity: tor.EventNotice}
	eventuallyTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, torStatusBlockID, "bootstrap")
		return test.nodeLabel(torNode) == "error" && ok && item.Type == formular.ItemProgressbar && item.Progress != nil && *item.Progress == 47
	}, "bootstrap progress preserves the latched connectivity fault")
	for _, hidden := range []string{"outboundGeneration", "outboundAttempts", "containment", "identity"} {
		if _, ok := test.e.MenuItem(torNode, torStatusBlockID, hidden); ok {
			t.Fatalf("implementation detail %q is visible in the node status", hidden)
		}
	}
	test.e.Engine.RemoveLink(almagest.MainWorkspace, outLink)
	outLink = test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "attachment replacement clears the latch")
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	time.Sleep(25 * time.Millisecond)
	if got := test.nodeLabel(torNode); got != "up" {
		t.Fatalf("stale pre-reconnect outbound event changed label to %q", got)
	}

	client := test.e.AddNode(httpnodes.NodeTypeHTTPClient, "client")
	outputLink := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, torNode, "left", "Network default", client, "right", "Network")
	if snapshot, ok := test.e.Engine.LinkSnapshot(almagest.MainWorkspace, outputLink); !ok || snapshot.Type != links.TypeNetwork {
		t.Fatalf("Tor output link = %#v, %v", snapshot, ok)
	}
	test.e.Engine.RemoveLink(almagest.MainWorkspace, outputLink)
	outputLink = test.e.MustAddTypedLinkByPortName(links.TypeNetwork, torNode, "left", "Network default", client, "right", "Network")
	if outputLink == 0 {
		t.Fatal("Tor output did not reconnect")
	}

	test.e.Engine.RemoveLink(almagest.MainWorkspace, outLink)
	eventuallyTor(t, func() bool { return !driver.outbound().Available && test.nodeLabel(torNode) == "waiting for Network" }, "Tor blocks after outbound detach")
	state = driver.outbound()
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	time.Sleep(25 * time.Millisecond)
	if got := test.nodeLabel(torNode); got != "waiting for Network" {
		t.Fatalf("stale outbound failure changed label to %q", got)
	}
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "Tor recovers after outbound reconnect")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("driver starts = %d, want 1 across outbound reconnect", got)
	}

	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	eventuallyTor(t, func() bool { return driver.closed.Load() }, "Tor driver closes with node")
}

func TestTorDriverRebuildsWhenFirstOutboundFollowsOfflineBootstrap(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-offline-bootstrap")
	test.e.SubscribeMenus(torNode)
	first := test.factory.waitDriver(t, 0)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor waits for outbound Network")

	first.events <- tor.BootstrapEvent{Progress: 4, Stage: "conn_done_proxy", Severity: tor.EventNotice}
	eventuallyTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, torStatusBlockID, "bootstrap")
		return ok && item.Progress != nil && *item.Progress == 4
	}, "offline bootstrap attempt reaches the node")

	out := test.e.AddNode("almagest/MockNetwork", "offline-bootstrap-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	second := test.factory.waitDriver(t, 1)
	if second == first {
		t.Fatal("offline Tor runtime was reused after the first outbound attachment")
	}
	eventuallyTor(t, func() bool { return first.closed.Load() }, "offline Tor runtime closes")
	if second.deps.Outbound == nil {
		t.Fatal("replacement Tor runtime did not receive the attached outbound Network at startup")
	}
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "replacement Tor runtime becomes ready")
}

func TestTorDriverPassesOwnerSandboxCapabilityToTorDriver(t *testing.T) {
	test := newTorTestEngine(t)
	test.e.AddNode(NodeTypeTorDriver, "sandbox-capability")
	driver := test.factory.waitDriver(t, 0)

	if driver.deps.Sandboxes != test.factory.sandboxes {
		t.Fatal("Tor driver did not receive the node owner sandbox capability")
	}
	if driver.deps.Clock == nil || driver.deps.Random == nil || driver.deps.Logger == nil {
		t.Fatalf("Tor dependencies are incomplete: %#v", driver.deps)
	}
	if driver.config.SandboxSlot != tor.DefaultSandboxSlot ||
		driver.config.SandboxTemplate != tor.DefaultSandboxTemplate ||
		driver.config.TorProgram != tor.DefaultTorProgram {
		t.Fatalf("Tor sandbox selection = %#v", driver.config)
	}
}

func TestTorDriverFailsClosedWhenSandboxCapabilityIsUnavailable(t *testing.T) {
	want := errors.New("sandbox unavailable for test")
	node := &torDriverNode{
		Base: nodekit.Base{},
		start: func(context.Context, tor.Config, tor.Dependencies) (torDriverBackend, error) {
			t.Fatal("driver started without a sandbox capability")
			return nil, nil
		},
		config: defaultTorDriverConfig(),
		sandboxes: func(context.Context) (sandbox.OwnerSandboxes, error) {
			return nil, want
		},
	}
	node.generation.Store(1)
	_, _, err := node.buildRuntime(context.Background(), 1, node.config, "tor")
	if !errors.Is(err, want) {
		t.Fatalf("buildRuntime error = %v, want %v", err, want)
	}
}

func TestTorDriverMenuConfigurationSecretsAndNodeFeatures(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-features")
	test.e.SubscribeMenus(torNode)
	test.factory.waitDriver(t, 0)

	for _, item := range []struct {
		block string
		field string
	}{
		{torSettingsBlockID, torFieldEnabled},
		{torNetworksBlockID, torFieldNetworks},
		{torServicesBlockID, torFieldServices},
		{torClientAuthBlockID, torFieldClientAuthorizations},
	} {
		if _, ok := test.e.MenuItem(torNode, item.block, item.field); !ok {
			t.Fatalf("rich Tor menu field %s/%s is missing", item.block, item.field)
		}
	}
	for _, block := range []string{torSettingsBlockID, torPoliciesBlockID, torNetworksBlockID, torBridgesBlockID, torServicesBlockID, torClientAuthBlockID} {
		if _, ok := test.e.MenuItem(torNode, block, torDriverConfigurationField); ok {
			t.Fatalf("raw Tor configuration editor is still present in %s", block)
		}
	}
	if item, ok := test.e.MenuItem(torNode, torNetworksBlockID, torFieldNetworks); !ok || item.Field == nil ||
		item.Kind != formular.FieldArray || len(item.Templates) != 1 || len(item.Templates[0].Items) != 4 {
		t.Fatalf("Tor Network editor = %#v, present %v", item, ok)
	}

	applyTorTestForm(t, test, torNode, torSettingsBlockID, map[string]any{
		torFieldEnabled: true, torFieldMaxProxyConnections: 17,
	}, 1)
	applyTorTestForm(t, test, torNode, torPoliciesBlockID, map[string]any{
		torFieldOutboundFailures: "latch",
		torFieldBandwidthRate:    "0", torFieldBandwidthBurst: "0", torFieldConnectionPadding: "reduced",
		torFieldCircuitPadding: "disabled", torFieldReachableORPorts: "", torFieldMaxPendingCircuits: 0,
		torFieldNumCPUs: 0, torFieldClientIP: "dual", torFieldOnionTraffic: "only",
		torFieldEntryNodes: "", torFieldExitNodes: "", torFieldExcludeNodes: "",
		torFieldExcludeExitNodes: "", torFieldStrictNodes: false,
	}, 2)
	applyTorTestForm(t, test, torNode, torNetworksBlockID, map[string]any{
		torFieldNetworks: []formular.ArrayElementValue{
			{ID: "network-session", Template: "network", Values: map[string]any{
				"name": "session", "circuits": "default", "isolateDestinationAddress": true, "isolateDestinationPort": true,
			}},
			{ID: "network-fresh", Template: "network", Values: map[string]any{
				"name": "fresh", "circuits": "connection", "isolateDestinationAddress": false, "isolateDestinationPort": false,
			}},
		},
	}, 3)
	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Test service", "ports": "80, 443", "maxStreams": 4, "maxStreamsPolicy": "close",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 4)
	applyTorTestForm(t, test, torNode, torClientAuthBlockID, map[string]any{
		torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization-client", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}},
	}, 5)
	driver := test.factory.waitDriver(t, 5)
	eventuallyTor(t, func() bool {
		return test.hasPort(torNode, "left", "Network session") && test.hasPort(torNode, "left", "Network fresh") && test.hasPort(torNode, "left", "Onion Test service")
	}, "configured Tor output ports")

	eventuallyTor(t, func() bool {
		driver.mu.Lock()
		defer driver.mu.Unlock()
		return len(driver.networkConfigs) == 2 && len(driver.serviceConfigs) == 1 && len(driver.clientAuth) == 1
	}, "all configured Tor resources are installed")
	driver.mu.Lock()
	driverConfig := driver.config
	networkConfigs := append([]tor.NetworkConfig(nil), driver.networkConfigs...)
	serviceConfigs := append([]tor.ServiceConfig(nil), driver.serviceConfigs...)
	service := driver.services[0]
	driver.mu.Unlock()
	if driverConfig.OutboundFailures != tor.LatchOutboundErrors || driverConfig.ConnectionPadding != tor.ReducedConnectionPadding ||
		driverConfig.CircuitPadding != tor.DisableCircuitPadding || driverConfig.ClientIP != tor.ClientDualStack ||
		driverConfig.OnionTraffic != tor.OnionTrafficOnly || driverConfig.MaxProxyConnections != 17 {
		t.Fatalf("driver config = %#v", driverConfig)
	}
	if networkConfigs[0].Isolation != tor.IsolateDestinationAddress|tor.IsolateDestinationPort || networkConfigs[1].Circuits != tor.IsolateEachConnection {
		t.Fatalf("network configs = %#v", networkConfigs)
	}
	if serviceConfigs[0].MaxStreamsPolicy != tor.CloseRendezvousCircuit || len(serviceConfigs[0].Ports) != 2 {
		t.Fatalf("service configs = %#v", serviceConfigs)
	}

	out := test.e.AddNode("almagest/MockNetwork", "feature-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "configured Tor node becomes ready")
	service.events <- tor.PublicationEvent{State: tor.PublicationPending}
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "publishing onion services" }, "service publication pending state")
	service.events <- tor.PublicationEvent{State: tor.PublicationPublished}
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "service publication ready state")

	cfg := configer.NewMemory(nil)
	if err := test.e.Engine.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var saved TorDriverConfig
	if err := configer.Unmarshal(cfg.View(configer.Path{"nodes", "tor-features", torDriverConfigurationField}), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Services[0].KeySecret == "" || saved.ClientAuthorizations[0].PrivateKeySecret == "" {
		t.Fatalf("generated secret selectors were not saved: %#v", saved)
	}
	if saved.ClientAuthorizations[0].PrivateKeySecret == saved.Services[0].KeySecret {
		t.Fatal("service and client authorization reused one secret")
	}
	if _, ok := test.secrets.SecretValue(saved.Services[0].KeySecret); !ok {
		t.Fatal("onion service key was not stored in secrets")
	}
	if _, ok := test.secrets.SecretValue(saved.ClientAuthorizations[0].PrivateKeySecret); !ok {
		t.Fatal("client authorization key was not stored in secrets")
	}

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.ButtonPressMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torControlBlockID,
		ButtonID:    torRestartButtonID,
	})
	test.factory.waitDriver(t, 6)
	eventuallyTor(t, func() bool { return test.factory.driverClosed(5) }, "menu restart closes old driver")
}

func TestTorDriverGeneratedSelectorsNotifyDurableConfigChanges(t *testing.T) {
	changes := make(chan uint64, 4)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		changes <- node
		return completedTorConfigPersistence(nil)
	}))
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-config-notify")
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Persistent service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	assertTorConfigChange(t, changes, torNode, "onion service selector")

	applyTorTestForm(t, test, torNode, torClientAuthBlockID, map[string]any{
		torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization-client", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}},
	}, 2)
	assertTorConfigChange(t, changes, torNode, "client authorization selector")

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	test.factory.waitDriver(t, 3)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) != "starting" }, "Tor restart completes")
	select {
	case node := <-changes:
		t.Fatalf("restart with saved selectors reported configuration change for node %d", node)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTorDriverRenamedSelectorsNotifyPersistAndRemainUsable(t *testing.T) {
	changes := make(chan uint64, 8)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		changes <- node
		return completedTorConfigPersistence(nil)
	}))
	const nodeName = "tor-secret-rename"
	torNode := test.e.AddNode(NodeTypeTorDriver, nodeName)
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Persistent service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	assertTorConfigChange(t, changes, torNode, "generated onion service selector")
	applyTorTestForm(t, test, torNode, torClientAuthBlockID, map[string]any{
		torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization-client", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}},
	}, 2)
	assertTorConfigChange(t, changes, torNode, "generated client authorization selector")

	initial := savedTorConfig(t, test, torNode)
	serviceOld := initial.Services[0].KeySecret
	authorizationOld := initial.ClientAuthorizations[0].PrivateKeySecret
	serviceNew := serviceOld + "-renamed"
	authorizationNew := authorizationOld + "-renamed"

	renameTorTestSecret(t, test, nodeName, serviceOld, serviceNew)
	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: serviceOld,
			SecretNameNew:  serviceNew,
		},
	})
	assertTorConfigChange(t, changes, torNode, "renamed onion service selector")
	test.factory.waitDriver(t, 3)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor reloads the renamed onion service secret")
	afterServiceRename := savedTorConfig(t, test, torNode)
	if got := afterServiceRename.Services[0].KeySecret; got != serviceNew {
		t.Fatalf("saved service selector = %q, want %q", got, serviceNew)
	}
	if got := afterServiceRename.ClientAuthorizations[0].PrivateKeySecret; got != authorizationOld {
		t.Fatalf("service rename changed authorization selector to %q", got)
	}

	renameTorTestSecret(t, test, nodeName, authorizationOld, authorizationNew)
	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: authorizationOld,
			SecretNameNew:  authorizationNew,
		},
	})
	assertTorConfigChange(t, changes, torNode, "renamed client authorization selector")
	test.factory.waitDriver(t, 4)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor reloads the renamed client authorization secret")
	afterAuthorizationRename := savedTorConfig(t, test, torNode)
	if got := afterAuthorizationRename.Services[0].KeySecret; got != serviceNew {
		t.Fatalf("authorization rename changed service selector to %q", got)
	}
	if got := afterAuthorizationRename.ClientAuthorizations[0].PrivateKeySecret; got != authorizationNew {
		t.Fatalf("saved authorization selector = %q, want %q", got, authorizationNew)
	}

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	test.factory.waitDriver(t, 5)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor restarts with both persisted renamed selectors")
	assertNoTorConfigChange(t, changes, "restart with renamed selectors")
}

func TestTorDriverSecretValueChangesDoNotReportConfigChanges(t *testing.T) {
	changes := make(chan uint64, 4)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		changes <- node
		return completedTorConfigPersistence(nil)
	}))
	const nodeName = "tor-secret-value-change"
	torNode := test.e.AddNode(NodeTypeTorDriver, nodeName)
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Persistent service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	assertTorConfigChange(t, changes, torNode, "generated onion service selector")
	selector := savedTorConfig(t, test, torNode).Services[0].KeySecret

	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: selector,
			SecretNameNew:  selector,
		},
	})
	test.factory.waitDriver(t, 2)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor reloads a changed secret value")
	assertNoTorConfigChange(t, changes, "secret value change")
	if got := savedTorConfig(t, test, torNode).Services[0].KeySecret; got != selector {
		t.Fatalf("secret value change changed saved selector to %q", got)
	}

	before := test.factory.count()
	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: "unrelated-old",
			SecretNameNew:  "unrelated-new",
		},
	})
	time.Sleep(50 * time.Millisecond)
	if got := test.factory.count(); got != before {
		t.Fatalf("unrelated secret change started %d drivers, want %d", got, before)
	}
	assertNoTorConfigChange(t, changes, "unrelated secret change")
}

func TestTorDriverDiscardsServiceSecretFromCanceledGeneration(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-service-cancel")
	test.factory.waitDriver(t, 0)

	stored := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	test.factory.mu.Lock()
	test.factory.afterServiceSecret = func() {
		once.Do(func() {
			close(stored)
			<-release
		})
	}
	test.factory.mu.Unlock()

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torServicesBlockID,
		Values: map[string]any{torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}}},
	})
	waitTorSignal(t, stored, "service secret creation")
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("secrets during canceled service build = %d, want 1", got)
	}
	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	close(release)
	test.factory.waitDriver(t, 2)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) != "starting" && test.secrets.SecretCount() == 1 }, "canceled service secret is replaced without leaking")
	assertTorSavedSelectors(t, test, torNode, true, false)
}

func TestTorDriverDiscardsClientAuthorizationSecretFromCanceledGeneration(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-client-auth-cancel")
	test.factory.waitDriver(t, 0)

	stored := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	test.factory.mu.Lock()
	test.factory.beforeAddClientAuth = func() {
		once.Do(func() {
			close(stored)
			<-release
		})
	}
	test.factory.mu.Unlock()

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torClientAuthBlockID,
		Values: map[string]any{torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}}},
	})
	waitTorSignal(t, stored, "client authorization secret creation")
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("secrets during canceled authorization build = %d, want 1", got)
	}
	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	close(release)
	test.factory.waitDriver(t, 2)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) != "starting" && test.secrets.SecretCount() == 1 }, "canceled authorization secret is replaced without leaking")
	assertTorSavedSelectors(t, test, torNode, false, true)
}

func TestTorDriverDiscardsUncommittedSecretWhenNodeStops(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-secret-stop")
	test.factory.waitDriver(t, 0)

	stored := make(chan struct{})
	release := make(chan struct{})
	test.factory.mu.Lock()
	test.factory.afterServiceSecret = func() {
		close(stored)
		<-release
	}
	test.factory.mu.Unlock()
	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torServicesBlockID,
		Values: map[string]any{torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}}},
	})
	waitTorSignal(t, stored, "service secret creation before stop")
	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	close(release)
	eventuallyTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "node stop discards uncommitted service secret")
}

func TestTorDriverDiscardsGeneratedSecretsWhenRuntimeBuildFails(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeTorFactory)
		apply     func(*testing.T, *torTestEngine, uint64)
		assert    func(*testing.T, TorDriverConfig)
	}{
		{
			name: "onion service",
			configure: func(factory *fakeTorFactory) {
				factory.newServiceError = errors.New("injected service creation failure")
			},
			apply: func(t *testing.T, test *torTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     torServicesBlockID,
					Values: map[string]any{torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
						"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config TorDriverConfig) {
				if len(config.Services) != 1 || config.Services[0].KeySecret != "" {
					t.Fatalf("failed service build saved selectors: %#v", config.Services)
				}
			},
		},
		{
			name: "client authorization",
			configure: func(factory *fakeTorFactory) {
				factory.addClientAuthError = errors.New("injected client authorization failure")
			},
			apply: func(t *testing.T, test *torTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     torClientAuthBlockID,
					Values: map[string]any{torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
						"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config TorDriverConfig) {
				if len(config.ClientAuthorizations) != 1 || config.ClientAuthorizations[0].PrivateKeySecret != "" {
					t.Fatalf("failed authorization build saved selectors: %#v", config.ClientAuthorizations)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newTorTestEngine(t)
			torNode := test.e.AddNode(NodeTypeTorDriver, "tor-failed-secret-"+strings.ReplaceAll(testCase.name, " ", "-"))
			test.factory.waitDriver(t, 0)
			test.factory.mu.Lock()
			testCase.configure(test.factory)
			test.factory.mu.Unlock()
			testCase.apply(t, test, torNode)
			test.factory.waitDriver(t, 1)
			eventuallyTor(t, func() bool {
				return test.nodeLabel(torNode) == "error" && test.secrets.SecretCount() == 0
			}, "failed build discards its generated secret")
			testCase.assert(t, savedTorConfig(t, test, torNode))
		})
	}
}

func TestTorDriverRestartsSecretGenerationAfterNodeRename(t *testing.T) {
	tests := []struct {
		name    string
		setHook func(*fakeTorFactory, func())
		apply   func(*testing.T, *torTestEngine, uint64)
		assert  func(*testing.T, TorDriverConfig)
	}{
		{
			name: "onion service",
			setHook: func(factory *fakeTorFactory, hook func()) {
				factory.afterServiceSecret = hook
			},
			apply: func(t *testing.T, test *torTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     torServicesBlockID,
					Values: map[string]any{torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
						"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config TorDriverConfig) {
				if len(config.Services) != 1 || config.Services[0].KeySecret == "" {
					t.Fatalf("renamed service selector was not committed: %#v", config.Services)
				}
			},
		},
		{
			name: "client authorization",
			setHook: func(factory *fakeTorFactory, hook func()) {
				factory.beforeAddClientAuth = hook
			},
			apply: func(t *testing.T, test *torTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     torClientAuthBlockID,
					Values: map[string]any{torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
						"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config TorDriverConfig) {
				if len(config.ClientAuthorizations) != 1 || config.ClientAuthorizations[0].PrivateKeySecret == "" {
					t.Fatalf("renamed authorization selector was not committed: %#v", config.ClientAuthorizations)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newTorTestEngine(t)
			oldName := "tor-rename-" + strings.ReplaceAll(testCase.name, " ", "-")
			newName := oldName + "-new"
			torNode := test.e.AddNode(NodeTypeTorDriver, oldName)
			test.factory.waitDriver(t, 0)

			stored := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			test.factory.mu.Lock()
			testCase.setHook(test.factory, func() {
				once.Do(func() {
					close(stored)
					<-release
				})
			})
			test.factory.mu.Unlock()
			testCase.apply(t, test, torNode)
			waitTorSignal(t, stored, "secret generation before rename")
			test.e.Engine.SetNodeName(almagest.MainWorkspace, torNode, newName)
			test.secrets.RenameNodeReferences(oldName, newName)
			close(release)

			test.factory.waitDriver(t, 2)
			eventuallyTor(t, func() bool {
				return test.nodeLabel(torNode) != "starting" && test.secrets.SecretCount() == 1
			}, "renamed node replaces the stale generated secret")
			testCase.assert(t, savedTorConfig(t, test, torNode))

			if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
				t.Fatal(err)
			}
			test.factory.waitDriver(t, 3)
			eventuallyTor(t, func() bool { return test.nodeLabel(torNode) != "error" && test.nodeLabel(torNode) != "starting" }, "renamed secret remains readable after restart")
			if got := test.secrets.SecretCount(); got != 1 {
				t.Fatalf("secrets after renamed restart = %d, want 1", got)
			}
		})
	}
}

func TestTorDriverMenuRejectsInvalidConfigurationWithoutRestart(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-invalid-menu")
	test.e.SubscribeMenus(torNode)
	test.factory.waitDriver(t, 0)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "initial Tor runtime starts")

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torNetworksBlockID,
		Values: map[string]any{torFieldNetworks: []formular.ArrayElementValue{{
			ID: "invalid-network", Template: "network", Values: map[string]any{
				"name": "invalid", "circuits": "connection", "isolateDestinationAddress": true, "isolateDestinationPort": false,
			},
		}}},
	})
	eventuallyTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, torNetworksBlockID, torFieldNetworks)
		return test.nodeLabel(torNode) == "error" && ok && item.Field != nil &&
			item.Status == formular.StatusError && strings.Contains(item.StatusText, "requires session circuits")
	}, "invalid Tor form reports an inline error")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("invalid form started %d Tor drivers, want 1", got)
	}

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torNetworksBlockID,
		Values: map[string]any{torFieldNetworks: []formular.ArrayElementValue{{
			ID: "default-network", Template: "network", Values: map[string]any{
				"name": "default", "circuits": "default", "isolateDestinationAddress": false, "isolateDestinationPort": false,
			},
		}}},
	})
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "corrected Tor form clears the error")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("unchanged corrected form started %d Tor drivers, want 1", got)
	}
}

func TestTorDriverMenuRejectsDriverInvalidPolicyWithoutRestart(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-invalid-policy")
	test.e.SubscribeMenus(torNode)
	test.factory.waitDriver(t, 0)

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     torPoliciesBlockID,
		Values: map[string]any{
			torFieldOutboundFailures: "default",
			torFieldBandwidthRate:    "0", torFieldBandwidthBurst: "0",
			torFieldConnectionPadding: "default", torFieldCircuitPadding: "default",
			torFieldReachableORPorts: "", torFieldMaxPendingCircuits: 1025,
			torFieldNumCPUs: 0, torFieldClientIP: "default", torFieldOnionTraffic: "default",
			torFieldEntryNodes: "", torFieldExitNodes: "", torFieldExcludeNodes: "",
			torFieldExcludeExitNodes: "", torFieldStrictNodes: false,
		},
	})
	eventuallyTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, torPoliciesBlockID, torFieldMaxPendingCircuits)
		return test.nodeLabel(torNode) == "error" && ok && item.Field != nil &&
			item.Status == formular.StatusError && strings.Contains(item.StatusText, "between 0 and 1024")
	}, "driver-invalid policy reports an inline error")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("driver-invalid policy started %d Tor drivers, want 1", got)
	}
	if saved := savedTorConfig(t, test, torNode); saved.Driver.MaxPendingCircuits != 0 {
		t.Fatalf("rejected pending circuit limit was saved: %d", saved.Driver.MaxPendingCircuits)
	}
}

func TestTorDriverRejectsGeneratedSecretsWithoutDurableConfigPersistence(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		block   string
		values  map[string]any
		message string
	}{
		{
			name:  "onion service",
			block: torServicesBlockID,
			values: map[string]any{torFieldServices: []formular.ArrayElementValue{{
				ID: "service", Template: "service", Values: map[string]any{
					"name": "service", "ports": "80", "maxStreams": 0,
					"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
				},
			}}},
			message: "generated secrets require durable node configuration persistence",
		},
		{
			name:  "client authorization",
			block: torClientAuthBlockID,
			values: map[string]any{torFieldClientAuthorizations: []formular.ArrayElementValue{{
				ID: "authorization", Template: "authorization", Values: map[string]any{
					"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
				},
			}}},
			message: "generated secrets require durable node configuration persistence",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(nil))
			torNode := test.e.AddNode(NodeTypeTorDriver, "tor-read-only-"+strings.ReplaceAll(testCase.name, " ", "-"))
			test.e.SubscribeMenus(torNode)
			test.factory.waitDriver(t, 0)

			test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
				MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
				BlockID:     testCase.block,
				Values:      testCase.values,
			})
			eventuallyTor(t, func() bool {
				return test.nodeLabel(torNode) == "error"
			}, "read-only generated secret configuration is rejected")
			if got := test.factory.count(); got != 1 {
				t.Fatalf("rejected generated secret started %d Tor drivers, want 1", got)
			}
			if got := test.secrets.SecretCount(); got != 0 {
				t.Fatalf("rejected generated secret stored %d secrets, want 0", got)
			}
			itemID := torFieldServices
			if testCase.block == torClientAuthBlockID {
				itemID = torFieldClientAuthorizations
			}
			eventuallyTor(t, func() bool {
				item, ok := test.e.MenuItem(torNode, testCase.block, itemID)
				return ok && item.Field != nil && item.Status == formular.StatusError &&
					strings.Contains(item.StatusText, testCase.message)
			}, "read-only form displays the persistence error")
			saved := savedTorConfig(t, test, torNode)
			if len(saved.Services) != 0 || len(saved.ClientAuthorizations) != 0 {
				t.Fatalf("rejected generated secret configuration was saved: %#v", saved)
			}
		})
	}
}

type torPersistenceRequest struct {
	node   uint64
	result chan error
}

func TestTorDriverKeepsGeneratedSecretPendingUntilDurableAcknowledgement(t *testing.T) {
	requests := make(chan torPersistenceRequest, 4)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-persistence-pending")
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	if request.node != torNode {
		t.Fatalf("persistence node = %d, want %d", request.node, torNode)
	}
	selector := savedTorConfig(t, test, torNode).Services[0].KeySecret
	if selector == "" || test.secrets.SecretCount() != 1 {
		t.Fatalf("pending generated selector = %q, secrets = %d", selector, test.secrets.SecretCount())
	}

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	test.factory.waitDriver(t, 2)
	eventuallyTor(t, func() bool {
		saved := savedTorConfig(t, test, torNode)
		return len(saved.Services) == 1 && saved.Services[0].KeySecret == selector && test.secrets.SecretCount() == 1
	}, "restart reuses the unacknowledged generated secret")
	select {
	case unexpected := <-requests:
		t.Fatalf("restart generated another persistence request: %#v", unexpected)
	case <-time.After(50 * time.Millisecond):
	}

	request.result <- nil
	close(request.result)
	time.Sleep(25 * time.Millisecond)
	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	eventuallyTor(t, func() bool { return test.secrets.SecretCount() == 1 }, "durably acknowledged secret survives node stop")
}

func TestTorDriverRollsBackGeneratedSecretAfterPersistenceFailure(t *testing.T) {
	requests := make(chan torPersistenceRequest, 1)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-persistence-failure")
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	request.result <- errors.New("durable write failed")
	close(request.result)

	eventuallyTor(t, func() bool {
		saved := savedTorConfig(t, test, torNode)
		return test.nodeLabel(torNode) == "error" && len(saved.Services) == 1 &&
			saved.Services[0].KeySecret == "" && test.secrets.SecretCount() == 0 &&
			test.factory.driverClosed(1)
	}, "persistence failure rolls back selector, secret, and runtime")
}

func TestTorDriverDeletesPendingSecretRemovedBeforePersistenceCompletes(t *testing.T) {
	requests := make(chan torPersistenceRequest, 1)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-persistence-removed")
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{torFieldServices: []formular.ArrayElementValue{}}, 2)
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("pending secret count before acknowledgement = %d, want 1", got)
	}
	request.result <- nil
	close(request.result)
	eventuallyTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "removed selector discards acknowledged obsolete secret")
}

func TestTorDriverIgnoresObsoletePersistenceFailureAfterSecretRemoval(t *testing.T) {
	requests := make(chan torPersistenceRequest, 1)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-obsolete-persistence-failure")
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{torFieldServices: []formular.ArrayElementValue{}}, 2)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "runtime without removed service becomes active")

	request.result <- errors.New("obsolete durable write failed")
	close(request.result)
	eventuallyTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "obsolete generated secret is deleted")
	time.Sleep(25 * time.Millisecond)
	if got := test.nodeLabel(torNode); got != "waiting for Network" {
		t.Fatalf("obsolete persistence failure changed the current runtime label to %q", got)
	}
	if test.factory.driverClosed(2) {
		t.Fatal("obsolete persistence failure closed the current Tor runtime")
	}
}

func TestTorDriverDeletesUnacknowledgedSecretWhenNodeStops(t *testing.T) {
	requests := make(chan torPersistenceRequest, 1)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-persistence-stop")
	test.factory.waitDriver(t, 0)

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	eventuallyTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "node stop discards unacknowledged secret")
	request.result <- nil
	close(request.result)
	time.Sleep(25 * time.Millisecond)
	if got := test.secrets.SecretCount(); got != 0 {
		t.Fatalf("late acknowledgement restored %d secrets", got)
	}
}

func TestTorDriverRecoversPendingOnionKeyAfterProcessCrash(t *testing.T) {
	requests := make(chan torPersistenceRequest, 1)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	const nodeName = "tor-recover-onion-key"
	torNode := test.e.AddNode(NodeTypeTorDriver, nodeName)
	test.factory.waitDriver(t, 0)

	key, err := tor.OnionServiceKeyFromEd25519Seed(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	text, err := key.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	selector, err := test.secrets.NewGeneratedSecret(
		storage.Secret{Value: string(text)}, nodeName,
		torServiceSecretOwner(torServiceKeyName("service")),
	)
	clear(text)
	if err != nil {
		t.Fatal(err)
	}

	applyTorTestForm(t, test, torNode, torServicesBlockID, map[string]any{
		torFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	saved := savedTorConfig(t, test, torNode)
	if got := saved.Services[0].KeySecret; got != selector {
		t.Fatalf("recovered service selector = %q, want %q", got, selector)
	}
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("recovery stored %d secrets, want 1", got)
	}
	request.result <- nil
	close(request.result)
	eventuallyTor(t, func() bool {
		key, _, recoverErr := test.secrets.RecoverGeneratedSecret(torServiceSecretOwner(torServiceKeyName("service")), nodeName)
		return recoverErr == nil && key == ""
	}, "durable service selector clears crash-recovery metadata")
}

func TestTorDriverRecoversPendingClientAuthorizationAfterProcessCrash(t *testing.T) {
	requests := make(chan torPersistenceRequest, 1)
	test := newTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- torPersistenceRequest{node: node, result: result}
		return result
	}))
	const nodeName = "tor-recover-client-authorization"
	torNode := test.e.AddNode(NodeTypeTorDriver, nodeName)
	test.factory.waitDriver(t, 0)
	address := testOnionAddress()
	privateKey, err := tor.NewClientAuthorizationPrivateKey(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	text, err := privateKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	selector, err := test.secrets.NewGeneratedSecret(
		storage.Secret{Value: string(text)}, nodeName,
		torClientAuthorizationSecretOwner(address),
	)
	clear(text)
	if err != nil {
		t.Fatal(err)
	}

	applyTorTestForm(t, test, torNode, torClientAuthBlockID, map[string]any{
		torFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
			"address": address, "name": "client", "privateKeySecret": "",
		}}},
	}, 1)
	request := waitTorPersistenceRequest(t, requests)
	saved := savedTorConfig(t, test, torNode)
	if got := saved.ClientAuthorizations[0].PrivateKeySecret; got != selector {
		t.Fatalf("recovered authorization selector = %q, want %q", got, selector)
	}
	driver := test.factory.waitDriver(t, 1)
	driver.mu.Lock()
	authorizations := append([]tor.ClientAuthorization(nil), driver.clientAuth...)
	driver.mu.Unlock()
	if len(authorizations) != 1 {
		t.Fatalf("installed client authorizations = %d, want 1", len(authorizations))
	}
	actualText, err := authorizations[0].PrivateKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	wantText, err := privateKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualText, wantText) {
		t.Fatal("driver did not receive the recovered client authorization key")
	}
	clear(actualText)
	clear(wantText)
	request.result <- nil
	close(request.result)
	eventuallyTor(t, func() bool {
		key, _, recoverErr := test.secrets.RecoverGeneratedSecret(torClientAuthorizationSecretOwner(address), nodeName)
		return recoverErr == nil && key == ""
	}, "durable client authorization selector clears crash-recovery metadata")
}

func waitTorPersistenceRequest(t *testing.T, requests <-chan torPersistenceRequest) torPersistenceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for node configuration persistence request")
		return torPersistenceRequest{}
	}
}

func TestTorDriverRestoreRequiresPersistenceOnlyForEmptySecretSelectors(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		config TorDriverConfig
		want   bool
	}{
		{
			name: "generated service key",
			config: TorDriverConfig{Enabled: true, Services: []TorDriverServiceConfig{{
				Name: "service", Ports: []uint16{80},
			}}},
			want: true,
		},
		{
			name: "generated client authorization",
			config: TorDriverConfig{Enabled: true, ClientAuthorizations: []TorDriverClientAuthConfig{{
				Address: testOnionAddress(), Name: "client",
			}}},
			want: true,
		},
		{
			name: "explicit selectors",
			config: TorDriverConfig{Enabled: true,
				Services:             []TorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, KeySecret: "service-key"}},
				ClientAuthorizations: []TorDriverClientAuthConfig{{Address: testOnionAddress(), Name: "client", PrivateKeySecret: "client-key"}},
			},
			want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := configer.NewMemory(nil)
			if err := configer.Marshal(cfg.View(configer.Path{torDriverConfigurationField}), testCase.config); err != nil {
				t.Fatal(err)
			}
			_, err := (TorDriverClass{}).NewNode(cfg)
			if testCase.want {
				if err == nil || !strings.Contains(err.Error(), "durable node configuration persistence is unavailable") {
					t.Fatalf("NewNode error = %v, want persistence error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewNode with explicit selectors: %v", err)
			}
		})
	}
}

func applyTorTestForm(t *testing.T, test *torTestEngine, node uint64, block string, values map[string]any, driverIndex int) {
	t.Helper()
	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     block,
		Values:      values,
	})
	test.factory.waitDriver(t, driverIndex)
	eventuallyTor(t, func() bool { return test.nodeLabel(node) != "starting" }, "Tor applies %s", block)
}

func TestTorDriverRejectsStaleReadinessAfterRestart(t *testing.T) {
	test := newTorTestEngine(t)
	test.factory.blockReadiness = true
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-race")
	test.e.SubscribeMenus(torNode)
	first := test.factory.waitDriver(t, 0)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "first Tor runtime starts")

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	second := test.factory.waitDriver(t, 1)
	if first == second {
		t.Fatal("restart reused old driver")
	}
	first.markReady()
	eventuallyTor(t, func() bool { return first.closed.Load() }, "old Tor driver closes")
	if got := test.nodeLabel(torNode); got == "up" {
		t.Fatal("stale readiness marked replacement runtime up")
	}
}

func TestTorDriverConvergesOutboundChangedDuringStartup(t *testing.T) {
	test := newTorTestEngine(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	test.factory.mu.Lock()
	test.factory.startEntered = entered
	test.factory.startRelease = release
	test.factory.mu.Unlock()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-startup-outbound")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Tor start did not reach the attachment race point")
	}
	out := test.e.AddNode("almagest/MockNetwork", "startup-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	close(release)

	driver := test.factory.waitDriver(t, 0)
	eventuallyTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "startup attachment converges")
	calls := driver.setOutboundCalls()
	if len(calls) != 1 || calls[0] == nil {
		t.Fatalf("SetOutbound calls = %#v, want one current non-nil attachment", calls)
	}
}

func TestTorDriverRearmsUnavailableInitialOutboundAfterRestart(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-initial-outbound")
	out := test.e.AddNode("almagest/MockNetwork", "initial-outbound")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "first Tor runtime becomes ready")

	test.factory.mu.Lock()
	test.factory.initialOutboundUnavailable = true
	test.factory.mu.Unlock()
	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	driver := test.factory.waitDriver(t, 1)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "replacement rearms its initial outbound attachment")
	if calls := driver.setOutboundCalls(); len(calls) == 0 || calls[0] == nil {
		t.Fatalf("SetOutbound calls = %#v, want explicit non-nil rearm", calls)
	}
}

func TestTorDriverIgnoresOldOutboundFailureWhileInputAttachIsPending(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-pending-outbound")
	test.e.SubscribeMenus(torNode)
	driver := test.factory.waitDriver(t, 0)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor waits for outbound Network")

	attachEntered := make(chan struct{})
	releaseAttach := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAttach) }) }
	defer release()
	var blockOnce sync.Once
	driver.setHook(func(network gonnect.Network) {
		if network == nil {
			return
		}
		blockOnce.Do(func() {
			close(attachEntered)
			<-releaseAttach
		})
	})
	out := test.e.AddNode("almagest/MockNetwork", "pending-outbound")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	select {
	case <-attachEntered:
	case <-time.After(time.Second):
		t.Fatal("SetOutbound did not reach the pending attachment")
	}

	state := driver.outbound()
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	driver.events <- tor.BootstrapEvent{Progress: 23, Stage: "handshake", Severity: tor.EventNotice}
	eventuallyTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, torStatusBlockID, "bootstrap")
		return ok && item.Progress != nil && *item.Progress == 23
	}, "old failure and marker events are processed")
	if got := test.nodeLabel(torNode); got == "error" {
		t.Fatal("old outbound failure latched the pending input attachment")
	}
	release()
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "pending attachment recovers")
}

func TestTorDriverSerializesRapidOutboundDetachAndReattach(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-rapid-outbound")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "rapid-out")
	link := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "initial attachment becomes ready")

	detachEntered := make(chan struct{})
	releaseDetach := make(chan struct{})
	var blockOnce sync.Once
	driver.setHook(func(network gonnect.Network) {
		if network != nil {
			return
		}
		blockOnce.Do(func() {
			close(detachEntered)
			<-releaseDetach
		})
	})
	test.e.Engine.RemoveLink(almagest.MainWorkspace, link)
	select {
	case <-detachEntered:
	case <-time.After(time.Second):
		t.Fatal("outbound detach did not start")
	}
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	close(releaseDetach)

	eventuallyTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "rapid reattach converges to the latest attachment")
	calls := driver.setOutboundCalls()
	if len(calls) < 3 || calls[len(calls)-2] != nil || calls[len(calls)-1] == nil {
		t.Fatalf("SetOutbound calls = %#v, want detach followed by current reattach", calls)
	}
}

func TestTorDriverLatchedFailureWithdrawsOutputsUntilReattach(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-latched-output")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "latched-out")
	outLink := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "Tor becomes ready")

	receiverNode := test.e.AddNode(torReceiverNodeType, "receiver")
	receiver := test.receivers.wait(t, 0)
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, torNode, "left", "Network default", receiverNode, "right", "Network")
	readyPayload := networktest.ReceivePayload(t, receiver.Events)
	if readyPayload.Network == nil {
		t.Fatal("Tor output was not published")
	}

	state := driver.outbound()
	driver.setOutboundAvailable(false)
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	waitTorUnavailablePayload(t, receiver.Events)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "error" }, "latched failure becomes an error")
	// Model readiness that completed before the failure but reached the node
	// after the failure event.
	for revision := uint64(0); revision < 4; revision++ {
		test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
			ReceiverNode: torNode,
			Payload: torOutboundResult{
				runtimeID: 1, outboundRevision: revision,
				state: tor.OutboundState{Available: true}, ready: true,
			},
		})
	}
	driver.events <- tor.BootstrapEvent{Progress: 100, Severity: tor.EventNotice}
	time.Sleep(25 * time.Millisecond)
	if got := test.nodeLabel(torNode); got != "error" {
		t.Fatalf("bootstrap changed latched label to %q", got)
	}
	networktest.AssertNoEvent(t, receiver.Events)

	test.e.Engine.RemoveLink(almagest.MainWorkspace, outLink)
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "reattach recovers latched output")
	if payload := networktest.ReceivePayload(t, receiver.Events); payload.Network == nil {
		t.Fatal("reattach did not republish Tor output")
	}
}

func TestTorDriverOutboundReplacementDoesNotCancelControlWait(t *testing.T) {
	test := newTorTestEngine(t)
	test.factory.blockReadiness = true
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-wait-replacement")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "wait-out")
	link := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return driver.waitReadyCalls.Load() > 0 }, "outbound readiness wait starts")

	test.e.Engine.RemoveLink(almagest.MainWorkspace, link)
	eventuallyTor(t, func() bool {
		calls := driver.setOutboundCalls()
		return len(calls) >= 2 && calls[len(calls)-1] == nil
	}, "outbound detach is applied while readiness remains blocked")
	if got := driver.waitReadyCanceled.Load(); got != 0 {
		t.Fatalf("readiness waits canceled during detach = %d, want 0", got)
	}
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return len(driver.setOutboundCalls()) >= 3 }, "replacement is applied while the old readiness wait remains active")
	if got := driver.waitReadyCanceled.Load(); got != 0 {
		t.Fatalf("readiness waits canceled during replacement = %d, want 0", got)
	}
	driver.markReady()
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "replacement becomes ready")
}

func TestTorDriverDoesNotReuseReadinessFromReplacedOutbound(t *testing.T) {
	test := newTorTestEngine(t)
	firstWaitStarted := make(chan struct{})
	releaseFirstWait := make(chan struct{})
	secondWaitStarted := make(chan struct{})
	releaseSecondWait := make(chan struct{})
	var firstOnce sync.Once
	var secondOnce sync.Once
	test.factory.mu.Lock()
	test.factory.waitReadyHook = func(ctx context.Context, call int32) error {
		switch call {
		case 1:
			firstOnce.Do(func() { close(firstWaitStarted) })
			select {
			case <-releaseFirstWait:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			secondOnce.Do(func() { close(secondWaitStarted) })
			select {
			case <-releaseSecondWait:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	test.factory.mu.Unlock()
	defer func() {
		firstOnce.Do(func() { close(firstWaitStarted) })
		secondOnce.Do(func() { close(secondWaitStarted) })
		select {
		case <-releaseFirstWait:
		default:
			close(releaseFirstWait)
		}
		select {
		case <-releaseSecondWait:
		default:
			close(releaseSecondWait)
		}
	}()

	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-readiness-revision")
	driver := test.factory.waitDriver(t, 0)
	firstOut := test.e.AddNode("almagest/MockNetwork", "readiness-first-out")
	link := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, firstOut, "left", "Network", torNode, "right", torOutboundPortName)
	waitTorSignal(t, firstWaitStarted, "first outbound readiness wait")

	test.e.Engine.RemoveLink(almagest.MainWorkspace, link)
	secondOut := test.e.AddNode("almagest/MockNetwork", "readiness-second-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, secondOut, "left", "Network", torNode, "right", torOutboundPortName)
	waitTorSignal(t, secondWaitStarted, "replacement outbound readiness wait")

	close(releaseFirstWait)
	time.Sleep(2 * torOutboundRetryDelay)
	if got := test.nodeLabel(torNode); got == "up" {
		t.Fatal("readiness from the replaced outbound marked the current attachment up")
	}
	close(releaseSecondWait)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "replacement readiness marks Tor up")
	if calls := driver.waitReadyCalls.Load(); calls < 2 {
		t.Fatalf("WaitReady calls = %d, want a separate call for each outbound revision", calls)
	}
}

func TestTorDriverTerminalFailureRejectsQueuedReadiness(t *testing.T) {
	test := newTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeTorDriver, "tor-terminal")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "terminal-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "Tor becomes ready before terminal failure")

	receiverNode := test.e.AddNode(torReceiverNodeType, "terminal-receiver")
	receiver := test.receivers.wait(t, 0)
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, torNode, "left", "Network default", receiverNode, "right", "Network")
	if payload := networktest.ReceivePayload(t, receiver.Events); payload.Network == nil {
		t.Fatal("Tor output was not published")
	}

	driver.events <- tor.TerminalEvent{Cause: tor.TerminalControlConnection}
	waitTorUnavailablePayload(t, receiver.Events)
	eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "error" }, "terminal failure becomes an error")

	// Model a readiness completion that was calculated before the terminal
	// event but entered the workspace inbox after it.
	for revision := uint64(0); revision < 4; revision++ {
		test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
			ReceiverNode: torNode,
			Payload: torOutboundResult{
				runtimeID: 1, outboundRevision: revision,
				state: tor.OutboundState{Available: true}, ready: true,
			},
		})
	}
	time.Sleep(2 * torOutboundRetryDelay)
	if got := test.nodeLabel(torNode); got != "error" {
		t.Fatalf("queued readiness changed terminal label to %q", got)
	}
	networktest.AssertNoEvent(t, receiver.Events)
}

func TestTorDriverRetriesRecoverableOutboundOperations(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeTorFactory)
		assert    func(*testing.T, *fakeTorDriver)
	}{
		{
			name: "attachment",
			configure: func(factory *fakeTorFactory) {
				factory.setOutboundErrors = []error{errors.New("temporary attachment failure")}
			},
			assert: func(t *testing.T, driver *fakeTorDriver) {
				if calls := driver.setOutboundCalls(); len(calls) < 2 {
					t.Fatalf("SetOutbound calls = %d, want an automatic retry", len(calls))
				}
			},
		},
		{
			name: "readiness",
			configure: func(factory *fakeTorFactory) {
				factory.waitReadyErrors = []error{errors.New("temporary readiness failure")}
			},
			assert: func(t *testing.T, driver *fakeTorDriver) {
				if calls := driver.waitReadyCalls.Load(); calls < 2 {
					t.Fatalf("WaitReady calls = %d, want an automatic retry", calls)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newTorTestEngine(t)
			test.factory.mu.Lock()
			testCase.configure(test.factory)
			test.factory.mu.Unlock()
			torNode := test.e.AddNode(NodeTypeTorDriver, "tor-retry-"+testCase.name)
			driver := test.factory.waitDriver(t, 0)
			out := test.e.AddNode("almagest/MockNetwork", "retry-out-"+testCase.name)
			test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", torOutboundPortName)
			eventuallyTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "Tor recovers without a graph change")
			testCase.assert(t, driver)
		})
	}
}

type torTestEngine struct {
	t         *testing.T
	e         *e2etest.NodeE2E
	factory   *fakeTorFactory
	secrets   *e2etest.SecretsStorage
	receivers *torReceiverClass
}

func newTorTestEngine(t *testing.T) *torTestEngine {
	return newTorTestEngineWithOptions(t)
}

func newTorTestEngineWithOptions(t *testing.T, options ...almagest.BuildEngineOption) *torTestEngine {
	t.Helper()
	factory := &fakeTorFactory{}
	secrets := e2etest.NewSecretsStorage()
	logf := logging.NewDebugLogFactory(1024)
	system := e2etest.NewDebugSystem()
	pool := e2etest.NewTestDebugPool(t)
	class := TorDriverClass{
		start: factory.start,
		sandboxes: func(context.Context) (sandbox.OwnerSandboxes, error) {
			return factory.sandboxes, nil
		},
	}
	receivers := &torReceiverClass{}
	buildOptions := append([]almagest.BuildEngineOption{
		almagest.WithNodeConfigChangeCallback(func(uint64) <-chan error {
			return completedTorConfigPersistence(nil)
		}),
	}, options...)
	engine, err := almagest.BuildEngine(context.Background(), configer.NewMemory(nil), logf, system, nil, nil, secrets, nil, []pasta.NodeClass{class, receivers}, pool, true, buildOptions...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return &torTestEngine{t: t, e: e2etest.NewNodeE2EFromEngine(t, engine, logf, system, secrets), factory: factory, secrets: secrets, receivers: receivers}
}

func completedTorConfigPersistence(err error) <-chan error {
	result := make(chan error, 1)
	result <- err
	close(result)
	return result
}

func (e *torTestEngine) nodeLabel(node uint64) string {
	snapshot, ok := e.e.Engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		return ""
	}
	return snapshot.Label
}

func (e *torTestEngine) hasPort(node uint64, direction, name string) bool {
	snapshot, ok := e.e.Engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		return false
	}
	ports := snapshot.LeftPorts
	if direction == "right" {
		ports = snapshot.RightPorts
	}
	for _, id := range ports {
		port, ok := e.e.Engine.PortSnapshot(almagest.MainWorkspace, id)
		if ok && port.Name == name && port.Direction == direction {
			return true
		}
	}
	return false
}

type fakeTorFactory struct {
	mu                         sync.Mutex
	drivers                    []*fakeTorDriver
	sandboxes                  fakeTorSandboxes
	blockReadiness             bool
	initialOutboundUnavailable bool
	startEntered               chan struct{}
	startRelease               chan struct{}
	startOnce                  sync.Once
	setOutboundHook            func(gonnect.Network)
	afterServiceSecret         func()
	beforeAddClientAuth        func()
	waitReadyErrors            []error
	waitReadyHook              func(context.Context, int32) error
	setOutboundErrors          []error
	newServiceError            error
	addClientAuthError         error
}

func (f *fakeTorFactory) start(ctx context.Context, config tor.Config, deps tor.Dependencies) (torDriverBackend, error) {
	driver := newFakeTorDriver(config, deps, !f.blockReadiness)
	f.mu.Lock()
	if f.initialOutboundUnavailable && deps.Outbound != nil {
		driver.state.Available = false
	}
	driver.setOutboundHook = f.setOutboundHook
	driver.afterServiceSecret = f.afterServiceSecret
	driver.beforeAddClientAuth = f.beforeAddClientAuth
	driver.waitReadyErrors = append([]error(nil), f.waitReadyErrors...)
	driver.waitReadyHook = f.waitReadyHook
	driver.setOutboundErrors = append([]error(nil), f.setOutboundErrors...)
	driver.newServiceError = f.newServiceError
	driver.addClientAuthError = f.addClientAuthError
	f.drivers = append(f.drivers, driver)
	entered := f.startEntered
	release := f.startRelease
	f.mu.Unlock()
	if entered != nil {
		f.startOnce.Do(func() { close(entered) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return driver, nil
}

func (f *fakeTorFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.drivers)
}

func (f *fakeTorFactory) waitDriver(t *testing.T, index int) *fakeTorDriver {
	t.Helper()
	var driver *fakeTorDriver
	eventuallyTor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if index < len(f.drivers) {
			driver = f.drivers[index]
		}
		return driver != nil
	}, "Tor driver %d starts", index)
	return driver
}

func (f *fakeTorFactory) driverClosed(index int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return index < len(f.drivers) && f.drivers[index].closed.Load()
}

type fakeTorSandboxes struct{}

func (fakeTorSandboxes) Acquire(context.Context, string, sandbox.TemplateID) (sandbox.Sandbox, error) {
	return nil, errors.New("unexpected fake sandbox acquisition")
}
func (fakeTorSandboxes) Remove(context.Context, string) error { return nil }

type fakeTorDriver struct {
	mu                  sync.Mutex
	config              tor.Config
	deps                tor.Dependencies
	state               tor.OutboundState
	networkConfigs      []tor.NetworkConfig
	serviceConfigs      []tor.ServiceConfig
	clientAuth          []tor.ClientAuthorization
	events              chan tor.DriverEvent
	done                chan struct{}
	ready               chan struct{}
	readyOnce           sync.Once
	closeOnce           sync.Once
	closed              atomic.Bool
	services            []*fakeTorService
	setOutboundHook     func(gonnect.Network)
	outboundHistory     []gonnect.Network
	afterServiceSecret  func()
	beforeAddClientAuth func()
	waitReadyErrors     []error
	waitReadyHook       func(context.Context, int32) error
	setOutboundErrors   []error
	newServiceError     error
	addClientAuthError  error
	waitReadyCalls      atomic.Int32
	waitReadyCanceled   atomic.Int32
}

func newFakeTorDriver(config tor.Config, deps tor.Dependencies, ready bool) *fakeTorDriver {
	d := &fakeTorDriver{config: config, deps: deps, events: make(chan tor.DriverEvent, 32), done: make(chan struct{}), ready: make(chan struct{})}
	d.state = tor.OutboundState{Available: deps.Outbound != nil, Generation: 1}
	if ready {
		d.markReady()
	}
	return d
}

func (d *fakeTorDriver) markReady() { d.readyOnce.Do(func() { close(d.ready) }) }
func (d *fakeTorDriver) WaitReady(ctx context.Context) error {
	call := d.waitReadyCalls.Add(1)
	d.mu.Lock()
	hook := d.waitReadyHook
	if len(d.waitReadyErrors) > 0 {
		err := d.waitReadyErrors[0]
		d.waitReadyErrors = d.waitReadyErrors[1:]
		d.mu.Unlock()
		return err
	}
	d.mu.Unlock()
	if hook != nil {
		return hook(ctx, call)
	}
	select {
	case <-d.ready:
		return nil
	case <-d.done:
		return tor.ErrClosed
	case <-ctx.Done():
		d.waitReadyCanceled.Add(1)
		return ctx.Err()
	}
}
func (d *fakeTorDriver) SubscribeEvents(int) (<-chan tor.DriverEvent, func(), error) {
	return d.events, func() {}, nil
}
func (d *fakeTorDriver) SetOutbound(network gonnect.Network) error {
	d.mu.Lock()
	hook := d.setOutboundHook
	d.mu.Unlock()
	if hook != nil {
		hook(network)
	}
	d.mu.Lock()
	var injected error
	if len(d.setOutboundErrors) > 0 {
		injected = d.setOutboundErrors[0]
		d.setOutboundErrors = d.setOutboundErrors[1:]
	}
	d.outboundHistory = append(d.outboundHistory, network)
	if injected != nil {
		d.state.Available = false
		d.state.Generation++
		d.mu.Unlock()
		return injected
	}
	d.state.Available = network != nil
	d.state.Generation++
	d.mu.Unlock()
	if network == nil {
		return errors.New("retire closed outbound session")
	}
	if network != nil {
		d.markReady()
	}
	return nil
}
func (d *fakeTorDriver) setOutboundCalls() []gonnect.Network {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]gonnect.Network(nil), d.outboundHistory...)
}
func (d *fakeTorDriver) setHook(hook func(gonnect.Network)) {
	d.mu.Lock()
	d.setOutboundHook = hook
	d.mu.Unlock()
}
func (d *fakeTorDriver) outbound() tor.OutboundState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}
func (d *fakeTorDriver) setOutboundAvailable(available bool) {
	d.mu.Lock()
	d.state.Available = available
	d.mu.Unlock()
}
func (d *fakeTorDriver) OutboundState() tor.OutboundState { return d.outbound() }
func (d *fakeTorDriver) NewNetwork(config tor.NetworkConfig) (torNetwork, error) {
	d.mu.Lock()
	d.networkConfigs = append(d.networkConfigs, config)
	d.mu.Unlock()
	return newFakeTorNetwork(), nil
}
func (d *fakeTorDriver) NewService(ctx context.Context, config tor.ServiceConfig) (torService, error) {
	if config.KeyName != "" && d.deps.OnionKeys != nil {
		_, err := d.deps.OnionKeys.Load(ctx, config.KeyName)
		if errors.Is(err, fs.ErrNotExist) {
			key, keyErr := tor.OnionServiceKeyFromEd25519Seed(make([]byte, 32))
			if keyErr != nil {
				return nil, keyErr
			}
			err = d.deps.OnionKeys.Store(ctx, config.KeyName, key)
		}
		if err != nil {
			return nil, err
		}
		if d.afterServiceSecret != nil {
			d.afterServiceSecret()
		}
	}
	if d.newServiceError != nil {
		return nil, d.newServiceError
	}
	service := newFakeTorService()
	d.mu.Lock()
	d.serviceConfigs = append(d.serviceConfigs, config)
	d.services = append(d.services, service)
	d.mu.Unlock()
	return service, nil
}
func (d *fakeTorDriver) GenerateClientAuthorization(name string) (tor.ClientAuthorization, error) {
	key, err := tor.NewClientAuthorizationPrivateKey(bytes.Repeat([]byte{1}, 32))
	return tor.ClientAuthorization{PrivateKey: key, Name: name}, err
}
func (d *fakeTorDriver) AddClientAuthorization(_ context.Context, _ string, authorization tor.ClientAuthorization) error {
	if d.beforeAddClientAuth != nil {
		d.beforeAddClientAuth()
	}
	if d.addClientAuthError != nil {
		return d.addClientAuthError
	}
	d.mu.Lock()
	d.clientAuth = append(d.clientAuth, authorization)
	d.mu.Unlock()
	return nil
}

func waitTorSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertTorSavedSelectors(t *testing.T, test *torTestEngine, node uint64, service, authorization bool) {
	t.Helper()
	saved := savedTorConfig(t, test, node)
	if service && (len(saved.Services) != 1 || saved.Services[0].KeySecret == "") {
		t.Fatalf("saved service selector = %#v", saved.Services)
	}
	if authorization && (len(saved.ClientAuthorizations) != 1 || saved.ClientAuthorizations[0].PrivateKeySecret == "") {
		t.Fatalf("saved client authorization selector = %#v", saved.ClientAuthorizations)
	}
}

func savedTorConfig(t *testing.T, test *torTestEngine, node uint64) TorDriverConfig {
	t.Helper()
	cfg := configer.NewMemory(nil)
	if err := test.e.Engine.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := test.e.Engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatal("Tor node is missing")
	}
	var saved TorDriverConfig
	if err := configer.Unmarshal(cfg.View(configer.Path{"nodes", snapshot.Name, torDriverConfigurationField}), &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}
func (d *fakeTorDriver) Done() <-chan struct{} { return d.done }
func (d *fakeTorDriver) Err() error            { return nil }
func (d *fakeTorDriver) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.done)
		close(d.events)
		d.mu.Lock()
		for _, service := range d.services {
			_ = service.Close()
		}
		d.mu.Unlock()
	})
	return nil
}

type fakeTorNetwork struct {
	gonnect.Network
	closed atomic.Bool
}

func newFakeTorNetwork() *fakeTorNetwork {
	return &fakeTorNetwork{Network: gonnect.NewLoopbackNetwork()}
}
func (n *fakeTorNetwork) Close() error { n.closed.Store(true); return nil }

type fakeTorService struct {
	*fakeTorNetwork
	events    chan tor.ServiceEvent
	closeOnce sync.Once
}

func newFakeTorService() *fakeTorService {
	events := make(chan tor.ServiceEvent, 2)
	events <- tor.PublicationEvent{State: tor.PublicationPublished}
	return &fakeTorService{fakeTorNetwork: newFakeTorNetwork(), events: events}
}
func (s *fakeTorService) Address() string { return testOnionAddress() }
func (s *fakeTorService) SubscribeEvents(int) (<-chan tor.ServiceEvent, func(), error) {
	return s.events, func() {}, nil
}
func (s *fakeTorService) Close() error {
	s.closeOnce.Do(func() {
		_ = s.fakeTorNetwork.Close()
		close(s.events)
	})
	return nil
}

func testOnionAddress() string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.onion"
}

func eventuallyTor(t *testing.T, condition func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(format, args...)
}

func assertTorConfigChange(t *testing.T, changes <-chan uint64, want uint64, description string) {
	t.Helper()
	select {
	case got := <-changes:
		if got != want {
			t.Fatalf("%s config change node = %d, want %d", description, got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s did not report a durable configuration change", description)
	}
}

func assertNoTorConfigChange(t *testing.T, changes <-chan uint64, description string) {
	t.Helper()
	select {
	case node := <-changes:
		t.Fatalf("%s reported a configuration change for node %d", description, node)
	case <-time.After(50 * time.Millisecond):
	}
}

func renameTorTestSecret(t *testing.T, test *torTestEngine, nodeName, previous, next string) {
	t.Helper()
	value, ok := test.secrets.SecretValue(previous)
	if !ok {
		t.Fatalf("secret %q is missing before rename", previous)
	}
	test.secrets.SetSecret(next, storage.Secret{Value: value}, nodeName)
	test.secrets.DeleteSecret(previous)
}

func waitTorUnavailablePayload(t *testing.T, events <-chan links.NetworkPayload) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case payload := <-events:
			if payload.Network == nil {
				return
			}
		case <-timer.C:
			t.Fatal("timed out waiting for unavailable Tor network payload")
		}
	}
}

const torReceiverNodeType = "test/TorNetworkReceiver"

type torReceiverClass struct {
	mu        sync.Mutex
	receivers []*networktest.Receiver
}

func (*torReceiverClass) ClassName() string        { return torReceiverNodeType }
func (*torReceiverClass) ShortDescription() string { return "Tor test receiver" }
func (*torReceiverClass) LongDescription() string  { return "Receives Tor network payloads." }
func (*torReceiverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeNetwork}
}
func (c *torReceiverClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	receiver := networktest.NewReceiver(32)
	c.mu.Lock()
	c.receivers = append(c.receivers, receiver)
	c.mu.Unlock()
	return receiver, nil
}

func (c *torReceiverClass) wait(t *testing.T, index int) *networktest.Receiver {
	t.Helper()
	var receiver *networktest.Receiver
	eventuallyTor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if index < len(c.receivers) {
			receiver = c.receivers[index]
		}
		return receiver != nil
	}, "Tor network receiver %d starts", index)
	return receiver
}
