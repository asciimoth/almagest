package socksnodes

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

func TestBuildSocksGoClientUsesOutNetworkAndProxiesAllAddrs(t *testing.T) {
	calls := make([]string, 0, 2)
	out := &socksRouteNetwork{name: "out", calls: &calls}

	client, err := buildSocksGoClient("socks5://proxy.example.test:1080", out, nil, nil, "")
	if err != nil {
		t.Fatalf("buildSocksGoClient error = %v, want nil", err)
	}

	if client.DoFilter("tcp", "127.0.0.1:80") {
		t.Fatal("client.DoFilter(loopback) = true, want false")
	}
	if _, err := client.GetDialer()(context.Background(), "tcp", "proxy.example.test:1080"); err != nil {
		t.Fatalf("client dialer error = %v, want nil", err)
	}

	testkit.AssertStringSlice(t, calls, []string{"out.Dial tcp proxy.example.test:1080"})
}

func TestBuildSocksGoClientUsesTLSConfigForTLSProxy(t *testing.T) {
	client, err := buildSocksGoClient(
		"socks5+tls://proxy.example.test:1080",
		nil,
		nil,
		&tls.Config{ServerName: "policy.test", MinVersion: tls.VersionTLS12},
		"",
	)
	if err != nil {
		t.Fatalf("buildSocksGoClient error = %v, want nil", err)
	}
	if client.TLSConfig == nil || client.TLSConfig.ServerName != "policy.test" {
		t.Fatalf("client TLS config = %#v, want policy.test", client.TLSConfig)
	}
}

func TestBuildSocksGoClientReportsTLSConfigErrorOnlyForTLSProxy(t *testing.T) {
	_, err := buildSocksGoClient("socks5+tls://proxy.example.test:1080", nil, nil, nil, "policy failed")
	if err == nil || !strings.Contains(err.Error(), "policy failed") {
		t.Fatalf("TLS proxy error = %v, want policy failed", err)
	}

	if _, err := buildSocksGoClient("socks5://proxy.example.test:1080", nil, nil, nil, "policy failed"); err != nil {
		t.Fatalf("plain proxy error = %v, want nil", err)
	}
}

func TestOverwriteSocksClientPasswordReplacesURLPassword(t *testing.T) {
	client, err := buildSocksGoClient("socks5://alice:inline@proxy.example.test:1080", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildSocksGoClient error = %v, want nil", err)
	}
	if !overwriteSocksClientPassword(client, "from-secret") {
		t.Fatal("overwriteSocksClientPassword = false, want true")
	}
	auth, ok := client.Auth.Get(protocol.PassAuthCode).(*protocol.PassAuthMethod)
	if !ok || auth.User != "alice" || auth.Pass != "from-secret" {
		t.Fatalf("client auth = %#v, want alice with secret password", auth)
	}

	clientWithoutUser, err := buildSocksGoClient("socks5://proxy.example.test:1080", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildSocksGoClient without user error = %v, want nil", err)
	}
	if overwriteSocksClientPassword(clientWithoutUser, "from-secret") {
		t.Fatal("overwriteSocksClientPassword without URL user = true, want false")
	}
}

func TestSocksClientMenuOffersPasswordSecretSelector(t *testing.T) {
	node := &socksClientNode{proxyURL: DefaultSocksClientProxyURL, passwordSecret: "proxy-password"}
	block := node.configBlock()
	for _, item := range block.Items {
		if item.ID != socksClientPasswordSecretFieldID {
			continue
		}
		if item.Field == nil || item.Value != "proxy-password" || item.Selector != nodekit.SecretNameSelector {
			t.Fatalf("password secret item = %#v, want selected secret selector", item)
		}
		return
	}
	t.Fatal("password secret menu item is missing")
}

func TestSocksClientHandlesTLSConfigChangedRename(t *testing.T) {
	storage := newNodeTestTLSConfigStorage(map[string]string{
		DefaultTLSConfigName: "default.test",
		"custom":             "custom.test",
		"renamed":            "renamed.test",
	})
	node := &socksClientNode{
		NodeBase:      NodeBase{Runtime: &testRuntime{tlsConfigs: storage}},
		proxyURL:      "socks5+tls://proxy.example.test:1080",
		tlsConfigName: "custom",
	}
	node.rebuildClient()
	if node.client == nil || node.client.TLSConfig == nil || node.client.TLSConfig.ServerName != "custom.test" {
		t.Fatalf("initial client TLS config = %#v, want custom.test", node.client)
	}

	if err := node.OnInbox(pasta.InboxMessage{Payload: TlsConfigChanged{NamePrev: "custom", NameNew: "renamed"}}); err != nil {
		t.Fatalf("OnInbox error = %v, want nil", err)
	}
	if node.tlsConfigName != "renamed" {
		t.Fatalf("node TLS config name = %q, want renamed", node.tlsConfigName)
	}
	if node.client == nil || node.client.TLSConfig == nil || node.client.TLSConfig.ServerName != "renamed.test" {
		t.Fatalf("renamed client TLS config = %#v, want renamed.test", node.client)
	}
}

