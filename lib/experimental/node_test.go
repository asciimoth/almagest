package experimentalnodes

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
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	tor "github.com/asciimoth/tor-driver"
	"github.com/asciimoth/tor-driver/direct"
)

func TestXTorDriverIsOptInAndRuntimeBound(t *testing.T) {
	logf := logging.NewDebugLogFactory(64)
	system := e2etest.NewDebugSystem()
	pool := e2etest.NewTestDebugPool(t)
	engine, err := almagest.BuildEngine(context.Background(), configer.NewMemory(nil), logf, system, nil, nil, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.NodeClassSnapshot(almagest.MainWorkspace, NodeTypeXTorDriver); ok {
		t.Fatal("experimental Tor class is registered without an extra class")
	}
	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}

	test := newXTorTestEngine(t)
	class, ok := test.e.Engine.NodeClassSnapshot(almagest.MainWorkspace, NodeTypeXTorDriver)
	if !ok {
		t.Fatal("explicit experimental Tor class is missing")
	}
	if class.PrimaryType != links.TypeNetwork {
		t.Fatalf("class snapshot = %#v", class)
	}
}

func TestXTorDriverLinksDisconnectReconnectAndFullLifecycle(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor")
	test.e.SubscribeMenus(torNode)
	driver := test.factory.waitDriver(t, 0)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor waits for outbound Network")

	out := test.e.AddNode("almagest/MockNetwork", "out")
	outLink := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "Tor becomes ready after outbound attach")

	// Bootstrap progress cannot recover a latched outbound gate. Only an
	// explicit attachment replacement can make the driver usable again.
	state := driver.outbound()
	driver.setOutboundAvailable(false)
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "error" }, "Tor reports the current outbound fault")
	driver.events <- tor.BootstrapEvent{Progress: 47, Stage: "loading_descriptors", Severity: tor.EventNotice}
	eventuallyXTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, xTorStatusBlockID, "bootstrap")
		return test.nodeLabel(torNode) == "error" && ok && item.Type == formular.ItemProgressbar && item.Progress != nil && *item.Progress == 47
	}, "bootstrap progress preserves the latched connectivity fault")
	for _, hidden := range []string{"outboundGeneration", "outboundAttempts", "containment", "identity"} {
		if _, ok := test.e.MenuItem(torNode, xTorStatusBlockID, hidden); ok {
			t.Fatalf("implementation detail %q is visible in the node status", hidden)
		}
	}
	test.e.Engine.RemoveLink(almagest.MainWorkspace, outLink)
	outLink = test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "attachment replacement clears the latch")
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
	eventuallyXTor(t, func() bool { return !driver.outbound().Available && test.nodeLabel(torNode) == "waiting for Network" }, "Tor blocks after outbound detach")
	state = driver.outbound()
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	time.Sleep(25 * time.Millisecond)
	if got := test.nodeLabel(torNode); got != "waiting for Network" {
		t.Fatalf("stale outbound failure changed label to %q", got)
	}
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "Tor recovers after outbound reconnect")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("driver starts = %d, want 1 across outbound reconnect", got)
	}

	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	eventuallyXTor(t, func() bool { return driver.closed.Load() && test.factory.systemClosed(0) }, "Tor process and host adapter close with node")
}

func TestXTorDriverMenuConfigurationSecretsAndNodeFeatures(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-features")
	test.e.SubscribeMenus(torNode)
	test.factory.waitDriver(t, 0)

	for _, item := range []struct {
		block string
		field string
	}{
		{xTorSettingsBlockID, xTorFieldEnabled},
		{xTorNetworksBlockID, xTorFieldNetworks},
		{xTorServicesBlockID, xTorFieldServices},
		{xTorClientAuthBlockID, xTorFieldClientAuthorizations},
	} {
		if _, ok := test.e.MenuItem(torNode, item.block, item.field); !ok {
			t.Fatalf("rich Tor menu field %s/%s is missing", item.block, item.field)
		}
	}
	for _, block := range []string{xTorSettingsBlockID, xTorPoliciesBlockID, xTorNetworksBlockID, xTorBridgesBlockID, xTorServicesBlockID, xTorClientAuthBlockID} {
		if _, ok := test.e.MenuItem(torNode, block, xTorDriverConfigurationField); ok {
			t.Fatalf("raw Tor configuration editor is still present in %s", block)
		}
	}
	if item, ok := test.e.MenuItem(torNode, xTorNetworksBlockID, xTorFieldNetworks); !ok || item.Field == nil ||
		item.Kind != formular.FieldArray || len(item.Templates) != 1 || len(item.Templates[0].Items) != 4 {
		t.Fatalf("Tor Network editor = %#v, present %v", item, ok)
	}

	applyXTorTestForm(t, test, torNode, xTorSettingsBlockID, map[string]any{
		xTorFieldEnabled: true, xTorFieldMaxProxyConnections: 17,
	}, 1)
	applyXTorTestForm(t, test, torNode, xTorPoliciesBlockID, map[string]any{
		xTorFieldOutboundFailures: "latch",
		xTorFieldBandwidthRate:    "0", xTorFieldBandwidthBurst: "0", xTorFieldConnectionPadding: "reduced",
		xTorFieldCircuitPadding: "disabled", xTorFieldReachableORPorts: "", xTorFieldMaxPendingCircuits: 0,
		xTorFieldNumCPUs: 0, xTorFieldClientIP: "dual", xTorFieldOnionTraffic: "only",
		xTorFieldEntryNodes: "", xTorFieldExitNodes: "", xTorFieldExcludeNodes: "",
		xTorFieldExcludeExitNodes: "", xTorFieldStrictNodes: false,
	}, 2)
	applyXTorTestForm(t, test, torNode, xTorNetworksBlockID, map[string]any{
		xTorFieldNetworks: []formular.ArrayElementValue{
			{ID: "network-session", Template: "network", Values: map[string]any{
				"name": "session", "circuits": "default", "isolateDestinationAddress": true, "isolateDestinationPort": true,
			}},
			{ID: "network-fresh", Template: "network", Values: map[string]any{
				"name": "fresh", "circuits": "connection", "isolateDestinationAddress": false, "isolateDestinationPort": false,
			}},
		},
	}, 3)
	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Test service", "ports": "80, 443", "maxStreams": 4, "maxStreamsPolicy": "close",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 4)
	applyXTorTestForm(t, test, torNode, xTorClientAuthBlockID, map[string]any{
		xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization-client", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}},
	}, 5)
	driver := test.factory.waitDriver(t, 5)
	eventuallyXTor(t, func() bool {
		return test.hasPort(torNode, "left", "Network session") && test.hasPort(torNode, "left", "Network fresh") && test.hasPort(torNode, "left", "Onion Test service")
	}, "configured Tor output ports")

	eventuallyXTor(t, func() bool {
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
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "configured Tor node becomes ready")
	service.events <- tor.PublicationEvent{State: tor.PublicationPending}
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "publishing onion services" }, "service publication pending state")
	service.events <- tor.PublicationEvent{State: tor.PublicationPublished}
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "service publication ready state")

	cfg := configer.NewMemory(nil)
	if err := test.e.Engine.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var saved XTorDriverConfig
	if err := configer.Unmarshal(cfg.View(configer.Path{"nodes", "tor-features", xTorDriverConfigurationField}), &saved); err != nil {
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
		BlockID:     xTorControlBlockID,
		ButtonID:    xTorRestartButtonID,
	})
	test.factory.waitDriver(t, 6)
	eventuallyXTor(t, func() bool { return test.factory.driverClosed(5) }, "menu restart closes old driver")
}

