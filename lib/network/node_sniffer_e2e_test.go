package networknodes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

var nextSnifferHTTPTestPort atomic.Uint32

func snifferHTTPTestPort() int {
	return 19080 + int(nextSnifferHTTPTestPort.Add(1))
}

func TestSnifferNodeE2EHTTPRoutesByURLAndRemapsDestination(t *testing.T) {
	backend := NewMockLoopbackNetwork(true)
	t.Cleanup(func() { _ = backend.Close() })

	alphaPort := snifferHTTPTestPort()
	betaPort := snifferHTTPTestPort()
	startSnifferHTTPServer(t, backend, alphaPort, "alpha response")
	startSnifferHTTPServer(t, backend, betaPort, "beta response")

	network := newSnifferNodeOutputNetwork(t, backend, fmt.Sprintf(`
DIAL
TCP
AND
INTERCEPT

SNIFF HTTP URL:/alpha
ROUTE 1 DST:127.0.0.1:%d

SNIFF HTTP URL:/beta
ROUTE 1 DST:127.0.0.1:%d

TRUE
DROP
`, alphaPort, betaPort))

	client := snifferHTTPClient(network, nil)
	if got := snifferHTTPGet(t, client, "http://service.test/alpha"); got != "alpha response" {
		t.Fatalf("alpha response = %q, want alpha response", got)
	}
	if got := snifferHTTPGet(t, client, "http://service.test/beta"); got != "beta response" {
		t.Fatalf("beta response = %q, want beta response", got)
	}
	snifferHTTPGetFails(t, client, "http://service.test/rejected")
}

func TestSnifferNodeE2EHTTPSRoutesByTLSALPNAndRemapsDestination(t *testing.T) {
	backend := NewMockLoopbackNetwork(true)
	t.Cleanup(func() { _ = backend.Close() })

	port := snifferHTTPTestPort()
	startSnifferHTTPSServer(t, backend, port, "tls response")

	network := newSnifferNodeOutputNetwork(t, backend, fmt.Sprintf(`
DIAL
TCP
AND
INTERCEPT

SNIFF TLS ALPN:http/1.1
ROUTE 1 DST:127.0.0.1:%d

TRUE
DROP
`, port))

	client := snifferHTTPClient(network, &tls.Config{
		InsecureSkipVerify: true, // Test certificate trust is not the behavior under test.
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"http/1.1"},
	})
	if got := snifferHTTPGet(t, client, "https://service.test/secure"); got != "tls response" {
		t.Fatalf("TLS response = %q, want tls response", got)
	}
}

