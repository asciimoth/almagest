package lib

import (
	"context"
	"testing"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo"
)

func TestBuildSocksGoClientUsesOutNetworkAndProxiesAllAddrs(t *testing.T) {
	calls := make([]string, 0, 2)
	out := &socksRouteNetwork{name: "out", calls: &calls}

	client, err := buildSocksGoClient("socks5://proxy.example.test:1080", out)
	if err != nil {
		t.Fatalf("buildSocksGoClient error = %v, want nil", err)
	}

	if client.DoFilter("tcp", "127.0.0.1:80") {
		t.Fatal("client.DoFilter(loopback) = true, want false")
	}
	if _, err := client.GetDialer()(context.Background(), "tcp", "proxy.example.test:1080"); err != nil {
		t.Fatalf("client dialer error = %v, want nil", err)
	}

	assertStringSlice(t, calls, []string{"out.Dial tcp proxy.example.test:1080"})
}

func TestSocksClientNodeEmitsClientAfterOutNetworkUpdate(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(socksClientClass{}); err != nil {
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
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	out := mustNodeSnapshot(t, w, outID)
	client := mustNodeSnapshot(t, w, clientID)
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink out->client: %v", err)
	}

	payload := receiveOutNetPayload(t, receiver.events)
	wrapped, ok := payload.Network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("payload.Network = %T, want *gonnect.DetachedNetwork", payload.Network)
	}
	if _, ok := wrapped.GetWrapped().(*socksgo.Client); !ok {
		t.Fatalf("wrapped network = %T, want *socksgo.Client", wrapped.GetWrapped())
	}
}

func mustNodeSnapshot(t *testing.T, w *pasta.Workspace, node uint64) pasta.NodeSnapshot {
	t.Helper()
	snapshot, ok := w.NodeSnapshot(node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	return snapshot
}