func TestXTorDriverGeneratedSelectorsNotifyDurableConfigChanges(t *testing.T) {
	changes := make(chan uint64, 4)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		changes <- node
		return completedXTorConfigPersistence(nil)
	}))
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-config-notify")
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Persistent service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	assertXTorConfigChange(t, changes, torNode, "onion service selector")

	applyXTorTestForm(t, test, torNode, xTorClientAuthBlockID, map[string]any{
		xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization-client", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}},
	}, 2)
	assertXTorConfigChange(t, changes, torNode, "client authorization selector")

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	test.factory.waitDriver(t, 3)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) != "starting" }, "Tor restart completes")
	select {
	case node := <-changes:
		t.Fatalf("restart with saved selectors reported configuration change for node %d", node)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestXTorDriverRenamedSelectorsNotifyPersistAndRemainUsable(t *testing.T) {
	changes := make(chan uint64, 8)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		changes <- node
		return completedXTorConfigPersistence(nil)
	}))
	const nodeName = "tor-secret-rename"
	torNode := test.e.AddNode(NodeTypeXTorDriver, nodeName)
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Persistent service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	assertXTorConfigChange(t, changes, torNode, "generated onion service selector")
	applyXTorTestForm(t, test, torNode, xTorClientAuthBlockID, map[string]any{
		xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization-client", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}},
	}, 2)
	assertXTorConfigChange(t, changes, torNode, "generated client authorization selector")

	initial := savedXTorConfig(t, test, torNode)
	serviceOld := initial.Services[0].KeySecret
	authorizationOld := initial.ClientAuthorizations[0].PrivateKeySecret
	serviceNew := serviceOld + "-renamed"
	authorizationNew := authorizationOld + "-renamed"

	renameXTorTestSecret(t, test, nodeName, serviceOld, serviceNew)
	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: serviceOld,
			SecretNameNew:  serviceNew,
		},
	})
	assertXTorConfigChange(t, changes, torNode, "renamed onion service selector")
	test.factory.waitDriver(t, 3)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor reloads the renamed onion service secret")
	afterServiceRename := savedXTorConfig(t, test, torNode)
	if got := afterServiceRename.Services[0].KeySecret; got != serviceNew {
		t.Fatalf("saved service selector = %q, want %q", got, serviceNew)
	}
	if got := afterServiceRename.ClientAuthorizations[0].PrivateKeySecret; got != authorizationOld {
		t.Fatalf("service rename changed authorization selector to %q", got)
	}

	renameXTorTestSecret(t, test, nodeName, authorizationOld, authorizationNew)
	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: authorizationOld,
			SecretNameNew:  authorizationNew,
		},
	})
	assertXTorConfigChange(t, changes, torNode, "renamed client authorization selector")
	test.factory.waitDriver(t, 4)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor reloads the renamed client authorization secret")
	afterAuthorizationRename := savedXTorConfig(t, test, torNode)
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
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor restarts with both persisted renamed selectors")
	assertNoXTorConfigChange(t, changes, "restart with renamed selectors")
}

func TestXTorDriverSecretValueChangesDoNotReportConfigChanges(t *testing.T) {
	changes := make(chan uint64, 4)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		changes <- node
		return completedXTorConfigPersistence(nil)
	}))
	const nodeName = "tor-secret-value-change"
	torNode := test.e.AddNode(NodeTypeXTorDriver, nodeName)
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service-web", Template: "service", Values: map[string]any{
			"name": "Persistent service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default",
			"keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	assertXTorConfigChange(t, changes, torNode, "generated onion service selector")
	selector := savedXTorConfig(t, test, torNode).Services[0].KeySecret

	test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
		ReceiverNode: torNode,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: selector,
			SecretNameNew:  selector,
		},
	})
	test.factory.waitDriver(t, 2)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "Tor reloads a changed secret value")
	assertNoXTorConfigChange(t, changes, "secret value change")
	if got := savedXTorConfig(t, test, torNode).Services[0].KeySecret; got != selector {
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
	assertNoXTorConfigChange(t, changes, "unrelated secret change")
}

func TestXTorDriverDiscardsServiceSecretFromCanceledGeneration(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-service-cancel")
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
		BlockID:     xTorServicesBlockID,
		Values: map[string]any{xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}}},
	})
	waitXTorSignal(t, stored, "service secret creation")
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("secrets during canceled service build = %d, want 1", got)
	}
	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	close(release)
	test.factory.waitDriver(t, 2)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) != "starting" && test.secrets.SecretCount() == 1 }, "canceled service secret is replaced without leaking")
	assertXTorSavedSelectors(t, test, torNode, true, false)
}

func TestXTorDriverDiscardsClientAuthorizationSecretFromCanceledGeneration(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-client-auth-cancel")
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
		BlockID:     xTorClientAuthBlockID,
		Values: map[string]any{xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
			"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
		}}}},
	})
	waitXTorSignal(t, stored, "client authorization secret creation")
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("secrets during canceled authorization build = %d, want 1", got)
	}
	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	close(release)
	test.factory.waitDriver(t, 2)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) != "starting" && test.secrets.SecretCount() == 1 }, "canceled authorization secret is replaced without leaking")
	assertXTorSavedSelectors(t, test, torNode, false, true)
}

