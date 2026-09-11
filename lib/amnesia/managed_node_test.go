package amnesianodes

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func TestAmnesiaClassHasOnlyManagedPortsAndMenuFields(t *testing.T) {
	class := AmnesiaClass{}
	if NodeTypeAmnesia != "almagest/Amnesia" || class.ClassName() != NodeTypeAmnesia {
		t.Fatalf("Amnesia class name = %q", class.ClassName())
	}
	params := class.DefaultNodeParams()
	if params.PrimaryType != wireguardnodes.TypeWGCtrl || len(params.InitialPorts) != 3 {
		t.Fatalf("default params = %#v", params)
	}
	if port := params.InitialPorts[0]; port.Direction != "left" || port.Name != "Tun" || len(port.Types) != 1 || port.Types[0] != nodekit.TypeTun {
		t.Fatalf("TUN port = %#v", port)
	}
	if port := params.InitialPorts[1]; port.Direction != "left" || port.Name != "DNS" || len(port.Types) != 1 || port.Types[0] != dnsnodes.TypeDNS {
		t.Fatalf("DNS port = %#v", port)
	}
	if port := params.InitialPorts[2]; port.Direction != "right" || port.Name != "Network" || len(port.Types) != 1 || port.Types[0] != nodekit.TypeNetwork {
		t.Fatalf("network port = %#v", port)
	}

	raw, err := class.NewNode(configer.NewMemory(map[string]any{
		fieldConnectionSecret:  "connection",
		fieldServerCountryCode: "DE",
		// A legacy/controller field must not become managed-node config.
		fieldConnectEvent: false,
	}))
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	node := raw.(*managedAmnesiaNode)
	if err := node.OnInit(nil, nil, 0, NodeTypeAmnesia, &pasta.NodeInitData{Name: "Amnesia Service"}, false, false, false, false); err != nil {
		t.Fatalf("initialize node: %v", err)
	}
	if want := amnesiaStorageID("Amnesia Service"); node.config.storageID != want {
		t.Fatalf("storage ID = %q, want %q", node.config.storageID, want)
	}
	if node.config.serverCountryCode != "de" {
		t.Fatalf("server country = %q, want de", node.config.serverCountryCode)
	}
	for _, id := range []string{
		fieldEnabled,
		fieldConnectionSecret,
		fieldServerCountryCode,
		nodekit.TLSConfigFieldID,
		fieldGatewayURL,
		fieldGatewayPublicKeySecret,
	} {
		item, ok := amnesiaMenuItem(node.configBlock(), id)
		if !ok || item.Field == nil {
			t.Errorf("managed config field %q = %#v", id, item)
		}
		if (id == fieldConnectionSecret || id == fieldGatewayPublicKeySecret) && item.Selector != nodekit.SecretNameSelector {
			t.Errorf("managed secret field %q selector = %q, want %q", id, item.Selector, nodekit.SecretNameSelector)
		}
	}
	for _, id := range []string{fieldConnectEvent, "reconnect", "privateKey", "peers"} {
		if item, ok := amnesiaMenuItem(node.configBlock(), id); ok {
			t.Errorf("unexpected managed config item %q = %#v", id, item)
		}
	}
	for _, id := range []string{
		"state", "inputFormat", "profile", "country", "addresses", "dns",
		"installationUUID", "rxBytes", "txBytes",
	} {
		item, ok := amnesiaMenuItem(node.statusBlock(), id)
		if !ok || item.Field == nil || !item.Readonly {
			t.Errorf("managed status field %q = %#v", id, item)
		}
	}
	for _, id := range []string{"latestHandshake", "peerPublicKey", "allowedIPs", "peerActive", "peerConnected", "wireGuard"} {
		if item, ok := amnesiaMenuItem(node.statusBlock(), id); ok {
			t.Errorf("unexpected managed status item %q = %#v", id, item)
		}
	}

	saved := configer.NewMemory(map[string]any{})
	if err := node.OnSave(saved); err != nil {
		t.Fatalf("save node: %v", err)
	}
	if _, err := saved.Get(configer.Path{fieldConnectEvent}); err == nil {
		t.Fatal("managed node persisted the controller connect-event setting")
	}
	for _, field := range []string{"privateKey", "privateKeySecret", "listenPort", "peers", "amneziaWG"} {
		if _, err := saved.Get(configer.Path{field}); err == nil {
			t.Errorf("managed node persisted WireGuard field %q", field)
		}
	}
}