func TestSnifferNodeE2ELinksAttachDetachReattach(t *testing.T) {
	const (
		slot1Response = "sniffer slot 1 response"
		slot2Response = "sniffer slot 2 response"
	)

	backend1 := NewMockLoopbackNetwork(true)
	t.Cleanup(func() { _ = backend1.Close() })
	backend2 := NewMockLoopbackNetwork(true)
	t.Cleanup(func() { _ = backend2.Close() })

	port1 := snifferHTTPTestPort()
	port2 := snifferHTTPTestPort()
	startSnifferHTTPServer(t, backend1, port1, slot1Response)
	startSnifferHTTPServer(t, backend2, port2, slot2Response)

	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(SnifferClass{}); err != nil {
		t.Fatalf("AddNodeClass Sniffer: %v", err)
	}
	source1 := addSnifferTestNetworkSource(t, w, "test/SnifferNetworkSource1", "backend-1", backend1)
	source2 := addSnifferTestNetworkSource(t, w, "test/SnifferNetworkSource2", "backend-2", backend2)
	sniffer, err := w.AddNodeByClass(NodeTypeSniffer, "sniffer")
	if err != nil {
		t.Fatalf("AddNodeByClass Sniffer: %v", err)
	}
	applySnifferRules(t, w, sniffer, fmt.Sprintf(`
DIAL
TCP
AND
INTERCEPT

SNIFF HTTP URL:/slot-1
ROUTE 1 DST:127.0.0.1:%d

SNIFF HTTP URL:/slot-2
ROUTE 2 DST:127.0.0.1:%d

TRUE
DROP
`, port1, port2))
	receiver := networktest.NewReceiver(8)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	source1Port := testkit.MustNodeSnapshot(t, w, source1).LeftPorts[0]
	source2Port := testkit.MustNodeSnapshot(t, w, source2).LeftPorts[0]
	snifferPort := testkit.MustNodeSnapshot(t, w, sniffer).LeftPorts[0]
	slot1Port := snifferRightPort(t, w, sniffer, slotPortName(1))
	slot2Port := snifferRightPort(t, w, sniffer, slotPortName(2))

	mustAddWorkspaceNetworkLink(t, w, source1Port, slot1Port, "backend-1->sniffer slot 1")
	slot2Link := mustAddWorkspaceNetworkLink(t, w, source2Port, slot2Port, "backend-2->sniffer slot 2")
	outputLink := mustAddWorkspaceNetworkLink(t, w, snifferPort, receiver.In, "sniffer->receiver")
	network := receiveLatestOutNetPayload(t, receiver.Events).Network
	assertSnifferHTTPGet(t, network, "/slot-1", slot1Response)
	assertSnifferHTTPGet(t, network, "/slot-2", slot2Response)

	w.RemoveLink(outputLink)
	snifferHTTPGetFails(t, snifferHTTPClient(network, nil), "http://service.test/slot-1")
	mustAddWorkspaceNetworkLink(t, w, snifferPort, receiver.In, "sniffer->receiver reattach")
	network = receiveLatestOutNetPayload(t, receiver.Events).Network
	assertSnifferHTTPGet(t, network, "/slot-1", slot1Response)

	w.RemoveLink(slot2Link)
	snifferHTTPGetFails(t, snifferHTTPClient(network, nil), "http://service.test/slot-2")
	slot2Link = mustAddWorkspaceNetworkLink(t, w, source2Port, slot2Port, "backend-2->sniffer slot 2 reattach")
	network = receiveLatestOutNetPayload(t, receiver.Events).Network
	assertSnifferHTTPGet(t, network, "/slot-2", slot2Response)

	w.RemoveLink(slot2Link)
	slot2Link = mustAddWorkspaceNetworkLink(t, w, source1Port, slot2Port, "backend-1->sniffer slot 2 attach")
	network = receiveLatestOutNetPayload(t, receiver.Events).Network
	snifferHTTPGetFails(t, snifferHTTPClient(network, nil), "http://service.test/slot-2")

	w.RemoveLink(slot2Link)
	mustAddWorkspaceNetworkLink(t, w, source2Port, slot2Port, "backend-2->sniffer slot 2 restore")
	network = receiveLatestOutNetPayload(t, receiver.Events).Network
	assertSnifferHTTPGet(t, network, "/slot-2", slot2Response)
}

func newSnifferNodeOutputNetwork(t *testing.T, backend gonnect.Network, rules string) NetworkCloser {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(SnifferClass{}); err != nil {
		t.Fatalf("AddNodeClass Sniffer: %v", err)
	}
	if err := w.AddNodeClass(snifferTestNetworkSourceClass{network: backend}); err != nil {
		t.Fatalf("AddNodeClass SnifferNetworkSource: %v", err)
	}

	source, err := w.AddNodeByClass("test/SnifferNetworkSource", "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass backend: %v", err)
	}
	node, err := w.AddNodeByClass(NodeTypeSniffer, "sniffer")
	if err != nil {
		t.Fatalf("AddNodeByClass Sniffer: %v", err)
	}
	applySnifferRules(t, w, node, rules)
	receiver := networktest.NewReceiver(2)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	sourceSnapshot := testkit.MustNodeSnapshot(t, w, source)
	snifferSnapshot := testkit.MustNodeSnapshot(t, w, node)
	if _, _, err := w.AddLink(sourceSnapshot.LeftPorts[0], snifferRightPort(t, w, node, slotPortName(1))); err != nil {
		t.Fatalf("AddLink backend->sniffer: %v", err)
	}
	if _, _, err := w.AddLink(snifferSnapshot.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink sniffer->receiver: %v", err)
	}
	return networktest.ReceivePayload(t, receiver.Events).Network
}

func startSnifferHTTPServer(t *testing.T, network NetworkCloser, port int, body string) {
	t.Helper()
	startSnifferServer(t, network, port, nil, http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(body))
	}))
}

