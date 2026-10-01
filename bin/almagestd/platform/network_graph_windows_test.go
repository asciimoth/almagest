//go:build windows

package platform

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"golang.org/x/sys/windows"
)

const windowsGraphDNSAddress = "198.18.232.1"

func TestWindowsGraphOutNetLocalNetAndWireGuardWithDefaultTun(t *testing.T) {
	engine, system, networkReceiver, _ := newWindowsNetworkGraphEngine(t)
	defaultTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeDefaultTun, "default tun")
	out := addWindowsGraphNode(t, engine, "almagest/OutNetwork", "out")
	receiver := addWindowsGraphNode(t, engine, networkReceiver.ClassName(), "network receiver")
	linkWindowsGraphPorts(t, engine, out, "left", "Network", receiver, "right", "Network")
	network := receiveWindowsNetworkPayload(t, networkReceiver.events).Network

	for _, family := range []struct {
		tcp      string
		udp      string
		wildcard string
	}{
		{tcp: "tcp4", udp: "udp4", wildcard: "0.0.0.0:0"},
		{tcp: "tcp6", udp: "udp6", wildcard: "[::]:0"},
	} {
		testWindowsGraphTCP(t, "OutNet", network, family.tcp, family.wildcard)
		testWindowsGraphUDP(t, "OutNet", network, family.udp, family.wildcard)
		testWindowsGraphTCP(t, "LocalNet", system.LocalNet(), family.tcp, family.wildcard)
		testWindowsGraphUDP(t, "LocalNet", system.LocalNet(), family.udp, family.wildcard)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if listener, err := network.ListenTCP(ctx, "tcp", ":0"); err == nil {
		_ = listener.Close()
		t.Fatal("OutNet accepted an ambiguous generic wildcard listener")
	}

	proxyPort := reserveWindowsLoopbackPort(t)
	proxy := addWindowsGraphNode(t, engine, socksnodes.NodeTypeLocalProxy, "local proxy")
	applyWindowsGraphForm(engine, proxy, "config", map[string]any{
		"socksHost": "0.0.0.0", "socksPort": proxyPort,
		"wsHost": "", "wsPort": 0, "wsPath": "/socks",
	})
	linkWindowsGraphPorts(t, engine, out, "left", "Network", proxy, "right", "proxy")
	waitWindowsGraphTCPConnect(t, system.LocalNet(), "tcp4", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if ips, err := system.LocalNet().LookupIP(ctx, "ip", "localhost"); err != nil {
		t.Fatalf("LocalNet localhost lookup: %v", err)
	} else {
		var hasIPv4, hasIPv6 bool
		for _, ip := range ips {
			if !ip.IsLoopback() {
				t.Fatalf("LocalNet localhost answer %s is not loopback", ip)
			}
			hasIPv4 = hasIPv4 || ip.To4() != nil
			hasIPv6 = hasIPv6 || ip.To4() == nil
		}
		if !hasIPv4 || !hasIPv6 {
			t.Fatalf("LocalNet localhost answers = %v, want mixed IPv4 and IPv6 loopback answers", ips)
		}
	}

	vtun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeVTun, "wireguard vtun")
	wg := addWindowsGraphNode(t, engine, wireguardnodes.NodeTypeWireGuard, "wireguard")
	applyWindowsGraphForm(engine, wg, "config", map[string]any{
		"privateKey": "CAkKCwwNDg8QERITFBUWFxgZGhscHR4fICEiIyQlJkc=",
		"listenPort": 0,
		"ownedIPs":   "10.232.0.2/32\nfd00:232::2/128",
	})
	linkWindowsGraphPorts(t, engine, vtun, "right", "Tun", wg, "left", "Tun")
	linkWindowsGraphPorts(t, engine, out, "left", "Network", wg, "right", "Network")
	waitWindowsGraphNodeLabel(t, engine, wg, "up")
	if snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, defaultTun); !ok || snapshot.Label != "ready" {
		t.Fatalf("DefaultTun state while WireGuard uses OutNet = %#v, want ready", snapshot)
	}
}