func TestManagedAmnesiaRuntimeUsesEphemeralDeviceAndRotatesKey(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})

	secrets := &amnesiaTestSecrets{values: map[string]string{
		"connection": amnesiaTestActivationKey(t, "8f2c41d7a693b5e0"),
	}}
	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	recorder := &managedAmnesiaClientRecorder{
		t: t, peerKey: peerKey,
		dnsByCountry: map[string][]string{
			"de": {"10.20.0.1"},
			"nl": {"10.20.0.3"},
		},
	}
	node := &managedAmnesiaNode{
		secrets: secrets,
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)
	req := managedAmnesiaRuntimeRequest{
		config: node.config, tunLinked: true, tun: tunSource, network: network,
		nodeName: "Amnesia", ctx: context.Background(),
	}

	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !prepared.prepared || prepared.ready || prepared.format != wgoamnesia.InputFormatServiceKey {
		t.Fatalf("prepared reconcile = %#v", prepared)
	}
	if node.runtimeDevice != nil || len(recorder.privateKeys) != 1 {
		t.Fatalf("first connection device_created=%t keys=%d", node.runtimeDevice != nil, len(recorder.privateKeys))
	}
	if !recorder.negotiations[0].ConnectEvent {
		t.Fatal("managed connection did not enable the connect event")
	}
	if recorder.options[0].Device != nil {
		t.Fatal("Amnezia client received a device before profile negotiation")
	}
	firstKey := recorder.privateKeys[0]
	if !node.markRuntimeTunConfigured(prepared.runtimeID) {
		t.Fatal("prepared runtime could not be marked as TUN-configured")
	}
	req.tun = nil
	waiting := runManagedAmnesiaReconcile(t, node, &req)
	if !waiting.waitingForTun || len(recorder.negotiations) != 1 || node.runtimeDevice != nil {
		t.Fatalf(
			"temporary TUN gap=%#v negotiations=%d device_created=%t",
			waiting, len(recorder.negotiations), node.runtimeDevice != nil,
		)
	}

	// Model the TUN replacement caused by publishing the negotiated interface
	// configuration. Activation must create only one device for the registered
	// profile key.
	replacementTun, replacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = replacementTun.Close()
		_ = replacementPeer.Close()
	})
	req.tun = replacementTun
	activated := runManagedAmnesiaReconcile(t, node, &req)
	if !activated.ready || activated.prepared {
		t.Fatalf("activation reconcile = %#v", activated)
	}
	if node.runtimeDevice == nil || len(recorder.negotiations) != 1 {
		t.Fatal("TUN configuration replacement negotiated a second connection")
	}
	if activated.dns == nil || !dnsnodes.SameDNSInstance(activated.dns, node.runtimeDNS) {
		t.Fatal("activated runtime did not publish its tunnel DNS resolver")
	}
	firstDNS := activated.dns
	firstDevice := node.runtimeDevice
	if firstDevice.PrivateKey() != firstKey {
		t.Fatal("managed device did not receive the negotiated private key")
	}

	// An equivalent reconcile keeps the same live connection.
	reused := runManagedAmnesiaReconcile(t, node, &req)
	if !reused.ready {
		t.Fatalf("equivalent reconcile = %#v", reused)
	}
	if node.runtimeDevice != firstDevice {
		t.Fatal("equivalent reconcile rebuilt the managed connection")
	}
	if !dnsnodes.SameDNSInstance(reused.dns, firstDNS) {
		t.Fatal("equivalent reconcile replaced the tunnel DNS resolver")
	}

	// A graph TUN replacement changes the device attachment, not the service
	// identity. Reuse the configured device and replace only its detached TUN.
	hotReplacementTun, hotReplacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = hotReplacementTun.Close()
		_ = hotReplacementPeer.Close()
	})
	req.tun = hotReplacementTun
	hotReplaced := runManagedAmnesiaReconcile(t, node, &req)
	if !hotReplaced.ready || len(recorder.negotiations) != 1 {
		t.Fatalf("hot TUN replacement=%#v negotiations=%d", hotReplaced, len(recorder.negotiations))
	}
	if node.runtimeDevice != firstDevice || node.runtimeDevice.PrivateKey() != firstKey {
		t.Fatal("hot TUN replacement rebuilt the registered WireGuard identity")
	}
	if hotReplaced.dns == nil || dnsnodes.SameDNSInstance(hotReplaced.dns, firstDNS) ||
		!dnsnodes.DNSIsReady(hotReplaced.dns) {
		t.Fatal("hot TUN replacement did not publish a fresh ready tunnel DNS resolver")
	}
	if dnsnodes.DNSIsReady(firstDNS) {
		t.Fatal("hot TUN replacement left the old tunnel DNS resolver ready")
	}
	firstDNS = hotReplaced.dns

	// A material connection change creates a new zero-key device. The client
	// generates a new identity while it imports the new connection.
	req.config.serverCountryCode = "nl"
	secondPrepared := runManagedAmnesiaReconcile(t, node, &req)
	if !secondPrepared.prepared || secondPrepared.ready || node.runtimeDevice != nil || len(recorder.privateKeys) != 2 {
		t.Fatalf("second prepared result=%#v device_created=%t keys=%d", secondPrepared, node.runtimeDevice != nil, len(recorder.privateKeys))
	}
	if recorder.privateKeys[1] == firstKey {
		t.Fatal("replacement connection reused the old WireGuard private key")
	}
	select {
	case <-firstDevice.Wait():
	default:
		t.Fatal("old WireGuard device remained open after rotation")
	}
	if dnsnodes.DNSIsReady(firstDNS) {
		t.Fatal("old tunnel DNS resolver remained ready after country change")
	}
	if !node.markRuntimeTunConfigured(secondPrepared.runtimeID) {
		t.Fatal("replacement runtime could not be marked as TUN-configured")
	}
	secondActivated := runManagedAmnesiaReconcile(t, node, &req)
	if !secondActivated.ready || len(recorder.negotiations) != 2 || node.runtimeDevice == nil {
		t.Fatalf("second activation result=%#v negotiations=%d", secondActivated, len(recorder.negotiations))
	}
	if node.runtimeDevice == firstDevice {
		t.Fatal("material change reused the old WireGuard device")
	}
	if node.runtimeDevice.PrivateKey() != recorder.privateKeys[1] {
		t.Fatal("replacement device did not receive the new private key")
	}
	if secondActivated.dns == nil || dnsnodes.SameDNSInstance(secondActivated.dns, firstDNS) ||
		!dnsnodes.DNSIsReady(secondActivated.dns) {
		t.Fatal("country change did not publish a fresh ready tunnel DNS resolver")
	}
	if len(node.runtimeProfile.dns) != 1 || node.runtimeProfile.dns[0] != "10.20.0.3" {
		t.Fatalf("replacement country DNS servers = %v, want [10.20.0.3]", node.runtimeProfile.dns)
	}
	if !nodekit.TunIsReady(tunSource) || !nodekit.TunIsReady(replacementTun) {
		t.Fatal("device rotation consumed the graph-owned TUN")
	}
}

