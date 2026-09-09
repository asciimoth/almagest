package amnesianodes

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

func TestAmnesiaServiceReconcileTracksRuntimeChanges(t *testing.T) {
	device, controller, privateKey := newAmnesiaTestController(t)
	apiNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = apiNetwork.Close() })
	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	profile := amnesiaTestProfile(privateKey.PublicKey(), peerKey, "de")
	profile.HostName = "vpn.example.test"
	secrets := &amnesiaTestSecrets{values: map[string]string{
		"connection": amnesiaTestActivationKey(t, "first"),
	}}
	recorder := &amnesiaClientRecorder{profile: profile}
	node := &amnesiaNode{
		secrets: secrets,
		config: amnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: recorder.factory,
	}
	t.Cleanup(node.stopRuntime)

	req := amnesiaRuntimeRequest{
		config: node.config, controller: controller, network: apiNetwork,
		tlsConfig:   &tls.Config{ServerName: "gateway.test", MinVersion: tls.VersionTLS13},
		tlsRevision: 1, nodeName: "Amnesia", ctx: context.Background(),
	}
	result := runAmnesiaReconcile(t, node, &req)
	if !result.ready || !result.requiresNetwork || result.format != wgoamnesia.InputFormatServiceKey {
		t.Fatalf("first reconcile result = %#v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("client imports = %d, want 1", recorder.count())
	}
	assertAmnesiaPeer(t, device, peerKey, true)
	firstUUID := recorder.option(0).Metadata.InstallationUUID
	if firstUUID == "" {
		t.Fatal("client received an empty installation UUID")
	}
	if result.installationUUID != firstUUID {
		t.Fatalf("status installation UUID = %q, want %q", result.installationUUID, firstUUID)
	}

	// An equivalent reconcile must keep the live tracked peer and must not
	// negotiate another profile.
	result = runAmnesiaReconcile(t, node, &req)
	if recorder.count() != 1 {
		t.Fatalf("equivalent reconcile imports = %d, want 1", recorder.count())
	}
	if result.installationUUID != firstUUID {
		t.Fatalf("reused installation UUID = %q, want %q", result.installationUUID, firstUUID)
	}

	secrets.values["connection"] = amnesiaTestActivationKey(t, "second")
	runAmnesiaReconcile(t, node, &req)
	if recorder.count() != 2 {
		t.Fatalf("changed connection secret imports = %d, want 2", recorder.count())
	}
	if got := recorder.option(1).Metadata.InstallationUUID; got != firstUUID {
		t.Fatalf("installation UUID changed from %q to %q", firstUUID, got)
	}

	req.tlsRevision++
	req.tlsConfig = &tls.Config{ServerName: "replacement.test", MinVersion: tls.VersionTLS13}
	runAmnesiaReconcile(t, node, &req)
	if recorder.count() != 3 {
		t.Fatalf("changed TLS config imports = %d, want 3", recorder.count())
	}
	transport, ok := recorder.option(2).HTTPClient.Transport.(interface{ CloseIdleConnections() })
	if !ok || transport == nil {
		t.Fatal("Amnezia client did not receive a dedicated HTTP transport")
	}

	req.config.serverCountryCode = "nl"
	runAmnesiaReconcile(t, node, &req)
	if recorder.count() != 4 || recorder.negotiation(3).ServerCountryCode != "nl" {
		t.Fatalf("country update imports=%d options=%#v", recorder.count(), recorder.negotiation(3))
	}

	// A service profile cannot remain installed without its API network.
	req.network = nil
	result = runAmnesiaReconcile(t, node, &req)
	if result.ready || !result.requiresNetwork || result.err != nil {
		t.Fatalf("detached API network result = %#v", result)
	}
	assertAmnesiaPeer(t, device, peerKey, false)

	req.network = apiNetwork
	result = runAmnesiaReconcile(t, node, &req)
	if !result.ready || recorder.count() != 5 {
		t.Fatalf("reattached API network result=%#v imports=%d", result, recorder.count())
	}
	assertAmnesiaPeer(t, device, peerKey, true)

	// Detaching the controller removes only this controller's tracked peer.
	req.controller = nil
	result = runAmnesiaReconcile(t, node, &req)
	if result.wgReady || result.ready {
		t.Fatalf("detached controller result = %#v", result)
	}
	assertAmnesiaPeer(t, device, peerKey, false)

	replacement := wgdevice.DetachDevice(device)
	req.controller = replacement
	result = runAmnesiaReconcile(t, node, &req)
	if !result.ready || recorder.count() != 6 {
		t.Fatalf("reattached controller result=%#v imports=%d", result, recorder.count())
	}
	assertAmnesiaPeer(t, device, peerKey, true)
}

