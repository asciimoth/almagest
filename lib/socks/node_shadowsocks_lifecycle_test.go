package socksnodes

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

func TestShadowsocksServerRestartWaitsForPreviousWorker(t *testing.T) {
	spawner := &localProxyTestSpawner{}
	stopSeen := make(chan struct{})
	release := make(chan struct{})
	old, err := nodekit.NewRestartableWorker(spawner, func(stop <-chan struct{}) {
		<-stop
		close(stopSeen)
		<-release
	}, "old Shadowsocks server")
	if err != nil {
		t.Fatal(err)
	}
	logFactory := newTestLogFactory()
	w := pasta.NewWorkspace(logFactory)
	t.Cleanup(w.Close)
	transport := gonnect.NewLoopbackNetwork()
	t.Cleanup(func() { _ = transport.Close() })
	listen := gonnect.DetachNetwork(transport, nil, nil)
	proxy := gonnect.DetachNetwork(transport, nil, nil)
	replay := newShadowsocksReplayGuard()
	t.Cleanup(func() { _ = listen.Close(); _ = proxy.Close() })
	n := &shadowsocksServerNode{
		NodeBase:      NodeBase{Logger: logFactory.NodeLogger(1, NodeTypeShadowsocksServer), W: w, ID: 1, WorkerSpawner: spawner},
		config:        shadowsocksConfig{address: net.JoinHostPort("127.0.0.1", reserveShadowsocksPort(t)), method: DefaultShadowsocksMethod, password: "test", tcp: true},
		listenNetwork: listen, proxyNetwork: proxy, worker: old, replay: replay,
	}
	n.restart()
	select {
	case <-stopSeen:
	case <-time.After(time.Second):
		t.Fatal("old worker did not receive stop")
	}
	n.restart()
	if got := spawner.Count("Shadowsocks server"); got != 0 || n.worker != nil || n.stopping != old {
		t.Fatalf("replacement started before old worker stopped: count=%d", got)
	}
	if err := n.OnInbox(pasta.InboxMessage{Payload: shadowsocksServerStatus{generation: n.generation - 1, ready: true}}); err != nil || n.ready {
		t.Fatalf("stale status changed current readiness: err=%v ready=%t", err, n.ready)
	}
	close(release)
	select {
	case <-old.Done():
	case <-time.After(time.Second):
		t.Fatal("old worker did not stop")
	}
	if err := n.OnInbox(pasta.InboxMessage{Payload: shadowsocksServerStopped{old}}); err != nil {
		t.Fatal(err)
	}
	if got := spawner.Count("Shadowsocks server"); got != 1 || n.worker == nil || n.stopping != nil || n.replay != replay {
		t.Fatalf("replacement did not start after old worker stopped: count=%d", got)
	}
	replacement := n.worker
	n.stopWorker()
	_ = listen.Close()
	_ = proxy.Close()
	select {
	case <-replacement.Done():
	case <-time.After(time.Second):
		t.Fatal("replacement worker did not stop")
	}
}

type shadowsocksTestRuntime struct{ *testRuntime }