func TestManagedAmnesiaRuntimeKeepsSuccessfulNegotiationAcrossStaleGeneration(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})

	recorder := &managedAmnesiaClientRecorder{
		t:       t,
		peerKey: amnesiaTestPrivateKey(t).PublicKey(),
	}
	node := &managedAmnesiaNode{
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "3a9e71c4d860f2b5"),
		}},
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)
	recorder.onImport = func() {
		// Model an equivalent graph notification that invalidates the request
		// after the service has accepted its generated public key.
		node.generation.Add(1)
	}
	req := managedAmnesiaRuntimeRequest{
		generation: node.generation.Add(1), config: node.config, tunLinked: true,
		tun: tunSource, network: network, nodeName: "Amnesia", ctx: context.Background(),
	}
	if result, ok := node.reconcile(req); ok {
		t.Fatalf("stale reconcile returned a result: %#v", result)
	}
	if node.runtimeDevice != nil || node.runtimeConfig == nil {
		t.Fatal("successful stale negotiation did not retain its profile")
	}

	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !prepared.prepared || len(recorder.negotiations) != 1 || node.runtimeDevice != nil {
		t.Fatalf(
			"next reconcile=%#v negotiations=%d device_created=%t",
			prepared, len(recorder.negotiations), node.runtimeDevice != nil,
		)
	}
}