func TestSocksClientNodeEmitsClientAfterOutNetworkUpdate(t *testing.T) {
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	if err := w.AddNodeClass(SocksClientClass{}); err != nil {
		t.Fatalf("AddNodeClass SocksClient: %v", err)
	}
	if err := w.AddNodeClass(testNativeNetworkClass{}); err != nil {
		t.Fatalf("AddNodeClass NativeNetwork: %v", err)
	}

	outID, err := w.AddNodeByClass(testNativeNetworkNodeType, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass native: %v", err)
	}
	clientID, err := w.AddNodeByClass(NodeTypeSocksClient, "socks")
	if err != nil {
		t.Fatalf("AddNodeByClass SocksClient: %v", err)
	}
	receiver := networktest.NewReceiver(2)
	if _, err := w.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	out := testkit.MustNodeSnapshot(t, w, outID)
	client := testkit.MustNodeSnapshot(t, w, clientID)
	link, _, err := w.AddLink(client.LeftPorts[0], receiver.In)
	if err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink out->client: %v", err)
	}

	payload := networktest.ReceivePayload(t, receiver.Events)
drain:
	for {
		select {
		case payload = <-receiver.Events:
		default:
			break drain
		}
	}
	wrapped, ok := payload.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("payload.Network = %T, want *gonnect.DetachedNetwork", payload.Network)
	}
	if _, ok := wrapped.GetWrapped().(*socksgo.Client); !ok {
		t.Fatalf("wrapped network = %T, want *socksgo.Client", wrapped.GetWrapped())
	}

	w.EmitEvent(receiver.ID, link, std.RequestValue{})
	requested := networktest.ReceivePayload(t, receiver.Events)
	if !SameNetworkInstance(requested.Network, payload.Network) {
		t.Fatal("request replaced the ready SOCKS client wrapper")
	}
	if !NetworkIsReady(payload.Network) {
		t.Fatal("request closed the ready SOCKS client wrapper")
	}
}

func TestSocksClientNodeURLUpdateClosesOldWrapperAndEmitsNewClient(t *testing.T) {
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	if err := w.AddNodeClass(SocksClientClass{}); err != nil {
		t.Fatalf("AddNodeClass SocksClient: %v", err)
	}
	if err := w.AddNodeClass(testNativeNetworkClass{}); err != nil {
		t.Fatalf("AddNodeClass NativeNetwork: %v", err)
	}

	outID, err := w.AddNodeByClass(testNativeNetworkNodeType, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass native: %v", err)
	}
	clientID, err := w.AddNodeByClass(NodeTypeSocksClient, "socks")
	if err != nil {
		t.Fatalf("AddNodeByClass SocksClient: %v", err)
	}
	receiver := networktest.NewReceiver(2)
	if _, err := w.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	const gostURL = "socks5://proxy.example.test:1080?gost"
	w.SendNodeFormularMsg(clientID, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(clientID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "proxyURL"},
		Value:       gostURL,
	})

	out := testkit.MustNodeSnapshot(t, w, outID)
	client := testkit.MustNodeSnapshot(t, w, clientID)
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink out->client: %v", err)
	}

	first := networktest.ReceivePayload(t, receiver.Events)
	firstWrapper, ok := first.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("first.Network = %T, want *gonnect.DetachedNetwork", first.Network)
	}
	firstClient, ok := firstWrapper.GetWrapped().(*socksgo.Client)
	if !ok {
		t.Fatalf("first wrapped network = %T, want *socksgo.Client", firstWrapper.GetWrapped())
	}
	if !firstClient.GostMbind {
		t.Fatal("first client GostMbind = false, want true")
	}

	const goURL = "socks5://proxy.example.test:1080?go"
	w.SendNodeFormularMsg(clientID, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(clientID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "proxyURL"},
		Value:       goURL,
	})

	secondWrapper, secondClient := receiveSocksClientPayload(t, receiver.Events, func(client *socksgo.Client) bool {
		return client != firstClient && !client.GostMbind
	})
	if up, err := firstWrapper.IsUp(); err != nil || up {
		t.Fatalf("first wrapper IsUp = %v, %v; want false, nil", up, err)
	}
	if !SameComparableValue(secondWrapper.GetWrapped(), secondClient) {
		t.Fatal("second wrapper does not wrap the returned second client")
	}
}

func receiveSocksClientPayload(t *testing.T, events <-chan NetworkPayload, accept func(*socksgo.Client) bool) (*gonnect.DetachedNetwork, *socksgo.Client) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		select {
		case payload := <-events:
			wrapper, ok := payload.Network.(*gonnect.DetachedNetwork)
			if !ok {
				t.Fatalf("payload.Network = %T, want *gonnect.DetachedNetwork", payload.Network)
			}
			client, ok := wrapper.GetWrapped().(*socksgo.Client)
			if !ok {
				t.Fatalf("wrapped network = %T, want *socksgo.Client", wrapper.GetWrapped())
			}
			if accept(client) {
				return wrapper, client
			}
		default:
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for matching socks client payload")
			}
			time.Sleep(time.Millisecond)
		}
	}
}