func TestXTorDriverDiscardsUncommittedSecretWhenNodeStops(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-secret-stop")
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
		BlockID:     xTorServicesBlockID,
		Values: map[string]any{xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}}},
	})
	waitXTorSignal(t, stored, "service secret creation before stop")
	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	close(release)
	eventuallyXTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "node stop discards uncommitted service secret")
}

func TestXTorDriverDiscardsGeneratedSecretsWhenRuntimeBuildFails(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeXTorFactory)
		apply     func(*testing.T, *xTorTestEngine, uint64)
		assert    func(*testing.T, XTorDriverConfig)
	}{
		{
			name: "onion service",
			configure: func(factory *fakeXTorFactory) {
				factory.newServiceError = errors.New("injected service creation failure")
			},
			apply: func(t *testing.T, test *xTorTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     xTorServicesBlockID,
					Values: map[string]any{xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
						"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config XTorDriverConfig) {
				if len(config.Services) != 1 || config.Services[0].KeySecret != "" {
					t.Fatalf("failed service build saved selectors: %#v", config.Services)
				}
			},
		},
		{
			name: "client authorization",
			configure: func(factory *fakeXTorFactory) {
				factory.addClientAuthError = errors.New("injected client authorization failure")
			},
			apply: func(t *testing.T, test *xTorTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     xTorClientAuthBlockID,
					Values: map[string]any{xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
						"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config XTorDriverConfig) {
				if len(config.ClientAuthorizations) != 1 || config.ClientAuthorizations[0].PrivateKeySecret != "" {
					t.Fatalf("failed authorization build saved selectors: %#v", config.ClientAuthorizations)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newXTorTestEngine(t)
			torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-failed-secret-"+strings.ReplaceAll(testCase.name, " ", "-"))
			test.factory.waitDriver(t, 0)
			test.factory.mu.Lock()
			testCase.configure(test.factory)
			test.factory.mu.Unlock()
			testCase.apply(t, test, torNode)
			test.factory.waitDriver(t, 1)
			eventuallyXTor(t, func() bool {
				return test.nodeLabel(torNode) == "error" && test.secrets.SecretCount() == 0
			}, "failed build discards its generated secret")
			testCase.assert(t, savedXTorConfig(t, test, torNode))
		})
	}
}

func TestXTorDriverRestartsSecretGenerationAfterNodeRename(t *testing.T) {
	tests := []struct {
		name    string
		setHook func(*fakeXTorFactory, func())
		apply   func(*testing.T, *xTorTestEngine, uint64)
		assert  func(*testing.T, XTorDriverConfig)
	}{
		{
			name: "onion service",
			setHook: func(factory *fakeXTorFactory, hook func()) {
				factory.afterServiceSecret = hook
			},
			apply: func(t *testing.T, test *xTorTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     xTorServicesBlockID,
					Values: map[string]any{xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
						"name": "service", "ports": "80", "maxStreams": 0, "maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config XTorDriverConfig) {
				if len(config.Services) != 1 || config.Services[0].KeySecret == "" {
					t.Fatalf("renamed service selector was not committed: %#v", config.Services)
				}
			},
		},
		{
			name: "client authorization",
			setHook: func(factory *fakeXTorFactory, hook func()) {
				factory.beforeAddClientAuth = hook
			},
			apply: func(t *testing.T, test *xTorTestEngine, node uint64) {
				test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
					MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
					BlockID:     xTorClientAuthBlockID,
					Values: map[string]any{xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
						"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
					}}}},
				})
			},
			assert: func(t *testing.T, config XTorDriverConfig) {
				if len(config.ClientAuthorizations) != 1 || config.ClientAuthorizations[0].PrivateKeySecret == "" {
					t.Fatalf("renamed authorization selector was not committed: %#v", config.ClientAuthorizations)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newXTorTestEngine(t)
			oldName := "tor-rename-" + strings.ReplaceAll(testCase.name, " ", "-")
			newName := oldName + "-new"
			torNode := test.e.AddNode(NodeTypeXTorDriver, oldName)
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
			waitXTorSignal(t, stored, "secret generation before rename")
			test.e.Engine.SetNodeName(almagest.MainWorkspace, torNode, newName)
			test.secrets.RenameNodeReferences(oldName, newName)
			close(release)

			test.factory.waitDriver(t, 2)
			eventuallyXTor(t, func() bool {
				return test.nodeLabel(torNode) != "starting" && test.secrets.SecretCount() == 1
			}, "renamed node replaces the stale generated secret")
			testCase.assert(t, savedXTorConfig(t, test, torNode))

			if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
				t.Fatal(err)
			}
			test.factory.waitDriver(t, 3)
			eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) != "error" && test.nodeLabel(torNode) != "starting" }, "renamed secret remains readable after restart")
			if got := test.secrets.SecretCount(); got != 1 {
				t.Fatalf("secrets after renamed restart = %d, want 1", got)
			}
		})
	}
}

func TestXTorDriverMenuRejectsInvalidConfigurationWithoutRestart(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-invalid-menu")
	test.e.SubscribeMenus(torNode)
	test.factory.waitDriver(t, 0)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "initial Tor runtime starts")

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     xTorNetworksBlockID,
		Values: map[string]any{xTorFieldNetworks: []formular.ArrayElementValue{{
			ID: "invalid-network", Template: "network", Values: map[string]any{
				"name": "invalid", "circuits": "connection", "isolateDestinationAddress": true, "isolateDestinationPort": false,
			},
		}}},
	})
	eventuallyXTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, xTorNetworksBlockID, xTorFieldNetworks)
		return test.nodeLabel(torNode) == "error" && ok && item.Field != nil &&
			item.Status == formular.StatusError && strings.Contains(item.StatusText, "requires session circuits")
	}, "invalid Tor form reports an inline error")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("invalid form started %d Tor drivers, want 1", got)
	}

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     xTorNetworksBlockID,
		Values: map[string]any{xTorFieldNetworks: []formular.ArrayElementValue{{
			ID: "default-network", Template: "network", Values: map[string]any{
				"name": "default", "circuits": "default", "isolateDestinationAddress": false, "isolateDestinationPort": false,
			},
		}}},
	})
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "corrected Tor form clears the error")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("unchanged corrected form started %d Tor drivers, want 1", got)
	}
}