func TestManagedAmnesiaRuntimeKeepsPreparedProfileWhenTunGoesDownDuringReconcile(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})

	recorder := &managedAmnesiaClientRecorder{
		t:       t,
		peerKey: amnesiaTestPrivateKey(t).PublicKey(),
	}
	node := &managedAmnesiaNode{
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "c7d1408a5e3f926b"),
		}},
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)
	req := managedAmnesiaRuntimeRequest{
		config: node.config, tunLinked: true, tun: tunSource, network: network,
		nodeName: "Amnesia", ctx: context.Background(),
	}

	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !prepared.prepared || len(recorder.negotiations) != 1 {
		t.Fatalf("initial reconcile=%#v negotiations=%d", prepared, len(recorder.negotiations))
	}
	if !node.markRuntimeTunConfigured(prepared.runtimeID) {
		t.Fatal("prepared runtime could not be marked as TUN-configured")
	}
	privateKey := recorder.privateKeys[0]

	// Model a provider that was ready when the worker captured its input but
	// went down before the worker decided whether it could reuse the prepared
	// profile. This is the ordering exposed by coverage instrumentation in the
	// managed self-host E2E case.
	req.tun = &readyThenDownTun{Tun: tunSource}
	waiting := runManagedAmnesiaReconcile(t, node, &req)
	if !waiting.waitingForTun || waiting.prepared || len(recorder.negotiations) != 1 {
		t.Fatalf("TUN transition=%#v negotiations=%d", waiting, len(recorder.negotiations))
	}
	if node.runtimeConfig == nil || node.runtimeID != prepared.runtimeID {
		t.Fatal("TUN transition discarded the registered profile")
	}

	replacementTun, replacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = replacementTun.Close()
		_ = replacementPeer.Close()
	})
	req.tun = replacementTun
	activated := runManagedAmnesiaReconcile(t, node, &req)
	if !activated.ready || len(recorder.negotiations) != 1 || node.runtimeDevice == nil {
		t.Fatalf("replacement reconcile=%#v negotiations=%d", activated, len(recorder.negotiations))
	}
	if node.runtimeDevice.PrivateKey() != privateKey {
		t.Fatal("TUN replacement did not reuse the registered profile key")
	}
	activeDevice := node.runtimeDevice

	// A graph unlink is not a temporary provider gap. It ends ownership of the
	// registration. A later link must create a new service identity.
	req.tunLinked = false
	req.tun = nil
	detached := runManagedAmnesiaReconcile(t, node, &req)
	if detached.ready || node.runtimeConfig != nil || len(recorder.negotiations) != 1 {
		t.Fatalf("TUN detach=%#v negotiations=%d", detached, len(recorder.negotiations))
	}
	select {
	case <-activeDevice.Wait():
	default:
		t.Fatal("TUN detach left the data-plane device open")
	}

	reattachedTun, reattachedPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = reattachedTun.Close()
		_ = reattachedPeer.Close()
	})
	req.tunLinked = true
	req.tun = reattachedTun
	reprepared := runManagedAmnesiaReconcile(t, node, &req)
	if !reprepared.prepared || len(recorder.negotiations) != 2 {
		t.Fatalf("TUN reattach=%#v negotiations=%d", reprepared, len(recorder.negotiations))
	}
	if recorder.privateKeys[1] == privateKey {
		t.Fatal("TUN reattach reused the detached profile key")
	}
}