func TestAmnesiaSetsInterfaceAddressesBeforeTrackedPeerUpsert(t *testing.T) {
	device, controller, privateKey := newAmnesiaTestController(t)
	apiNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = apiNetwork.Close() })
	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	profile := amnesiaTestProfile(privateKey.PublicKey(), peerKey, "de")
	node := &amnesiaNode{
		secrets: &amnesiaTestSecrets{values: map[string]string{
			"connection": amnesiaTestActivationKey(t, "ordered"),
		}},
		config: amnesiaConfig{
			enabled: true, connectionSecret: "connection", serverCountryCode: "de",
		},
		newClient: (&amnesiaClientRecorder{profile: profile}).factory,
	}
	t.Cleanup(node.stopRuntime)

	interfaceConfigApplied := false
	req := amnesiaRuntimeRequest{
		config: node.config, controller: controller, network: apiNetwork,
		nodeName: "Amnesia", ctx: context.Background(),
		setInterfaceConfig: func(_ context.Context, generation uint64, addresses, routes []string, dns string, mtu int) error {
			if generation != node.generation.Load() {
				t.Fatalf("interface config generation = %d, want %d", generation, node.generation.Load())
			}
			if !slices.Equal(addresses, profile.Interface.Addresses) {
				t.Fatalf("interface addresses = %v, want %v", addresses, profile.Interface.Addresses)
			}
			if !slices.Equal(routes, profile.Peer.AllowedIPs) {
				t.Fatalf("interface routes = %v, want %v", routes, profile.Peer.AllowedIPs)
			}
			if dns != profile.Interface.DNS[0] {
				t.Fatalf("interface DNS = %q, want %q", dns, profile.Interface.DNS[0])
			}
			if mtu != profile.Interface.MTU {
				t.Fatalf("interface MTU = %d, want %d", mtu, profile.Interface.MTU)
			}
			if _, found := device.PeerSpec(peerKey); found {
				t.Fatal("tracked peer exists before interface addresses are applied")
			}
			interfaceConfigApplied = true
			return nil
		},
	}
	result := runAmnesiaReconcile(t, node, &req)
	if !result.ready || !interfaceConfigApplied {
		t.Fatalf("reconcile result = %#v, interface config applied = %t", result, interfaceConfigApplied)
	}
	assertAmnesiaPeer(t, device, peerKey, true)
}