func TestXTorDriverMenuRejectsDriverInvalidPolicyWithoutRestart(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-invalid-policy")
	test.e.SubscribeMenus(torNode)
	test.factory.waitDriver(t, 0)

	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
		BlockID:     xTorPoliciesBlockID,
		Values: map[string]any{
			xTorFieldOutboundFailures: "default",
			xTorFieldBandwidthRate:    "0", xTorFieldBandwidthBurst: "0",
			xTorFieldConnectionPadding: "default", xTorFieldCircuitPadding: "default",
			xTorFieldReachableORPorts: "", xTorFieldMaxPendingCircuits: 1025,
			xTorFieldNumCPUs: 0, xTorFieldClientIP: "default", xTorFieldOnionTraffic: "default",
			xTorFieldEntryNodes: "", xTorFieldExitNodes: "", xTorFieldExcludeNodes: "",
			xTorFieldExcludeExitNodes: "", xTorFieldStrictNodes: false,
		},
	})
	eventuallyXTor(t, func() bool {
		item, ok := test.e.MenuItem(torNode, xTorPoliciesBlockID, xTorFieldMaxPendingCircuits)
		return test.nodeLabel(torNode) == "error" && ok && item.Field != nil &&
			item.Status == formular.StatusError && strings.Contains(item.StatusText, "between 0 and 1024")
	}, "driver-invalid policy reports an inline error")
	if got := test.factory.count(); got != 1 {
		t.Fatalf("driver-invalid policy started %d Tor drivers, want 1", got)
	}
	if saved := savedXTorConfig(t, test, torNode); saved.Driver.MaxPendingCircuits != 0 {
		t.Fatalf("rejected pending circuit limit was saved: %d", saved.Driver.MaxPendingCircuits)
	}
}

func TestXTorDriverRejectsGeneratedSecretsWithoutDurableConfigPersistence(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		block   string
		values  map[string]any
		message string
	}{
		{
			name:  "onion service",
			block: xTorServicesBlockID,
			values: map[string]any{xTorFieldServices: []formular.ArrayElementValue{{
				ID: "service", Template: "service", Values: map[string]any{
					"name": "service", "ports": "80", "maxStreams": 0,
					"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
				},
			}}},
			message: "generated secrets require durable node configuration persistence",
		},
		{
			name:  "client authorization",
			block: xTorClientAuthBlockID,
			values: map[string]any{xTorFieldClientAuthorizations: []formular.ArrayElementValue{{
				ID: "authorization", Template: "authorization", Values: map[string]any{
					"address": testOnionAddress(), "name": "client", "privateKeySecret": "",
				},
			}}},
			message: "generated secrets require durable node configuration persistence",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(nil))
			torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-read-only-"+strings.ReplaceAll(testCase.name, " ", "-"))
			test.e.SubscribeMenus(torNode)
			test.factory.waitDriver(t, 0)

			test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, torNode, formular.FormApplyMessage{
				MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(torNode)},
				BlockID:     testCase.block,
				Values:      testCase.values,
			})
			eventuallyXTor(t, func() bool {
				return test.nodeLabel(torNode) == "error"
			}, "read-only generated secret configuration is rejected")
			if got := test.factory.count(); got != 1 {
				t.Fatalf("rejected generated secret started %d Tor drivers, want 1", got)
			}
			if got := test.secrets.SecretCount(); got != 0 {
				t.Fatalf("rejected generated secret stored %d secrets, want 0", got)
			}
			itemID := xTorFieldServices
			if testCase.block == xTorClientAuthBlockID {
				itemID = xTorFieldClientAuthorizations
			}
			eventuallyXTor(t, func() bool {
				item, ok := test.e.MenuItem(torNode, testCase.block, itemID)
				return ok && item.Field != nil && item.Status == formular.StatusError &&
					strings.Contains(item.StatusText, testCase.message)
			}, "read-only form displays the persistence error")
			saved := savedXTorConfig(t, test, torNode)
			if len(saved.Services) != 0 || len(saved.ClientAuthorizations) != 0 {
				t.Fatalf("rejected generated secret configuration was saved: %#v", saved)
			}
		})
	}
}

type xTorPersistenceRequest struct {
	node   uint64
	result chan error
}

func TestXTorDriverKeepsGeneratedSecretPendingUntilDurableAcknowledgement(t *testing.T) {
	requests := make(chan xTorPersistenceRequest, 4)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- xTorPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-persistence-pending")
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitXTorPersistenceRequest(t, requests)
	if request.node != torNode {
		t.Fatalf("persistence node = %d, want %d", request.node, torNode)
	}
	selector := savedXTorConfig(t, test, torNode).Services[0].KeySecret
	if selector == "" || test.secrets.SecretCount() != 1 {
		t.Fatalf("pending generated selector = %q, secrets = %d", selector, test.secrets.SecretCount())
	}

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	test.factory.waitDriver(t, 2)
	eventuallyXTor(t, func() bool {
		saved := savedXTorConfig(t, test, torNode)
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
	eventuallyXTor(t, func() bool { return test.secrets.SecretCount() == 1 }, "durably acknowledged secret survives node stop")
}

func TestXTorDriverRollsBackGeneratedSecretAfterPersistenceFailure(t *testing.T) {
	requests := make(chan xTorPersistenceRequest, 1)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- xTorPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-persistence-failure")
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitXTorPersistenceRequest(t, requests)
	request.result <- errors.New("durable write failed")
	close(request.result)

	eventuallyXTor(t, func() bool {
		saved := savedXTorConfig(t, test, torNode)
		return test.nodeLabel(torNode) == "error" && len(saved.Services) == 1 &&
			saved.Services[0].KeySecret == "" && test.secrets.SecretCount() == 0 &&
			test.factory.driverClosed(1)
	}, "persistence failure rolls back selector, secret, and runtime")
}

func TestXTorDriverDeletesPendingSecretRemovedBeforePersistenceCompletes(t *testing.T) {
	requests := make(chan xTorPersistenceRequest, 1)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- xTorPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-persistence-removed")
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitXTorPersistenceRequest(t, requests)
	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{xTorFieldServices: []formular.ArrayElementValue{}}, 2)
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("pending secret count before acknowledgement = %d, want 1", got)
	}
	request.result <- nil
	close(request.result)
	eventuallyXTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "removed selector discards acknowledged obsolete secret")
}