func TestWindowsGraphDefaultTunDNSProviderLifecycle(t *testing.T) {
	engine, _, _, _ := newWindowsNetworkGraphEngine(t)
	defaultTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeDefaultTun, "default tun")
	applyWindowsGraphForm(engine, defaultTun, "config", map[string]any{
		"tunAddrs": windowsGraphDNSAddress + "/32",
		"dnsIP":    windowsGraphDNSAddress,
	})
	static := addWindowsGraphNode(t, engine, dnsnodes.NodeTypeStaticDNS, "static dns")
	applyWindowsGraphForm(engine, static, "config", map[string]any{
		"records": "graph.test A 192.0.2.18", "ttl": 60,
	})
	link := linkWindowsGraphPorts(t, engine, static, "left", "DNS", defaultTun, "right", "DNS")
	waitWindowsGraphDNSAnswer(t, "udp", "graph.test.", netip.MustParseAddr("192.0.2.18"))
	waitWindowsGraphDNSAnswer(t, "tcp", "graph.test.", netip.MustParseAddr("192.0.2.18"))

	applyWindowsGraphForm(engine, static, "config", map[string]any{
		"records": "graph.test A 192.0.2.19", "ttl": 60,
	})
	waitWindowsGraphDNSAnswer(t, "udp", "graph.test.", netip.MustParseAddr("192.0.2.19"))
	engine.RemoveLink(almagest.MainWorkspace, link)
	if err := queryWindowsGraphDNS("udp", "graph.test.", netip.MustParseAddr("192.0.2.19")); err == nil {
		t.Fatal("DefaultTun DNS answered after provider detach; want SetDNS(nil) drop")
	}

	linkWindowsGraphPorts(t, engine, static, "left", "DNS", defaultTun, "right", "DNS")
	waitWindowsGraphDNSAnswer(t, "tcp", "graph.test.", netip.MustParseAddr("192.0.2.19"))
	snapshot, _ := engine.NodeSnapshot(almagest.MainWorkspace, defaultTun)
	foundWarning := false
	for _, popup := range snapshot.Popups {
		foundWarning = foundWarning || popup.Text == string(sysnet.WarningDefaultTunDNSRouteNotExclusive)
	}
	if !foundWarning {
		t.Fatalf("DefaultTun popups = %#v, want DNS route warning", snapshot.Popups)
	}
}

func TestWindowsGraphRejectsUnsupportedDefaultTunSourceRoutes(t *testing.T) {
	sender := &windowsTunConfigSenderClass{config: links.TunConfig{
		Addrs:  []string{"10.232.0.2/32"},
		Routes: []string{"192.0.2.0/24"},
		SourceRoutes: []links.TunSourceRoute{{
			Destination: netip.MustParsePrefix("192.0.2.0/24"),
			Source:      netip.MustParseAddr("10.232.0.2"),
		}},
	}}
	engine, _, _, logf := newWindowsNetworkGraphEngine(t, sender)
	defaultTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeDefaultTun, "default tun")
	provider := addWindowsGraphNode(t, engine, sender.ClassName(), "source routes")
	linkWindowsGraphPorts(t, engine, provider, "left", "Tun", defaultTun, "right", "Tun")
	waitWindowsGraphNodeLabel(t, engine, defaultTun, "invalid config")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range logf.Drain() {
			if strings.Contains(entry.Message, "DefaultTun.SourceRoutes") && strings.Contains(entry.Message, "not supported") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("missing clear unsupported DefaultTun.SourceRoutes graph error")
}

func testWindowsGraphTCP(t *testing.T, name string, network gonnect.Network, family, wildcard string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener, err := network.ListenTCP(ctx, family, wildcard)
	if err != nil {
		t.Fatalf("%s ListenTCP(%s): %v", name, family, err)
	}
	defer listener.Close() //nolint:errcheck
	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.AcceptTCP()
		if acceptErr == nil {
			defer conn.Close() //nolint:errcheck
			buf := make([]byte, 4)
			_, acceptErr = io.ReadFull(conn, buf)
			if acceptErr == nil && string(buf) != "ping" {
				acceptErr = fmt.Errorf("TCP payload = %q", buf)
			}
		}
		accepted <- acceptErr
	}()
	conn, err := network.DialTCP(ctx, family, wildcard, listener.Addr().String())
	if err != nil {
		t.Fatalf("%s DialTCP(%s): %v", name, family, err)
	}
	if _, err = conn.Write([]byte("ping")); err != nil {
		t.Fatalf("%s TCP write(%s): %v", name, family, err)
	}
	_ = conn.Close()
	if err = <-accepted; err != nil {
		t.Fatalf("%s TCP accept(%s): %v", name, family, err)
	}
}