func TestManagedAmnesiaRuntimeKeepsProfileAcrossCarrierReplacement(t *testing.T) {
	firstNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = firstNetwork.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})

	recorder := &managedAmnesiaClientRecorder{
		t:       t,
		peerKey: amnesiaTestPrivateKey(t).PublicKey(),
	}
	node := &managedAmnesiaNode{
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "61b8e30f4a9d725c"),
		}},
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)
	req := managedAmnesiaRuntimeRequest{
		config: node.config, tunLinked: true, tun: tunSource,
		networkLinked: true, network: firstNetwork,
		nodeName: "Amnesia", ctx: context.Background(),
	}

	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !prepared.prepared || len(recorder.negotiations) != 1 {
		t.Fatalf("initial reconcile=%#v negotiations=%d", prepared, len(recorder.negotiations))
	}
	runtimeID := prepared.runtimeID
	privateKey := recorder.privateKeys[0]

	// A carrier wrapper replacement during startup must not register a second
	// key. The first connection has not become active yet.
	secondNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = secondNetwork.Close() })
	req.network = secondNetwork
	replaced := runManagedAmnesiaReconcile(t, node, &req)
	if !replaced.prepared || len(recorder.negotiations) != 1 || replaced.runtimeID != runtimeID {
		t.Fatalf("startup carrier replacement=%#v negotiations=%d", replaced, len(recorder.negotiations))
	}
	if !node.markRuntimeTunConfigured(prepared.runtimeID) {
		t.Fatal("prepared runtime could not be marked as TUN-configured")
	}
	activated := runManagedAmnesiaReconcile(t, node, &req)
	if !activated.ready || node.runtimeDevice == nil {
		t.Fatalf("initial activation=%#v", activated)
	}
	firstDevice := node.runtimeDevice
	if firstDevice.PrivateKey() != privateKey {
		t.Fatal("startup carrier replacement changed the registered profile key")
	}

	// A linked network can also become temporarily unavailable after the
	// connection is active. This is common when a nested tunnel replaces one of
	// its wrappers. Close the old bind and device, but retain the registration.
	req.network = &downNetwork{NetworkCloser: secondNetwork}
	waiting := runManagedAmnesiaReconcile(t, node, &req)
	if !waiting.waitingForNetwork || waiting.ready || len(recorder.negotiations) != 1 ||
		node.runtimeConfig == nil {
		t.Fatalf("linked carrier gap=%#v negotiations=%d", waiting, len(recorder.negotiations))
	}
	if node.runtimeDevice != nil {
		t.Fatal("linked carrier gap retained a device bound to the unavailable network")
	}
	select {
	case <-firstDevice.Wait():
	default:
		t.Fatal("linked carrier gap left the old data-plane device open")
	}

	thirdNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = thirdNetwork.Close() })
	req.network = thirdNetwork
	reactivated := runManagedAmnesiaReconcile(t, node, &req)
	if !reactivated.ready || len(recorder.negotiations) != 1 || node.runtimeDevice == nil {
		t.Fatalf("linked carrier recovery=%#v negotiations=%d", reactivated, len(recorder.negotiations))
	}
	if node.runtimeID != runtimeID || node.runtimeDevice.PrivateKey() != privateKey {
		t.Fatal("linked carrier recovery changed the registered profile identity")
	}
	recoveredDevice := node.runtimeDevice

	// An explicit carrier detach after activation ends the connection. A later
	// attach must import a new profile and rotate its ephemeral identity.
	req.networkLinked = false
	req.network = nil
	detached := runManagedAmnesiaReconcile(t, node, &req)
	if detached.ready || len(recorder.negotiations) != 1 || node.runtimeConfig != nil {
		t.Fatalf("carrier detach=%#v negotiations=%d", detached, len(recorder.negotiations))
	}
	if node.runtimeDevice != nil {
		t.Fatal("carrier detach retained its data-plane device")
	}
	select {
	case <-recoveredDevice.Wait():
	default:
		t.Fatal("carrier detach left the data-plane device open")
	}

	fourthNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = fourthNetwork.Close() })
	req.networkLinked = true
	req.network = fourthNetwork
	recovered := runManagedAmnesiaReconcile(t, node, &req)
	if !recovered.prepared || len(recorder.negotiations) != 2 || node.runtimeDevice != nil {
		t.Fatalf("carrier recovery=%#v negotiations=%d", recovered, len(recorder.negotiations))
	}
	if recorder.privateKeys[1] == privateKey {
		t.Fatal("carrier recovery reused the previous profile key")
	}
}