func TestXTorDriverDeletesUnacknowledgedSecretWhenNodeStops(t *testing.T) {
	requests := make(chan xTorPersistenceRequest, 1)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- xTorPersistenceRequest{node: node, result: result}
		return result
	}))
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-persistence-stop")
	test.factory.waitDriver(t, 0)

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitXTorPersistenceRequest(t, requests)
	test.e.Engine.RemoveNode(almagest.MainWorkspace, torNode)
	eventuallyXTor(t, func() bool { return test.secrets.SecretCount() == 0 }, "node stop discards unacknowledged secret")
	request.result <- nil
	close(request.result)
	time.Sleep(25 * time.Millisecond)
	if got := test.secrets.SecretCount(); got != 0 {
		t.Fatalf("late acknowledgement restored %d secrets", got)
	}
}

func TestXTorDriverRecoversPendingOnionKeyAfterProcessCrash(t *testing.T) {
	requests := make(chan xTorPersistenceRequest, 1)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- xTorPersistenceRequest{node: node, result: result}
		return result
	}))
	const nodeName = "tor-recover-onion-key"
	torNode := test.e.AddNode(NodeTypeXTorDriver, nodeName)
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
		xTorServiceSecretOwner(xTorServiceKeyName("service")),
	)
	clear(text)
	if err != nil {
		t.Fatal(err)
	}

	applyXTorTestForm(t, test, torNode, xTorServicesBlockID, map[string]any{
		xTorFieldServices: []formular.ArrayElementValue{{ID: "service", Template: "service", Values: map[string]any{
			"name": "service", "ports": "80", "maxStreams": 0,
			"maxStreamsPolicy": "default", "keySecret": "", "authorizedClients": "",
		}}},
	}, 1)
	request := waitXTorPersistenceRequest(t, requests)
	saved := savedXTorConfig(t, test, torNode)
	if got := saved.Services[0].KeySecret; got != selector {
		t.Fatalf("recovered service selector = %q, want %q", got, selector)
	}
	if got := test.secrets.SecretCount(); got != 1 {
		t.Fatalf("recovery stored %d secrets, want 1", got)
	}
	request.result <- nil
	close(request.result)
	eventuallyXTor(t, func() bool {
		key, _, recoverErr := test.secrets.RecoverGeneratedSecret(xTorServiceSecretOwner(xTorServiceKeyName("service")), nodeName)
		return recoverErr == nil && key == ""
	}, "durable service selector clears crash-recovery metadata")
}

func TestXTorDriverRecoversPendingClientAuthorizationAfterProcessCrash(t *testing.T) {
	requests := make(chan xTorPersistenceRequest, 1)
	test := newXTorTestEngineWithOptions(t, almagest.WithNodeConfigChangeCallback(func(node uint64) <-chan error {
		result := make(chan error, 1)
		requests <- xTorPersistenceRequest{node: node, result: result}
		return result
	}))
	const nodeName = "tor-recover-client-authorization"
	torNode := test.e.AddNode(NodeTypeXTorDriver, nodeName)
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
		xTorClientAuthorizationSecretOwner(address),
	)
	clear(text)
	if err != nil {
		t.Fatal(err)
	}

	applyXTorTestForm(t, test, torNode, xTorClientAuthBlockID, map[string]any{
		xTorFieldClientAuthorizations: []formular.ArrayElementValue{{ID: "authorization", Template: "authorization", Values: map[string]any{
			"address": address, "name": "client", "privateKeySecret": "",
		}}},
	}, 1)
	request := waitXTorPersistenceRequest(t, requests)
	saved := savedXTorConfig(t, test, torNode)
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
	eventuallyXTor(t, func() bool {
		key, _, recoverErr := test.secrets.RecoverGeneratedSecret(xTorClientAuthorizationSecretOwner(address), nodeName)
		return recoverErr == nil && key == ""
	}, "durable client authorization selector clears crash-recovery metadata")
}

func waitXTorPersistenceRequest(t *testing.T, requests <-chan xTorPersistenceRequest) xTorPersistenceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for node configuration persistence request")
		return xTorPersistenceRequest{}
	}
}

func TestXTorDriverRestoreRequiresPersistenceOnlyForEmptySecretSelectors(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		config XTorDriverConfig
		want   bool
	}{
		{
			name: "generated service key",
			config: XTorDriverConfig{Enabled: true, Services: []XTorDriverServiceConfig{{
				Name: "service", Ports: []uint16{80},
			}}},
			want: true,
		},
		{
			name: "generated client authorization",
			config: XTorDriverConfig{Enabled: true, ClientAuthorizations: []XTorDriverClientAuthConfig{{
				Address: testOnionAddress(), Name: "client",
			}}},
			want: true,
		},
		{
			name: "explicit selectors",
			config: XTorDriverConfig{Enabled: true,
				Services:             []XTorDriverServiceConfig{{Name: "service", Ports: []uint16{80}, KeySecret: "service-key"}},
				ClientAuthorizations: []XTorDriverClientAuthConfig{{Address: testOnionAddress(), Name: "client", PrivateKeySecret: "client-key"}},
			},
			want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := configer.NewMemory(nil)
			if err := configer.Marshal(cfg.View(configer.Path{xTorDriverConfigurationField}), testCase.config); err != nil {
				t.Fatal(err)
			}
			_, err := (XTorDriverClass{}).NewNode(cfg)
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

func applyXTorTestForm(t *testing.T, test *xTorTestEngine, node uint64, block string, values map[string]any, driverIndex int) {
	t.Helper()
	test.e.Engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     block,
		Values:      values,
	})
	test.factory.waitDriver(t, driverIndex)
	eventuallyXTor(t, func() bool { return test.nodeLabel(node) != "starting" }, "Tor applies %s", block)
}

func TestXTorDriverRejectsStaleReadinessAfterRestart(t *testing.T) {
	test := newXTorTestEngine(t)
	test.factory.blockReadiness = true
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-race")
	test.e.SubscribeMenus(torNode)
	first := test.factory.waitDriver(t, 0)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "waiting for Network" }, "first Tor runtime starts")

	if err := test.e.Engine.Trigger(almagest.MainWorkspace, torNode); err != nil {
		t.Fatal(err)
	}
	second := test.factory.waitDriver(t, 1)
	if first == second {
		t.Fatal("restart reused old driver")
	}
	first.markReady()
	eventuallyXTor(t, func() bool { return first.closed.Load() }, "old Tor driver closes")
	if got := test.nodeLabel(torNode); got == "up" {
		t.Fatal("stale readiness marked replacement runtime up")
	}
}