func testWindowsGraphUDP(t *testing.T, name string, network gonnect.Network, family, wildcard string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, err := network.ListenUDP(ctx, family, wildcard)
	if err != nil {
		t.Fatalf("%s ListenUDP(%s): %v", name, family, err)
	}
	defer server.Close() //nolint:errcheck
	client, err := network.DialUDP(ctx, family, wildcard, server.LocalAddr().String())
	if err != nil {
		t.Fatalf("%s DialUDP(%s): %v", name, family, err)
	}
	defer client.Close() //nolint:errcheck
	_ = server.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err = client.Write([]byte("ping")); err != nil {
		t.Fatalf("%s UDP write(%s): %v", name, family, err)
	}
	buf := make([]byte, 4)
	if n, _, readErr := server.ReadFromUDP(buf); readErr != nil || n != len(buf) || string(buf) != "ping" {
		t.Fatalf("%s UDP read(%s) = %d, %q, %v", name, family, n, buf[:n], readErr)
	}
}

func waitWindowsGraphTCPConnect(t *testing.T, network gonnect.Network, family, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		conn, err := network.DialTCP(ctx, family, "", address)
		cancel()
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("LocalNet did not connect to local proxy at %s", address)
}

func reserveWindowsLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func waitWindowsGraphDNSAnswer(t *testing.T, network, name string, want netip.Addr) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		err = queryWindowsGraphDNS(network, name, want)
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s DNS query for %s: %v", network, name, err)
}

func queryWindowsGraphDNS(network, name string, want netip.Addr) error {
	request := &dns.Message{ID: dns.NextID(), RecursionDesired: true, Questions: []dns.Question{{
		Name: name, Type: dns.TypeA, Class: dns.ClassIN,
	}}}
	wire, err := dns.Pack(request)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout(network, net.JoinHostPort(windowsGraphDNSAddress, "53"), 500*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if network == "tcp" {
		framed := make([]byte, 2+len(wire))
		binary.BigEndian.PutUint16(framed, uint16(len(wire))) //nolint:gosec -- DNS TCP frames are 16-bit.
		copy(framed[2:], wire)
		if _, err = conn.Write(framed); err != nil {
			return err
		}
		if _, err = io.ReadFull(conn, framed[:2]); err != nil {
			return err
		}
		wire = make([]byte, binary.BigEndian.Uint16(framed[:2]))
		if _, err = io.ReadFull(conn, wire); err != nil {
			return err
		}
	} else {
		if _, err = conn.Write(wire); err != nil {
			return err
		}
		wire = make([]byte, 4096)
		n, readErr := conn.Read(wire)
		if readErr != nil {
			return readErr
		}
		wire = wire[:n]
	}
	response, err := dns.Unpack(wire)
	if err != nil {
		return err
	}
	for _, answer := range response.Answers {
		if answer.Type == dns.TypeA && len(answer.Data) == net.IPv4len && netip.AddrFrom4([4]byte(answer.Data)) == want {
			return nil
		}
	}
	return fmt.Errorf("DNS answers = %#v, want %s", response.Answers, want)
}

type windowsNetworkReceiverClass struct {
	events chan links.NetworkPayload
}

func (*windowsNetworkReceiverClass) ClassName() string        { return "test/WindowsNetworkReceiver" }
func (*windowsNetworkReceiverClass) ShortDescription() string { return "Windows network receiver" }
func (*windowsNetworkReceiverClass) LongDescription() string {
	return "Receives native network graph values."
}
func (*windowsNetworkReceiverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{nodekit.NetworkPort("right", "Network")}}
}
func (c *windowsNetworkReceiverClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &windowsNetworkReceiverNode{events: c.events}, nil
}

type windowsNetworkReceiverNode struct {
	pasta.BasicNode
	in     uint64
	events chan links.NetworkPayload
}

func (n *windowsNetworkReceiverNode) OnInit(_ *pasta.Workspace, _ pasta.Logger, _ uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.in = restored.RightPorts[0]
	return nil
}
func (n *windowsNetworkReceiverNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if port != n.in || direction != "right" || linkType != links.TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}
func (n *windowsNetworkReceiverNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if event.ReceiverPort == n.in && direction == "right" && linkType == links.TypeNetwork {
		if payload, ok := event.Payload.(links.NetworkPayload); ok {
			n.events <- payload
		}
	}
	return nil
}

type windowsTunConfigSenderClass struct {
	config links.TunConfig
}

