package amnesianodes

import (
	"context"
	"errors"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func TestAmnesiaClassHasOnlyManagedPortsAndMenuFields(t *testing.T) {
	class := AmnesiaClass{}
	if NodeTypeAmnesia != "almagest/Amnesia" || class.ClassName() != NodeTypeAmnesia {
		t.Fatalf("Amnesia class name = %q", class.ClassName())
	}
	params := class.DefaultNodeParams()
	if params.PrimaryType != wireguardnodes.TypeWGCtrl || len(params.InitialPorts) != 2 {
		t.Fatalf("default params = %#v", params)
	}
	if port := params.InitialPorts[0]; port.Direction != "left" || port.Name != "Tun" || len(port.Types) != 1 || port.Types[0] != nodekit.TypeTun {
		t.Fatalf("TUN port = %#v", port)
	}
	if port := params.InitialPorts[1]; port.Direction != "right" || port.Name != "Network" || len(port.Types) != 1 || port.Types[0] != nodekit.TypeNetwork {
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
	if err := node.OnInit(nil, nil, 0, "Amnesia Service", nil, false, false, false, false); err != nil {
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
		"connection": amnesiaTestActivationKey(t, "managed"),
	}}
	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	recorder := &managedAmnesiaClientRecorder{t: t, peerKey: peerKey}
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
			"connection": amnesiaTestActivationKey(t, "stale-managed"),
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
	onImport     func()
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
	if c.owner.onImport != nil {
		onImport := c.owner.onImport
		c.owner.onImport = nil
		onImport()
	}
	return profile, nil
}