func TestXTorDriverConvergesOutboundChangedDuringStartup(t *testing.T) {
	test := newXTorTestEngine(t)
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

	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-startup-outbound")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Tor start did not reach the attachment race point")
	}
	out := test.e.AddNode("almagest/MockNetwork", "startup-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	close(release)

	driver := test.factory.waitDriver(t, 0)
	eventuallyXTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "startup attachment converges")
	calls := driver.setOutboundCalls()
	if len(calls) != 1 || calls[0] == nil {
		t.Fatalf("SetOutbound calls = %#v, want one current non-nil attachment", calls)
	}
}

func TestXTorDriverSerializesRapidOutboundDetachAndReattach(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-rapid-outbound")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "rapid-out")
	link := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "initial attachment becomes ready")

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
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	close(releaseDetach)

	eventuallyXTor(t, func() bool { return driver.outbound().Available && test.nodeLabel(torNode) == "up" }, "rapid reattach converges to the latest attachment")
	calls := driver.setOutboundCalls()
	if len(calls) < 3 || calls[len(calls)-2] != nil || calls[len(calls)-1] == nil {
		t.Fatalf("SetOutbound calls = %#v, want detach followed by current reattach", calls)
	}
}

func TestXTorDriverLatchedFailureWithdrawsOutputsUntilReattach(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-latched-output")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "latched-out")
	outLink := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "Tor becomes ready")

	receiverNode := test.e.AddNode(xTorReceiverNodeType, "receiver")
	receiver := test.receivers.wait(t, 0)
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, torNode, "left", "Network default", receiverNode, "right", "Network")
	readyPayload := networktest.ReceivePayload(t, receiver.Events)
	if readyPayload.Network == nil {
		t.Fatal("Tor output was not published")
	}

	state := driver.outbound()
	driver.setOutboundAvailable(false)
	driver.events <- tor.OutboundEvent{Generation: state.Generation, Latched: true}
	waitXTorUnavailablePayload(t, receiver.Events)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "error" }, "latched failure becomes an error")
	// Model readiness that completed before the failure but reached the node
	// after the failure event.
	for revision := uint64(0); revision < 4; revision++ {
		test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
			ReceiverNode: torNode,
			Payload: xTorOutboundResult{
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
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "reattach recovers latched output")
	if payload := networktest.ReceivePayload(t, receiver.Events); payload.Network == nil {
		t.Fatal("reattach did not republish Tor output")
	}
}

func TestXTorDriverOutboundReplacementDoesNotCancelControlWait(t *testing.T) {
	test := newXTorTestEngine(t)
	test.factory.blockReadiness = true
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-wait-replacement")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "wait-out")
	link := test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return driver.waitReadyCalls.Load() > 0 }, "outbound readiness wait starts")

	test.e.Engine.RemoveLink(almagest.MainWorkspace, link)
	eventuallyXTor(t, func() bool {
		calls := driver.setOutboundCalls()
		return len(calls) >= 2 && calls[len(calls)-1] == nil
	}, "outbound detach is applied while readiness remains blocked")
	if got := driver.waitReadyCanceled.Load(); got != 0 {
		t.Fatalf("readiness waits canceled during detach = %d, want 0", got)
	}
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return len(driver.setOutboundCalls()) >= 3 }, "replacement is applied while the old readiness wait remains active")
	if got := driver.waitReadyCanceled.Load(); got != 0 {
		t.Fatalf("readiness waits canceled during replacement = %d, want 0", got)
	}
	driver.markReady()
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "replacement becomes ready")
}

func TestXTorDriverTerminalFailureRejectsQueuedReadiness(t *testing.T) {
	test := newXTorTestEngine(t)
	torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-terminal")
	driver := test.factory.waitDriver(t, 0)
	out := test.e.AddNode("almagest/MockNetwork", "terminal-out")
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "Tor becomes ready before terminal failure")

	receiverNode := test.e.AddNode(xTorReceiverNodeType, "terminal-receiver")
	receiver := test.receivers.wait(t, 0)
	test.e.MustAddTypedLinkByPortName(links.TypeNetwork, torNode, "left", "Network default", receiverNode, "right", "Network")
	if payload := networktest.ReceivePayload(t, receiver.Events); payload.Network == nil {
		t.Fatal("Tor output was not published")
	}

	driver.events <- tor.TerminalEvent{Cause: tor.TerminalControlConnection}
	waitXTorUnavailablePayload(t, receiver.Events)
	eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "error" }, "terminal failure becomes an error")

	// Model a readiness completion that was calculated before the terminal
	// event but entered the workspace inbox after it.
	for revision := uint64(0); revision < 4; revision++ {
		test.e.Engine.SendInbox(almagest.MainWorkspace, pasta.InboxMessage{
			ReceiverNode: torNode,
			Payload: xTorOutboundResult{
				runtimeID: 1, outboundRevision: revision,
				state: tor.OutboundState{Available: true}, ready: true,
			},
		})
	}
	time.Sleep(2 * xTorOutboundRetryDelay)
	if got := test.nodeLabel(torNode); got != "error" {
		t.Fatalf("queued readiness changed terminal label to %q", got)
	}
	networktest.AssertNoEvent(t, receiver.Events)
}

func TestXTorDriverRetriesRecoverableOutboundOperations(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeXTorFactory)
		assert    func(*testing.T, *fakeXTorDriver)
	}{
		{
			name: "attachment",
			configure: func(factory *fakeXTorFactory) {
				factory.setOutboundErrors = []error{errors.New("temporary attachment failure")}
			},
			assert: func(t *testing.T, driver *fakeXTorDriver) {
				if calls := driver.setOutboundCalls(); len(calls) < 2 {
					t.Fatalf("SetOutbound calls = %d, want an automatic retry", len(calls))
				}
			},
		},
		{
			name: "readiness",
			configure: func(factory *fakeXTorFactory) {
				factory.waitReadyErrors = []error{errors.New("temporary readiness failure")}
			},
			assert: func(t *testing.T, driver *fakeXTorDriver) {
				if calls := driver.waitReadyCalls.Load(); calls < 2 {
					t.Fatalf("WaitReady calls = %d, want an automatic retry", calls)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newXTorTestEngine(t)
			test.factory.mu.Lock()
			testCase.configure(test.factory)
			test.factory.mu.Unlock()
			torNode := test.e.AddNode(NodeTypeXTorDriver, "tor-retry-"+testCase.name)
			driver := test.factory.waitDriver(t, 0)
			out := test.e.AddNode("almagest/MockNetwork", "retry-out-"+testCase.name)
			test.e.MustAddTypedLinkByPortName(links.TypeNetwork, out, "left", "Network", torNode, "right", xTorOutboundPortName)
			eventuallyXTor(t, func() bool { return test.nodeLabel(torNode) == "up" }, "Tor recovers without a graph change")
			testCase.assert(t, driver)
		})
	}
}