func TestAmnesiaNativeConfigDoesNotRequireAPIConnection(t *testing.T) {
	device, controller, privateKey := newAmnesiaTestController(t)
	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	input := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.30.0.2/32

[Peer]
PublicKey = %s
Endpoint = 127.0.0.1:51820
AllowedIPs = 0.0.0.0/0
`, privateKey.Base64(), peerKey.Base64())
	node := &amnesiaNode{
		secrets:   &amnesiaTestSecrets{values: map[string]string{"native": input}},
		config:    amnesiaConfig{enabled: true, connectionSecret: "native"},
		newClient: func(wgoamnesia.ClientOptions) (Client, error) { return nil, errors.New("unexpected client creation") },
	}
	t.Cleanup(node.stopRuntime)
	req := amnesiaRuntimeRequest{config: node.config, controller: controller, nodeName: "Amnesia", ctx: context.Background()}
	result := runAmnesiaReconcile(t, node, &req)
	if !result.ready || result.requiresNetwork || result.format != wgoamnesia.InputFormatNativeConfig {
		t.Fatalf("native reconcile result = %#v", result)
	}
	assertAmnesiaPeer(t, device, peerKey, true)
	firstSession := node.runtimeSession

	apiNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = apiNetwork.Close() })
	req.network = apiNetwork
	runAmnesiaReconcile(t, node, &req)
	if node.runtimeSession != firstSession {
		t.Fatal("API network attachment rebuilt a native-config session")
	}
	req.network = nil
	runAmnesiaReconcile(t, node, &req)
	if node.runtimeSession != firstSession {
		t.Fatal("API network detachment rebuilt a native-config session")
	}
}

func TestAmnesiaDebugLogsIncludeUUIDAndRedactSecrets(t *testing.T) {
	_, controller, privateKey := newAmnesiaTestController(t)
	apiNetwork := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = apiNetwork.Close() })

	peerKey := amnesiaTestPrivateKey(t).PublicKey()
	profile := amnesiaTestProfile(privateKey.PublicKey(), peerKey, "de")
	profile.HostName = "vpn.example.test"
	profile.Interface.PrivateKey = privateKey.Base64()
	presharedKey := amnesiaTestPrivateKey(t).Base64()
	profile.Peer.PresharedKey = presharedKey

	connectionValue := amnesiaTestActivationKey(t, "api-secret-value")
	gatewayKeyValue := "gateway-public-key-secret-value"
	secrets := &amnesiaTestSecrets{values: map[string]string{
		"connection-name":  connectionValue,
		"gateway-key-name": gatewayKeyValue,
	}}
	recorder := &amnesiaClientRecorder{profile: profile}
	logFactory := logging.NewDebugLogFactory(256)
	node := &amnesiaNode{
		Base:    nodekit.Base{Logger: logFactory.NodeLogger(42, NodeTypeAmnesiaCtrl)},
		secrets: secrets,
		config: amnesiaConfig{
			enabled: true, connectionSecret: "connection-name", serverCountryCode: "de",
			gatewayURL: "https://user:gateway-password@gateway.test/", gatewayPublicKeySecret: "gateway-key-name",
		},
		newClient: recorder.factory,
	}
	req := amnesiaRuntimeRequest{
		config: node.config, controller: controller, network: apiNetwork,
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS13}, nodeName: "Amnesia", ctx: context.Background(),
	}
	result := runAmnesiaReconcile(t, node, &req)
	node.stopRuntime()

	var messages strings.Builder
	for _, entry := range logFactory.Drain() {
		messages.WriteString(entry.Message)
		messages.WriteByte('\n')
	}
	logs := messages.String()
	for _, expected := range []string{
		result.installationUUID,
		peerKey.Base64(),
		"API client state changed",
		"API client profile received",
		`name="test-profile"`,
		`hostname="vpn.example.test"`,
		`endpoint_host="127.0.0.1"`,
		`country="de"`,
		"tracked peer upsert complete",
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("debug logs do not contain %q\nlogs:\n%s", expected, logs)
		}
	}
	for _, secret := range []string{
		connectionValue,
		privateKey.Base64(),
		presharedKey,
		gatewayKeyValue,
		"gateway-password",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("debug logs expose secret %q", secret)
		}
	}
}

func TestAmnesiaWGDebugSummaryRedactsHeaderProtectionKey(t *testing.T) {
	rawKey := bytes.Repeat([]byte{0xa7}, 32)
	var key wgdevice.AmneziaWGHeaderProtectionKey
	copy(key[:], rawKey)
	summary := amnesiaWGDebugSummary(&wgdevice.AmneziaWGConfig{HeaderProtectionKey: key})
	if !strings.Contains(summary, "header_protection_key_configured=true") {
		t.Fatalf("summary does not report configured header protection: %s", summary)
	}
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(rawKey),
		fmt.Sprintf("%x", rawKey),
	} {
		if strings.Contains(summary, encoded) {
			t.Fatalf("summary exposes header protection key %q", encoded)
		}
	}
}

func TestAmnesiaCaptchaIsAnExplicitError(t *testing.T) {
	err := amnesiaImportError(&wgoamnesia.InteractionRequiredError{
		Request: wgoamnesia.InteractionRequest{Kind: wgoamnesia.InteractionCaptcha},
	})
	if !errors.Is(err, ErrCaptchaUnsupported) {
		t.Fatalf("captcha error = %v, want %v", err, ErrCaptchaUnsupported)
	}
}

func TestAmnesiaHTTPTransportClonesTLSConfig(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	transport := amnesiaHTTPTransport(nil, tlsConfig)
	defer transport.CloseIdleConnections()
	if transport.TLSClientConfig == nil || transport.TLSClientConfig == tlsConfig {
		t.Fatal("HTTP transport did not clone the selected TLS config")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("minimum TLS version = %d, want %d", transport.TLSClientConfig.MinVersion, tls.VersionTLS13)
	}
}

func TestAmnesiaPreConnectUsesGraphNetwork(t *testing.T) {
	network := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	listener, err := network.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverErr := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer func() { _ = connection.Close() }()
		request := make([]byte, 2)
		if _, readErr := io.ReadFull(connection, request); readErr != nil {
			serverErr <- readErr
			return
		}
		if string(request) != "\xaa\xbb" {
			serverErr <- fmt.Errorf("request = %x", request)
			return
		}
		_, writeErr := connection.Write([]byte{0xcc, 0xdd})
		serverErr <- writeErr
	}()

	err = executePreConnectActions(context.Background(), network, []wgoamnesia.PreConnectAction{{
		Protocol: "tcp", Endpoint: listener.Addr().String(), Timeout: time.Second,
		PayloadSpec: "<b 0xaabb>", ExpectedResponseSpec: "<b 0xccdd>",
	}})
	if err != nil {
		t.Fatalf("execute pre-connect action: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("pre-connect server: %v", err)
	}
}

func runAmnesiaReconcile(t *testing.T, node *amnesiaNode, req *amnesiaRuntimeRequest) amnesiaRuntimeResult {
	t.Helper()
	req.generation = node.generation.Add(1)
	result, ok := node.reconcile(*req)
	if !ok {
		t.Fatal("reconcile returned a stale result")
	}
	if result.err != nil {
		t.Fatalf("reconcile: %v", result.err)
	}
	return result
}

func assertAmnesiaPeer(t *testing.T, device *wgdevice.Device, key wgdevice.NoisePublicKey, want bool) {
	t.Helper()
	spec, found := device.PeerSpec(key)
	if found != want {
		t.Fatalf("peer found = %t, want %t", found, want)
	}
	if found && (spec.Endpoint == nil || spec.Endpoint.Transport != wgdevice.DefaultTransportID) {
		t.Fatalf("peer endpoint = %#v, want WireGuard default transport", spec.Endpoint)
	}
}

func amnesiaTestActivationKey(t *testing.T, apiKey string) string {
	t.Helper()
	encoded, err := wgoamnesia.EncodeActivationKey(wgoamnesia.ActivationKey{
		Name: "test", ConfigVersion: 2,
		APIConfig: wgoamnesia.ActivationAPIConfig{ServiceType: "vpn", ServiceProtocol: "awg"},
		AuthData:  json.RawMessage(fmt.Sprintf(`{"api_key":%q}`, apiKey)),
	})
	if err != nil {
		t.Fatalf("encode activation key: %v", err)
	}
	return encoded
}

type amnesiaTestSecrets struct {
	values map[string]string
}

func (s *amnesiaTestSecrets) GetSecret(key, _ string) (storage.Secret, error) {
	value, ok := s.values[key]
	if !ok {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	return storage.Secret{Value: value}, nil
}

func (*amnesiaTestSecrets) NewSecret(storage.Secret, string) (string, error) {
	return "", errors.New("not implemented")
}

type amnesiaClientRecorder struct {
	mu           sync.Mutex
	profile      *wgoamnesia.Profile
	options      []wgoamnesia.ClientOptions
	negotiations []wgoamnesia.NegotiationOptions
}

func (r *amnesiaClientRecorder) factory(options wgoamnesia.ClientOptions) (Client, error) {
	r.mu.Lock()
	r.options = append(r.options, options)
	r.mu.Unlock()
	return amnesiaRecordingClient{owner: r}, nil
}

func (r *amnesiaClientRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.negotiations)
}

func (r *amnesiaClientRecorder) option(index int) wgoamnesia.ClientOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.options[index]
}

func (r *amnesiaClientRecorder) negotiation(index int) wgoamnesia.NegotiationOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.negotiations[index]
}

type amnesiaRecordingClient struct {
	owner *amnesiaClientRecorder
}

func (c amnesiaRecordingClient) ImportNonInteractive(_ context.Context, _ string, options wgoamnesia.NegotiationOptions) (*wgoamnesia.Profile, error) {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	c.owner.negotiations = append(c.owner.negotiations, options)
	copy := *c.owner.profile
	return &copy, nil
}