func (*shadowsocksTestRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return testWorkerSpawner{}
}
func shadowsocksApply(w *pasta.Workspace, node uint64, values map[string]any) {
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)}, BlockID: "config", Values: values})
}
func shadowsocksLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	last := "<missing>"
	for time.Now().Before(deadline) {
		if snapshot, ok := w.NodeSnapshot(node); ok {
			last = snapshot.Label
			if last == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("node %d label = %q, want %q", node, last, want)
}

func settledShadowsocksPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	latest := networktest.ReceivePayload(t, events)
	quiet := time.NewTimer(50 * time.Millisecond)
	defer quiet.Stop()
	for {
		select {
		case next := <-events:
			latest = next
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(50 * time.Millisecond)
		case <-quiet.C:
			if !NetworkIsReady(latest.Network) {
				t.Fatal("last Shadowsocks output was not ready")
			}
			return latest
		}
	}
}
func TestShadowsocksNodesLinkLifecycleAndMenu(t *testing.T) {
	runtime := &shadowsocksTestRuntime{testRuntime: &testRuntime{}}
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	for _, class := range []pasta.NodeClass{ShadowsocksClientClass{Runtime: runtime}, ShadowsocksServerClass{Runtime: runtime}, testNativeNetworkClass{}} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatal(err)
		}
	}
	source, err := w.AddNodeByClass(testNativeNetworkNodeType, "source")
	if err != nil {
		t.Fatal(err)
	}
	server, err := w.AddNodeByClass(NodeTypeShadowsocksServer, "server")
	if err != nil {
		t.Fatal(err)
	}
	client, err := w.AddNodeByClass(NodeTypeShadowsocksClient, "client")
	if err != nil {
		t.Fatal(err)
	}
	receiver := networktest.NewReceiver(32)
	if _, err := w.AddNode(receiver, "test/ShadowsocksReceiver", "receiver"); err != nil {
		t.Fatal(err)
	}
	sourcePort := testkit.MustNodeSnapshot(t, w, source).LeftPorts[0]
	serverPorts := testkit.MustNodeSnapshot(t, w, server).RightPorts
	clientPorts := testkit.MustNodeSnapshot(t, w, client)
	port := reserveShadowsocksPort(t)
	address := net.JoinHostPort("127.0.0.1", port)
	values := map[string]any{"address": address, "method": "aes-256-gcm", "password": "menu-secret", "tcp": true, "udp": true}
	shadowsocksApply(w, server, values)
	shadowsocksApply(w, client, values)
	if _, _, err := w.AddLink(clientPorts.LeftPorts[0], receiver.In); err != nil {
		t.Fatal(err)
	}
	networktest.AssertNoEvent(t, receiver.Events)
	shadowsocksLabel(t, w, client, "waiting for out")
	listenLink, _, err := w.AddLink(sourcePort, serverPorts[0])
	if err != nil {
		t.Fatal(err)
	}
	shadowsocksLabel(t, w, server, "waiting for proxy")
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("server listened without a proxy Network")
	}
	proxyLink, _, err := w.AddLink(sourcePort, serverPorts[1])
	if err != nil {
		t.Fatal(err)
	}
	shadowsocksLabel(t, w, server, address)
	clientLink, _, err := w.AddLink(sourcePort, clientPorts.RightPorts[0])
	if err != nil {
		t.Fatal(err)
	}
	payload := settledShadowsocksPayload(t, receiver.Events)
	assertShadowsocksPayload(t, payload.Network, address)
	w.RemoveLink(proxyLink)
	shadowsocksLabel(t, w, server, "waiting for proxy")
	w.RemoveLink(clientLink)
	shadowsocksLabel(t, w, client, "waiting for out")
	testkit.Eventually(t, func() bool { return !NetworkIsReady(payload.Network) }, "old output closure")
	if _, err := payload.Network.Dial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("old output still dials")
	}
	if _, _, err := w.AddLink(sourcePort, clientPorts.RightPorts[0]); err != nil {
		t.Fatal(err)
	}
	replacement := settledShadowsocksPayload(t, receiver.Events)
	assertShadowsocksPayload(t, replacement.Network, address)
	proxyLink, _, err = w.AddLink(sourcePort, serverPorts[1])
	if err != nil {
		t.Fatal(err)
	}
	shadowsocksLabel(t, w, server, address)
	w.RemoveLink(listenLink)
	shadowsocksLabel(t, w, server, "waiting for listen")
	w.RemoveLink(proxyLink)
	if _, _, err := w.AddLink(sourcePort, serverPorts[1]); err != nil {
		t.Fatal(err)
	}
	shadowsocksLabel(t, w, server, "waiting for listen")
	if _, _, err := w.AddLink(sourcePort, serverPorts[0]); err != nil {
		t.Fatal(err)
	}
	shadowsocksLabel(t, w, server, address)
	// A menu update must replace the client output and invalidate the old one.
	shadowsocksApply(w, client, map[string]any{})
	shadowsocksApply(w, server, map[string]any{})
	if !NetworkIsReady(replacement.Network) {
		t.Fatal("unchanged menu apply closed the output")
	}
	networktest.AssertNoEvent(t, receiver.Events)
	shadowsocksApply(w, client, map[string]any{"method": "chacha20-ietf-poly1305"})
	changed := settledShadowsocksPayload(t, receiver.Events)
	if NetworkIsReady(replacement.Network) {
		t.Fatal("menu update kept old output ready")
	}
	if changed.Network == nil {
		t.Fatal("menu update did not emit a replacement")
	}
	shadowsocksApply(w, client, map[string]any{"address": "bad"})
	if !NetworkIsReady(changed.Network) {
		t.Fatal("invalid menu input closed the active output")
	}
	shadowsocksLabel(t, w, client, address)
	newAddress := address
	for newAddress == address {
		newAddress = net.JoinHostPort("127.0.0.1", reserveShadowsocksPort(t))
	}
	shadowsocksApply(w, server, map[string]any{"address": newAddress})
	shadowsocksLabel(t, w, server, newAddress)
	shadowsocksApply(w, client, map[string]any{"address": newAddress, "method": "aes-256-gcm"})
	updated := settledShadowsocksPayload(t, receiver.Events)
	shadowsocksLabel(t, w, client, newAddress)
	if NetworkIsReady(changed.Network) {
		t.Fatal("old menu output stayed ready")
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = conn.Close() }(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	conn, err := updated.Network.Dial(context.Background(), "tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 5)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if string(response) != "ready" {
		t.Fatalf("first request after menu update = %q", response)
	}
	old, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if old != nil {
		_ = old.Close()
		t.Fatal("old server listener stayed open")
	}
	if err == nil {
		t.Fatal("old server listener accepted a connection")
	}
	active, err := updated.Network.Dial(context.Background(), "tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = active.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := active.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(active, response[:1]); err != nil {
		t.Fatal(err)
	}
	w.RemoveNode(server)
	if _, err := active.Read(response[:1]); err == nil {
		t.Fatal("active stream survived server removal")
	}
	_ = active.Close()
	w.RemoveNode(client)
	testkit.Eventually(t, func() bool { return !NetworkIsReady(updated.Network) }, "removed output closure")
}
func reserveShadowsocksPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return netPort(ln.Addr())
}
func netPort(addr net.Addr) string { _, port, _ := net.SplitHostPort(addr.String()); return port }
func assertShadowsocksPayload(t *testing.T, network NetworkCloser, address string) {
	t.Helper()
	wrapper, ok := network.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("output type %T", network)
	}
	proxy, ok := wrapper.GetWrapped().(*shadowsocksNetwork)
	if !ok {
		t.Fatalf("wrapped type %T", wrapper.GetWrapped())
	}
	if proxy.address != address || proxy.IsNative() {
		t.Fatalf("proxy state = %q native=%t", proxy.address, proxy.IsNative())
	}
}
func TestShadowsocksClientNeverUsesHostWhenUpstreamIsAbsent(t *testing.T) {
	network := &gonnect.RejectNetwork{}
	client, err := newShadowsocksNetwork(network, "127.0.0.1:8388", DefaultShadowsocksMethod, "secret", true, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, networkName := range []string{"tcp", "udp", "tcp4", "udp4"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := client.Dial(ctx, networkName, "127.0.0.1:80")
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		if err == nil {
			t.Fatalf("%s escaped reject upstream", networkName)
		}
	}
}

func TestShadowsocksMenuAndSecretFallback(t *testing.T) {
	config := shadowsocksConfig{address: "127.0.0.1:8388", method: DefaultShadowsocksMethod, passwordSecret: "ss-key", tcp: true, udp: true}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	block := config.configBlock("", false)
	fields := map[string]formular.Item{}
	for _, item := range block.Items {
		fields[item.ID] = item
	}
	for _, id := range []string{"address", "method", "password", "passwordSecret", "tcp", "udp"} {
		if fields[id].Field == nil {
			t.Fatalf("missing menu field %q", id)
		}
	}
	if !fields["password"].Secret {
		t.Fatal("password is not a secret field")
	}
	if fields["passwordSecret"].Selector != nodekit.SecretNameSelector {
		t.Fatal("password secret selector missing")
	}
	if len(fields["method"].AllowedValues) != 4 {
		t.Fatalf("cipher choices = %v", fields["method"].AllowedValues)
	}
	if _, err := newShadowsocksNetwork(&gonnect.RejectNetwork{}, config.address, config.method, config.resolved(nil).password, true, true); err == nil {
		t.Fatal("unavailable password was accepted")
	}
	config.password = "fallback"
	if config.resolved(nil).password != "fallback" {
		t.Fatal("inline password fallback was lost")
	}
	for _, bad := range []shadowsocksConfig{
		{address: "bad", method: config.method, password: "fallback", tcp: true},
		{address: config.address, method: "unsupported", password: "fallback", tcp: true},
		{address: config.address, method: config.method, password: "fallback"},
	} {
		if err := bad.validate(); err == nil {
			t.Fatalf("accepted invalid config: %#v", bad)
		}
	}
}

func TestShadowsocksNodesMissingSecretFailClosed(t *testing.T) {
	runtime := &shadowsocksTestRuntime{testRuntime: &testRuntime{}}
	w := pasta.NewWorkspace(newTestLogFactory())
	defer w.Close()
	for _, class := range []pasta.NodeClass{ShadowsocksClientClass{Runtime: runtime}, ShadowsocksServerClass{Runtime: runtime}, testNativeNetworkClass{}} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatal(err)
		}
	}
	source, err := w.AddNodeByClass(testNativeNetworkNodeType, "source")
	if err != nil {
		t.Fatal(err)
	}
	server, err := w.AddNodeByClass(NodeTypeShadowsocksServer, "server")
	if err != nil {
		t.Fatal(err)
	}
	client, err := w.AddNodeByClass(NodeTypeShadowsocksClient, "client")
	if err != nil {
		t.Fatal(err)
	}
	receiver := networktest.NewReceiver(4)
	if _, err := w.AddNode(receiver, "test/ShadowsocksSecretReceiver", "receiver"); err != nil {
		t.Fatal(err)
	}
	port := reserveShadowsocksPort(t)
	address := net.JoinHostPort("127.0.0.1", port)
	values := map[string]any{"address": address, "method": DefaultShadowsocksMethod, "password": "", "passwordSecret": "missing", "tcp": true, "udp": true}
	shadowsocksApply(w, server, values)
	shadowsocksApply(w, client, values)
	out := testkit.MustNodeSnapshot(t, w, source).LeftPorts[0]
	serverPorts := testkit.MustNodeSnapshot(t, w, server).RightPorts
	clientPorts := testkit.MustNodeSnapshot(t, w, client)
	if _, _, err := w.AddLink(clientPorts.LeftPorts[0], receiver.In); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.AddLink(out, serverPorts[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.AddLink(out, serverPorts[1]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.AddLink(out, clientPorts.RightPorts[0]); err != nil {
		t.Fatal(err)
	}
	shadowsocksLabel(t, w, server, "Shadowsocks error")
	shadowsocksLabel(t, w, client, "invalid Shadowsocks config")
	networktest.AssertNoEvent(t, receiver.Events)
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("server listened with unavailable password")
	}
}