func TestManagedAmnesiaRuntimeReattachesTunAfterStaleActivation(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})

	recorder := &managedAmnesiaClientRecorder{
		t:       t,
		peerKey: amnesiaTestPrivateKey(t).PublicKey(),
	}
	logFactory := logging.NewDebugLogFactory(1024)
	node := &managedAmnesiaNode{
		Base: nodekit.Base{Logger: logFactory.NodeLogger(42, NodeTypeAmnesia)},
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "d4a7601e9c3b825f"),
		}},
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)
	req := managedAmnesiaRuntimeRequest{
		config: node.config, tunLinked: true, tun: tunSource, network: network,
		nodeName: "Amnesia", ctx: context.Background(),
	}

	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !node.markRuntimeTunConfigured(prepared.runtimeID) {
		t.Fatal("prepared runtime could not be marked as TUN-configured")
	}
	privateKey := recorder.privateKeys[0]
	staleTun := &invalidateOnMTUTun{
		Tun: tunSource,
		invalidate: func() {
			node.generation.Add(1)
		},
	}
	req.tun = staleTun
	req.generation = node.generation.Add(1)
	if result, ok := node.reconcile(req); ok {
		t.Fatalf("stale activation returned a result: %#v", result)
	}
	if node.runtimeDevice != nil || node.runtimeConfig == nil || node.runtimeTunSource != nil {
		t.Fatal("stale activation did not discard only its data plane")
	}

	replacementTun, replacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = replacementTun.Close()
		_ = replacementPeer.Close()
	})
	req.tun = replacementTun
	activated := runManagedAmnesiaReconcile(t, node, &req)
	if !activated.ready || len(recorder.negotiations) != 1 {
		t.Fatalf("replacement activation=%#v negotiations=%d", activated, len(recorder.negotiations))
	}
	activeDevice := node.runtimeDevice
	if activeDevice == nil || activeDevice.PrivateKey() != privateKey {
		t.Fatal("replacement activation changed the registered profile key")
	}
	runtimeID := node.runtimeID

	currentFailingTun := &invalidateAndFailMTUTun{Tun: replacementTun}
	req.tun = currentFailingTun
	req.generation = node.generation.Add(1)
	failed, ok := node.reconcile(req)
	if !ok || failed.err == nil {
		t.Fatalf("current TUN setup failure result=%#v current=%t", failed, ok)
	}
	if node.runtimeConfig == nil || node.runtimeID != runtimeID || len(recorder.negotiations) != 1 {
		t.Fatal("current TUN setup failure discarded the registered profile")
	}
	select {
	case <-activeDevice.Wait():
	default:
		t.Fatal("current TUN setup failure left the active device open")
	}
	// Do not apply the error result. A new graph event can advance the
	// generation before the event loop receives it. The cleanup above must be
	// safe even though its request was current when it returned.
	recoveryTun, recoveryPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = recoveryTun.Close()
		_ = recoveryPeer.Close()
	})
	req.tun = recoveryTun
	recovered := runManagedAmnesiaReconcile(t, node, &req)
	if !recovered.ready || len(recorder.negotiations) != 1 || node.runtimeDevice == nil {
		t.Fatalf("current failure recovery=%#v negotiations=%d", recovered, len(recorder.negotiations))
	}
	if node.runtimeDevice.PrivateKey() != privateKey {
		t.Fatal("current failure recovery changed the registered profile key")
	}
	logs := drainAmnesiaCaptchaLogs(logFactory)
	if count := strings.Count(logs, "Amnesia connection created:"); count != 1 {
		t.Fatalf("connection-created log count = %d, want 1\nlogs:\n%s", count, logs)
	}
}