func (*windowsTunConfigSenderClass) ClassName() string        { return "test/WindowsTunConfigSender" }
func (*windowsTunConfigSenderClass) ShortDescription() string { return "Windows TUN config sender" }
func (*windowsTunConfigSenderClass) LongDescription() string {
	return "Sends one TUN config for policy tests."
}
func (*windowsTunConfigSenderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{nodekit.TunPort("left", "Tun")}}
}
func (c *windowsTunConfigSenderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &windowsTunConfigSenderNode{config: c.config}, nil
}

type windowsTunConfigSenderNode struct {
	pasta.BasicNode
	w      *pasta.Workspace
	id     uint64
	out    uint64
	config links.TunConfig
}

func (n *windowsTunConfigSenderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.out = restored.LeftPorts[0]
	return nil
}
func (n *windowsTunConfigSenderNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if port != n.out || direction != "left" || linkType != links.TypeTun {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}
func (n *windowsTunConfigSenderNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.out {
		n.w.EmitEventLocked(n.id, link, n.config)
	}
	return nil
}

func newWindowsNetworkGraphEngine(t *testing.T, extra ...pasta.NodeClass) (almagest.Engine, sysnet.System, *windowsNetworkReceiverClass, *logging.DebugLogFactory) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native Windows network graph tests require an elevated identity")
	}
	system, err := SystemIntegration(configer.NewMemory(nil), nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	receiver := &windowsNetworkReceiverClass{events: make(chan links.NetworkPayload, 8)}
	classes := append([]pasta.NodeClass{receiver}, extra...)
	logf := logging.NewDebugLogFactory(4096)
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logf, system,
		nil, nil, &windowsGraphSecrets{values: map[string]storage.Secret{}}, nil,
		classes, nil, false,
	)
	if err != nil {
		_ = system.Close()
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return engine, system, receiver, logf
}

type windowsGraphSecrets struct {
	mu     sync.Mutex
	values map[string]storage.Secret
	next   int
}

func (s *windowsGraphSecrets) GetSecret(key, _ string) (storage.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.values[key]
	if !ok {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	return secret, nil
}
func (s *windowsGraphSecrets) NewSecret(secret storage.Secret, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	key := fmt.Sprintf("windows-graph-secret-%d", s.next)
	s.values[key] = secret
	return key, nil
}

func addWindowsGraphNode(t *testing.T, engine almagest.Engine, class, name string) uint64 {
	t.Helper()
	id, err := engine.AddNode(almagest.MainWorkspace, class, name)
	if err != nil {
		t.Fatalf("AddNode %s: %v", class, err)
	}
	return id
}

func linkWindowsGraphPorts(t *testing.T, engine almagest.Engine, from uint64, fromDirection, fromName string, to uint64, toDirection, toName string) uint64 {
	t.Helper()
	fromPort := windowsGraphPort(t, engine, from, fromDirection, fromName)
	toPort := windowsGraphPort(t, engine, to, toDirection, toName)
	link, _, err := engine.AddLink(almagest.MainWorkspace, fromPort, toPort)
	if err != nil {
		t.Fatalf("AddLink %d/%s to %d/%s: %v", from, fromName, to, toName, err)
	}
	return link
}

func windowsGraphPort(t *testing.T, engine almagest.Engine, node uint64, direction, name string) uint64 {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("node %d snapshot missing", node)
	}
	ports := snapshot.LeftPorts
	if direction == "right" {
		ports = snapshot.RightPorts
	}
	for _, id := range ports {
		port, ok := engine.PortSnapshot(almagest.MainWorkspace, id)
		if ok && port.Name == name {
			return id
		}
	}
	t.Fatalf("node %d has no %s port %q", node, direction, name)
	return 0
}

func receiveWindowsNetworkPayload(t *testing.T, events <-chan links.NetworkPayload) links.NetworkPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Network == nil {
			t.Fatal("received nil Windows network payload")
		}
		return payload
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Windows network payload")
		return links.NetworkPayload{}
	}
}

func applyWindowsGraphForm(engine almagest.Engine, node uint64, block string, values map[string]any) {
	engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     block, Values: values,
	})
}

func waitWindowsGraphNodeLabel(t *testing.T, engine almagest.Engine, node uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node); ok && snapshot.Label == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	snapshot, _ := engine.NodeSnapshot(almagest.MainWorkspace, node)
	t.Fatalf("node %d label = %q, want %q", node, snapshot.Label, want)
}