type xTorTestEngine struct {
	t         *testing.T
	e         *e2etest.NodeE2E
	factory   *fakeXTorFactory
	secrets   *e2etest.SecretsStorage
	receivers *xTorReceiverClass
}

func newXTorTestEngine(t *testing.T) *xTorTestEngine {
	return newXTorTestEngineWithOptions(t)
}

func newXTorTestEngineWithOptions(t *testing.T, options ...almagest.BuildEngineOption) *xTorTestEngine {
	t.Helper()
	factory := &fakeXTorFactory{}
	secrets := e2etest.NewSecretsStorage()
	logf := logging.NewDebugLogFactory(1024)
	system := e2etest.NewDebugSystem()
	pool := e2etest.NewTestDebugPool(t)
	class := XTorDriverClass{
		newSystem: factory.newSystem,
		start:     factory.start,
	}
	receivers := &xTorReceiverClass{}
	buildOptions := append([]almagest.BuildEngineOption{
		almagest.WithNodeConfigChangeCallback(func(uint64) <-chan error {
			return completedXTorConfigPersistence(nil)
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
	return &xTorTestEngine{t: t, e: e2etest.NewNodeE2EFromEngine(t, engine, logf, system, secrets), factory: factory, secrets: secrets, receivers: receivers}
}

func completedXTorConfigPersistence(err error) <-chan error {
	result := make(chan error, 1)
	result <- err
	close(result)
	return result
}

func (e *xTorTestEngine) nodeLabel(node uint64) string {
	snapshot, ok := e.e.Engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		return ""
	}
	return snapshot.Label
}

func (e *xTorTestEngine) hasPort(node uint64, direction, name string) bool {
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

type fakeXTorFactory struct {
	mu                  sync.Mutex
	drivers             []*fakeXTorDriver
	systems             []*fakeXTorSystem
	blockReadiness      bool
	startEntered        chan struct{}
	startRelease        chan struct{}
	startOnce           sync.Once
	setOutboundHook     func(gonnect.Network)
	afterServiceSecret  func()
	beforeAddClientAuth func()
	waitReadyErrors     []error
	setOutboundErrors   []error
	newServiceError     error
	addClientAuthError  error
}

func (f *fakeXTorFactory) newSystem(config tor.Config) (xTorSystem, tor.Config, error) {
	system := &fakeXTorSystem{report: direct.BestEffortReport{Containment: true, Identity: &tor.Identity{UID: 65534, GID: 65534}, AutomaticIdentity: true, PrivilegeDrop: true}}
	config.TorExecutable = "/fake/tor"
	f.mu.Lock()
	f.systems = append(f.systems, system)
	f.mu.Unlock()
	return system, config, nil
}

func (f *fakeXTorFactory) start(ctx context.Context, config tor.Config, deps tor.Dependencies) (xTorDriverBackend, error) {
	driver := newFakeXTorDriver(config, deps, !f.blockReadiness)
	f.mu.Lock()
	driver.setOutboundHook = f.setOutboundHook
	driver.afterServiceSecret = f.afterServiceSecret
	driver.beforeAddClientAuth = f.beforeAddClientAuth
	driver.waitReadyErrors = append([]error(nil), f.waitReadyErrors...)
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

func (f *fakeXTorFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.drivers)
}

func (f *fakeXTorFactory) waitDriver(t *testing.T, index int) *fakeXTorDriver {
	t.Helper()
	var driver *fakeXTorDriver
	eventuallyXTor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if index < len(f.drivers) {
			driver = f.drivers[index]
		}
		return driver != nil
	}, "Tor driver %d starts", index)
	return driver
}

func (f *fakeXTorFactory) driverClosed(index int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return index < len(f.drivers) && f.drivers[index].closed.Load()
}

func (f *fakeXTorFactory) systemClosed(index int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return index < len(f.systems) && f.systems[index].closed.Load()
}

type fakeXTorSystem struct {
	report direct.BestEffortReport
	closed atomic.Bool
}

func (s *fakeXTorSystem) Dependencies(_ gonnect.Network, outbound gonnect.Network, logger tor.Logger) tor.Dependencies {
	return tor.Dependencies{Outbound: outbound, Logger: logger}
}
func (s *fakeXTorSystem) Report() direct.BestEffortReport { return s.report }
func (s *fakeXTorSystem) Close() error {
	s.closed.Store(true)
	return nil
}

type fakeXTorDriver struct {
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
	services            []*fakeXTorService
	setOutboundHook     func(gonnect.Network)
	outboundHistory     []gonnect.Network
	afterServiceSecret  func()
	beforeAddClientAuth func()
	waitReadyErrors     []error
	setOutboundErrors   []error
	newServiceError     error
	addClientAuthError  error
	waitReadyCalls      atomic.Int32
	waitReadyCanceled   atomic.Int32
}

func newFakeXTorDriver(config tor.Config, deps tor.Dependencies, ready bool) *fakeXTorDriver {
	d := &fakeXTorDriver{config: config, deps: deps, events: make(chan tor.DriverEvent, 32), done: make(chan struct{}), ready: make(chan struct{})}
	d.state = tor.OutboundState{Available: deps.Outbound != nil, Generation: 1}
	if ready {
		d.markReady()
	}
	return d
}

func (d *fakeXTorDriver) markReady() { d.readyOnce.Do(func() { close(d.ready) }) }
func (d *fakeXTorDriver) WaitReady(ctx context.Context) error {
	d.waitReadyCalls.Add(1)
	d.mu.Lock()
	if len(d.waitReadyErrors) > 0 {
		err := d.waitReadyErrors[0]
		d.waitReadyErrors = d.waitReadyErrors[1:]
		d.mu.Unlock()
		return err
	}
	d.mu.Unlock()
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
func (d *fakeXTorDriver) SubscribeEvents(int) (<-chan tor.DriverEvent, func(), error) {
	return d.events, func() {}, nil
}
func (d *fakeXTorDriver) SetOutbound(network gonnect.Network) error {
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
func (d *fakeXTorDriver) setOutboundCalls() []gonnect.Network {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]gonnect.Network(nil), d.outboundHistory...)
}
func (d *fakeXTorDriver) setHook(hook func(gonnect.Network)) {
	d.mu.Lock()
	d.setOutboundHook = hook
	d.mu.Unlock()
}
func (d *fakeXTorDriver) outbound() tor.OutboundState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}
func (d *fakeXTorDriver) setOutboundAvailable(available bool) {
	d.mu.Lock()
	d.state.Available = available
	d.mu.Unlock()
}
func (d *fakeXTorDriver) OutboundState() tor.OutboundState { return d.outbound() }
func (d *fakeXTorDriver) NewNetwork(config tor.NetworkConfig) (xTorNetwork, error) {
	d.mu.Lock()
	d.networkConfigs = append(d.networkConfigs, config)
	d.mu.Unlock()
	return newFakeXTorNetwork(), nil
}
func (d *fakeXTorDriver) NewService(ctx context.Context, config tor.ServiceConfig) (xTorService, error) {
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
	service := newFakeXTorService()
	d.mu.Lock()
	d.serviceConfigs = append(d.serviceConfigs, config)
	d.services = append(d.services, service)
	d.mu.Unlock()
	return service, nil
}
func (d *fakeXTorDriver) GenerateClientAuthorization(name string) (tor.ClientAuthorization, error) {
	key, err := tor.NewClientAuthorizationPrivateKey(bytes.Repeat([]byte{1}, 32))
	return tor.ClientAuthorization{PrivateKey: key, Name: name}, err
}
func (d *fakeXTorDriver) AddClientAuthorization(_ context.Context, _ string, authorization tor.ClientAuthorization) error {
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

func waitXTorSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertXTorSavedSelectors(t *testing.T, test *xTorTestEngine, node uint64, service, authorization bool) {
	t.Helper()
	saved := savedXTorConfig(t, test, node)
	if service && (len(saved.Services) != 1 || saved.Services[0].KeySecret == "") {
		t.Fatalf("saved service selector = %#v", saved.Services)
	}
	if authorization && (len(saved.ClientAuthorizations) != 1 || saved.ClientAuthorizations[0].PrivateKeySecret == "") {
		t.Fatalf("saved client authorization selector = %#v", saved.ClientAuthorizations)
	}
}

func savedXTorConfig(t *testing.T, test *xTorTestEngine, node uint64) XTorDriverConfig {
	t.Helper()
	cfg := configer.NewMemory(nil)
	if err := test.e.Engine.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := test.e.Engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatal("Tor node is missing")
	}
	var saved XTorDriverConfig
	if err := configer.Unmarshal(cfg.View(configer.Path{"nodes", snapshot.Name, xTorDriverConfigurationField}), &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}
func (d *fakeXTorDriver) Done() <-chan struct{} { return d.done }
func (d *fakeXTorDriver) Err() error            { return nil }
func (d *fakeXTorDriver) Close() error {
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

type fakeXTorNetwork struct {
	gonnect.Network
	closed atomic.Bool
}

func newFakeXTorNetwork() *fakeXTorNetwork {
	return &fakeXTorNetwork{Network: gonnect.NewLoopbackNetwork()}
}
func (n *fakeXTorNetwork) Close() error { n.closed.Store(true); return nil }

type fakeXTorService struct {
	*fakeXTorNetwork
	events    chan tor.ServiceEvent
	closeOnce sync.Once
}

func newFakeXTorService() *fakeXTorService {
	events := make(chan tor.ServiceEvent, 2)
	events <- tor.PublicationEvent{State: tor.PublicationPublished}
	return &fakeXTorService{fakeXTorNetwork: newFakeXTorNetwork(), events: events}
}
func (s *fakeXTorService) Address() string { return testOnionAddress() }
func (s *fakeXTorService) SubscribeEvents(int) (<-chan tor.ServiceEvent, func(), error) {
	return s.events, func() {}, nil
}
func (s *fakeXTorService) Close() error {
	s.closeOnce.Do(func() {
		_ = s.fakeXTorNetwork.Close()
		close(s.events)
	})
	return nil
}

func testOnionAddress() string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.onion"
}

func eventuallyXTor(t *testing.T, condition func() bool, format string, args ...any) {
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

func assertXTorConfigChange(t *testing.T, changes <-chan uint64, want uint64, description string) {
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

func assertNoXTorConfigChange(t *testing.T, changes <-chan uint64, description string) {
	t.Helper()
	select {
	case node := <-changes:
		t.Fatalf("%s reported a configuration change for node %d", description, node)
	case <-time.After(50 * time.Millisecond):
	}
}

func renameXTorTestSecret(t *testing.T, test *xTorTestEngine, nodeName, previous, next string) {
	t.Helper()
	value, ok := test.secrets.SecretValue(previous)
	if !ok {
		t.Fatalf("secret %q is missing before rename", previous)
	}
	test.secrets.SetSecret(next, storage.Secret{Value: value}, nodeName)
	test.secrets.DeleteSecret(previous)
}

func waitXTorUnavailablePayload(t *testing.T, events <-chan links.NetworkPayload) {
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

const xTorReceiverNodeType = "test/XTorNetworkReceiver"

type xTorReceiverClass struct {
	mu        sync.Mutex
	receivers []*networktest.Receiver
}

func (*xTorReceiverClass) ClassName() string        { return xTorReceiverNodeType }
func (*xTorReceiverClass) ShortDescription() string { return "XTor test receiver" }
func (*xTorReceiverClass) LongDescription() string  { return "Receives XTor network payloads." }
func (*xTorReceiverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeNetwork}
}
func (c *xTorReceiverClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	receiver := networktest.NewReceiver(32)
	c.mu.Lock()
	c.receivers = append(c.receivers, receiver)
	c.mu.Unlock()
	return receiver, nil
}

func (c *xTorReceiverClass) wait(t *testing.T, index int) *networktest.Receiver {
	t.Helper()
	var receiver *networktest.Receiver
	eventuallyXTor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if index < len(c.receivers) {
			receiver = c.receivers[index]
		}
		return receiver != nil
	}, "XTor network receiver %d starts", index)
	return receiver
}