func TestManagedAmnesiaRuntimeKeepsProfileAfterStaleTunSetupFailure(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	tunSource, tunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = tunSource.Close()
		_ = tunPeer.Close()
	})

	recorder := &managedAmnesiaClientRecorder{
		t:       t,
		peerKey: amnesiaTestPrivateKey(t).PublicKey(),
	}
	node := &managedAmnesiaNode{
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "95e2c70a4f1d638b"),
		}},
		config: managedAmnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)
	req := managedAmnesiaRuntimeRequest{
		config: node.config, tunLinked: true, tun: tunSource, network: network,
		nodeName: "Amnesia", ctx: context.Background(),
	}

	prepared := runManagedAmnesiaReconcile(t, node, &req)
	if !node.markRuntimeTunConfigured(prepared.runtimeID) {
		t.Fatal("prepared runtime could not be marked as TUN-configured")
	}
	runtimeID := node.runtimeID
	privateKey := recorder.privateKeys[0]
	failingTun := &invalidateAndFailMTUTun{
		Tun: tunSource,
		invalidate: func() {
			node.generation.Add(1)
		},
	}
	req.tun = failingTun
	req.generation = node.generation.Add(1)
	if result, ok := node.reconcile(req); ok {
		t.Fatalf("stale TUN setup returned a result: %#v", result)
	}
	if node.runtimeConfig == nil || node.runtimeID != runtimeID || len(recorder.negotiations) != 1 {
		t.Fatalf(
			"stale TUN setup discarded profile: runtime=%d profile=%t negotiations=%d",
			node.runtimeID, node.runtimeConfig != nil, len(recorder.negotiations),
		)
	}

	replacementTun, replacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = replacementTun.Close()
		_ = replacementPeer.Close()
	})
	req.tun = replacementTun
	activated := runManagedAmnesiaReconcile(t, node, &req)
	if !activated.ready || len(recorder.negotiations) != 1 {
		t.Fatalf("replacement activation=%#v negotiations=%d", activated, len(recorder.negotiations))
	}
	if node.runtimeDevice == nil || node.runtimeDevice.PrivateKey() != privateKey {
		t.Fatal("replacement activation changed the registered profile key")
	}
}

func runManagedAmnesiaReconcile(
	t *testing.T,
	node *managedAmnesiaNode,
	req *managedAmnesiaRuntimeRequest,
) managedAmnesiaRuntimeResult {
	t.Helper()
	req.generation = node.generation.Add(1)
	result, ok := node.reconcile(*req)
	if !ok {
		t.Fatal("managed reconcile returned stale")
	}
	if result.err != nil {
		t.Fatalf("managed reconcile: %v", result.err)
	}
	return result
}

type managedAmnesiaClientRecorder struct {
	t *testing.T

	peerKey      wgdevice.NoisePublicKey
	options      []wgoamnesia.ClientOptions
	privateKeys  []wgdevice.NoisePrivateKey
	negotiations []wgoamnesia.NegotiationOptions
	dnsByCountry map[string][]string
	onImport     func()
}

type readyThenDownTun struct {
	tun.Tun
	isUpCalls int
}

type downNetwork struct {
	nodekit.NetworkCloser
}

func (*downNetwork) IsUp() (bool, error) {
	return false, nil
}

func (t *readyThenDownTun) IsUp() (bool, error) {
	t.isUpCalls++
	return t.isUpCalls == 1, nil
}

type invalidateOnMTUTun struct {
	tun.Tun
	once       sync.Once
	invalidate func()
}

func (t *invalidateOnMTUTun) MTU() (int, error) {
	t.once.Do(t.invalidate)
	return t.Tun.MTU()
}

type invalidateAndFailMTUTun struct {
	tun.Tun
	once       sync.Once
	invalidate func()
}

func (t *invalidateAndFailMTUTun) MTU() (int, error) {
	if t.invalidate != nil {
		t.once.Do(t.invalidate)
	}
	return 0, errors.New("forced TUN MTU failure")
}

func (r *managedAmnesiaClientRecorder) factory(options wgoamnesia.ClientOptions) (Client, error) {
	if options.Device != nil {
		return nil, errors.New("managed client received a device before negotiation")
	}
	r.options = append(r.options, options)
	return &recordingManagedAmnesiaClient{owner: r}, nil
}

type recordingManagedAmnesiaClient struct {
	owner *managedAmnesiaClientRecorder
}

func (c *recordingManagedAmnesiaClient) ImportNonInteractive(
	_ context.Context,
	_ string,
	options wgoamnesia.NegotiationOptions,
) (*wgoamnesia.Profile, error) {
	privateKey, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	c.owner.privateKeys = append(c.owner.privateKeys, privateKey)
	c.owner.negotiations = append(c.owner.negotiations, options)
	profile := amnesiaTestProfile(privateKey.PublicKey(), c.owner.peerKey, options.ServerCountryCode)
	profile.Interface.PrivateKey = privateKey.Base64()
	if servers, ok := c.owner.dnsByCountry[options.ServerCountryCode]; ok {
		profile.Interface.DNS = append([]string(nil), servers...)
	}
	if c.owner.onImport != nil {
		onImport := c.owner.onImport
		c.owner.onImport = nil
		onImport()
	}
	return profile, nil
}