func startSnifferHTTPSServer(t *testing.T, network NetworkCloser, port int, body string) {
	t.Helper()
	caCert, caKey := newSnifferRuntimeTestCA(t)
	cert := newSnifferRuntimeTestServerCertificate(t, caCert, caKey)
	startSnifferServer(t, network, port, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}, http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(body))
	}))
}

func newSnifferRuntimeTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate runtime CA key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Almagest runtime test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create runtime CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse runtime CA certificate: %v", err)
	}
	return cert, key
}

func newSnifferRuntimeTestServerCertificate(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate runtime server key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses: []net.IP{
			net.IPv4(127, 0, 0, 1),
			net.IPv6loopback,
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create runtime server certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse runtime server certificate: %v", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der, caCert.Raw},
		PrivateKey:  key,
		Leaf:        cert,
	}
}

func startSnifferServer(t *testing.T, network NetworkCloser, port int, tlsConfig *tls.Config, handler http.Handler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := network.Listen(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("Listen(%s): %v", addr, err)
	}
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		_ = ln.Close()
		select {
		case err := <-done:
			if err != nil && err != http.ErrServerClosed {
				t.Errorf("server stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("timeout waiting for sniffer test server shutdown")
		}
	})
}

func snifferHTTPClient(network NetworkCloser, tlsConfig *tls.Config) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, networkName, address string) (net.Conn, error) {
				return network.Dial(ctx, networkName, address)
			},
			TLSClientConfig:   tlsConfig,
			ForceAttemptHTTP2: false,
			DisableKeepAlives: true,
		},
	}
}

func snifferHTTPGet(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %s, want 200 OK", url, resp.Status)
	}
	return string(data)
}

func snifferHTTPGetFails(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("GET %s error = nil, want failure", url)
	}
}

func assertSnifferHTTPGet(t *testing.T, network NetworkCloser, path, response string) {
	t.Helper()
	got := snifferHTTPGet(t, snifferHTTPClient(network, nil), "http://service.test"+path)
	if got != response {
		t.Fatalf("GET %s response = %q, want %q", path, got, response)
	}
}

func receiveLatestOutNetPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	var payload NetworkPayload
	select {
	case first := <-events:
		if first.Network != nil {
			payload = first
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for network payload")
	}
	for {
		select {
		case next := <-events:
			if next.Network != nil {
				payload = next
			}
		default:
			if payload.Network == nil {
				t.Fatal("received only unavailable network payloads")
			}
			return payload
		}
	}
}

func addSnifferTestNetworkSource(t *testing.T, w *pasta.Workspace, className, name string, network gonnect.Network) uint64 {
	t.Helper()
	if err := w.AddNodeClass(snifferTestNetworkSourceClass{
		className: className,
		network:   network,
	}); err != nil {
		t.Fatalf("AddNodeClass %s: %v", className, err)
	}
	id, err := w.AddNodeByClass(className, name)
	if err != nil {
		t.Fatalf("AddNodeByClass %s: %v", name, err)
	}
	return id
}

func mustAddWorkspaceNetworkLink(t *testing.T, w *pasta.Workspace, a, b uint64, description string) uint64 {
	t.Helper()
	link, linkType, err := w.AddLink(a, b)
	if err != nil {
		t.Fatalf("AddLink %s: %v", description, err)
	}
	if linkType != TypeNetwork {
		t.Fatalf("AddLink %s type = %q, want %q", description, linkType, TypeNetwork)
	}
	return link
}

type snifferTestNetworkSourceClass struct {
	className string
	network   gonnect.Network
}

func (c snifferTestNetworkSourceClass) ClassName() string {
	if c.className != "" {
		return c.className
	}
	return "test/SnifferNetworkSource"
}
func (snifferTestNetworkSourceClass) ShortDescription() string {
	return "Sniffer test network source"
}
func (snifferTestNetworkSourceClass) LongDescription() string {
	return "Sniffer test network source"
}
func (snifferTestNetworkSourceClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (c snifferTestNetworkSourceClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &snifferTestNetworkSourceNode{network: c.network}, nil
}

type snifferTestNetworkSourceNode struct {
	nodekit.SimpleNetworkProvider
	network gonnect.Network
}

func (n *snifferTestNetworkSourceNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	return n.InitSimpleNetworkProvider(w, nil, id, restored, n.network, "sniffer test network")
}
