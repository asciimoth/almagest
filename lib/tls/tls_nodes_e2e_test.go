package tlsnodes_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	tlsnodes "github.com/asciimoth/almagest/lib/tls"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

func TestTLSWrapperE2EHTTPClientToExternalHTTPSServer(t *testing.T) {
	originCA, originCert := newTLSTestCA(t, "origin wrapper CA")
	origin := newTLSTestHTTPSServer(t, "wrapper ok", originCA, "127.0.0.1")
	localCA, _ := newTLSTestCA(t, "local wrapper CA")
	store := newTLSNodeTestStorage(map[string]*tls.Config{
		"origin": {RootCAs: certPool(originCert), MinVersion: tls.VersionTLS12},
	}, localCA)
	e := newTLSNodeE2E(t, store)
	ops := httpe2etest.NewHTTPOps(e)

	out := e.AddNode("almagest/OutNetwork", "out")
	wrapper := e.AddNode(tlsnodes.NodeTypeTLSWrapper, "wrapper")
	client := e.AddNode("almagest/HttpClient", "client")
	e.SubscribeMenus(wrapper, client)
	applyNodeConfig(e, wrapper, map[string]any{
		nodekit.TLSConfigFieldID: "origin",
		"serverName":             "wrapper.test",
	})
	setHTTPClientURL(e, client, strings.Replace(origin.URL, "https://", "http://", 1))

	ops.ConnectByPortName(out, "left", "Network", wrapper, "right", "Network")
	ops.ConnectByPortName(wrapper, "left", "Network", client, "right", "Network")
	e.WaitNodeLabel(wrapper, "wrapping TLS")

	ops.SendClientRequest(client)
	ops.WaitClientResponse(client, "200 OK", "wrapper ok")
}

func TestTLSMITME2EInterceptsHTTPSThroughHTTPClient(t *testing.T) {
	originCA, originCert := newTLSTestCA(t, "origin mitm CA")
	origin := newTLSTestHTTPSServer(t, "mitm ok", originCA, "origin.test")
	localCA, localCert := newTLSTestCA(t, "local mitm CA")
	store := newTLSNodeTestStorage(map[string]*tls.Config{
		"origin":       {RootCAs: certPool(originCert), MinVersion: tls.VersionTLS12},
		"local-client": {RootCAs: certPool(localCert), ServerName: "origin.test", MinVersion: tls.VersionTLS12},
	}, localCA)
	e := newTLSNodeE2E(t, store)
	ops := httpe2etest.NewHTTPOps(e)

	out := e.AddNode("almagest/OutNetwork", "out")
	mitm := e.AddNode(tlsnodes.NodeTypeTLSMITM, "mitm")
	client := e.AddNode("almagest/HttpClient", "client")
	e.SubscribeMenus(mitm, client)
	applyNodeConfig(e, mitm, map[string]any{nodekit.TLSConfigFieldID: "origin"})
	setHTTPClientURL(e, client, origin.URL)
	setHTTPClientTLSConfig(e, client, "local-client")

	ops.ConnectByPortName(out, "left", "Network", mitm, "right", "Network")
	ops.ConnectByPortName(mitm, "left", "Network", client, "right", "Network")
	e.WaitNodeLabel(mitm, "intercepting TLS")

	ops.SendClientRequest(client)
	ops.WaitClientResponse(client, "200 OK", "mitm ok")
}

func TestTLSTerminatorE2ETerminatesHTTPSToPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("terminated ok"))
	}))
	t.Cleanup(plain.Close)
	localCA, localCert := newTLSTestCA(t, "local terminator CA")
	store := newTLSNodeTestStorage(map[string]*tls.Config{
		"local-client": {RootCAs: certPool(localCert), ServerName: "terminator.test", MinVersion: tls.VersionTLS12},
	}, localCA)
	e := newTLSNodeE2E(t, store)
	ops := httpe2etest.NewHTTPOps(e)

	out := e.AddNode("almagest/OutNetwork", "out")
	terminator := e.AddNode(tlsnodes.NodeTypeTLSTerminator, "terminator")
	client := e.AddNode("almagest/HttpClient", "client")
	e.SubscribeMenus(terminator, client)
	setHTTPClientURL(e, client, strings.Replace(plain.URL, "http://", "https://", 1))
	setHTTPClientTLSConfig(e, client, "local-client")

	ops.ConnectByPortName(out, "left", "Network", terminator, "right", "Network")
	ops.ConnectByPortName(terminator, "left", "Network", client, "right", "Network")
	e.WaitNodeLabel(terminator, "terminating TLS")

	ops.SendClientRequest(client)
	ops.WaitClientResponse(client, "200 OK", "terminated ok")
}

func TestTLSWrapperE2ELinksAttachDetachReattach(t *testing.T) {
	originCA, originCert := newTLSTestCA(t, "origin wrapper links CA")
	origin := newTLSTestHTTPSServer(t, "wrapper links ok", originCA, "127.0.0.1")
	localCA, _ := newTLSTestCA(t, "local wrapper links CA")
	store := newTLSNodeTestStorage(map[string]*tls.Config{
		"origin": {RootCAs: certPool(originCert), MinVersion: tls.VersionTLS12},
	}, localCA)
	e := newTLSNodeE2E(t, store)
	ops := httpe2etest.NewHTTPOps(e)

	out := e.AddNode("almagest/OutNetwork", "out")
	wrapper := e.AddNode(tlsnodes.NodeTypeTLSWrapper, "wrapper")
	client := e.AddNode("almagest/HttpClient", "client")
	e.SubscribeMenus(wrapper, client)
	applyNodeConfig(e, wrapper, map[string]any{
		nodekit.TLSConfigFieldID: "origin",
		"serverName":             "wrapper.test",
	})
	setHTTPClientURL(e, client, strings.Replace(origin.URL, "https://", "http://", 1))

	exerciseTLSNetworkNodeLinkCycle(t, e, ops, wrapper, client, "wrapping TLS", "wrapper links ok",
		func() uint64 {
			return ops.ConnectByPortName(out, "left", "Network", wrapper, "right", "Network")
		},
		func() uint64 {
			return ops.ConnectByPortName(wrapper, "left", "Network", client, "right", "Network")
		},
	)
}

func TestTLSMITME2ELinksAttachDetachReattach(t *testing.T) {
	originCA, originCert := newTLSTestCA(t, "origin mitm links CA")
	origin := newTLSTestHTTPSServer(t, "mitm links ok", originCA, "origin.test")
	localCA, localCert := newTLSTestCA(t, "local mitm links CA")
	store := newTLSNodeTestStorage(map[string]*tls.Config{
		"origin":       {RootCAs: certPool(originCert), MinVersion: tls.VersionTLS12},
		"local-client": {RootCAs: certPool(localCert), ServerName: "origin.test", MinVersion: tls.VersionTLS12},
	}, localCA)
	e := newTLSNodeE2E(t, store)
	ops := httpe2etest.NewHTTPOps(e)

	out := e.AddNode("almagest/OutNetwork", "out")
	mitm := e.AddNode(tlsnodes.NodeTypeTLSMITM, "mitm")
	client := e.AddNode("almagest/HttpClient", "client")
	e.SubscribeMenus(mitm, client)
	applyNodeConfig(e, mitm, map[string]any{nodekit.TLSConfigFieldID: "origin"})
	setHTTPClientURL(e, client, origin.URL)
	setHTTPClientTLSConfig(e, client, "local-client")

	exerciseTLSNetworkNodeLinkCycle(t, e, ops, mitm, client, "intercepting TLS", "mitm links ok",
		func() uint64 {
			return ops.ConnectByPortName(out, "left", "Network", mitm, "right", "Network")
		},
		func() uint64 {
			return ops.ConnectByPortName(mitm, "left", "Network", client, "right", "Network")
		},
	)
}

func TestTLSTerminatorE2ELinksAttachDetachReattach(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("terminator links ok"))
	}))
	t.Cleanup(plain.Close)
	localCA, localCert := newTLSTestCA(t, "local terminator links CA")
	store := newTLSNodeTestStorage(map[string]*tls.Config{
		"local-client": {RootCAs: certPool(localCert), ServerName: "terminator.test", MinVersion: tls.VersionTLS12},
	}, localCA)
	e := newTLSNodeE2E(t, store)
	ops := httpe2etest.NewHTTPOps(e)

	out := e.AddNode("almagest/OutNetwork", "out")
	terminator := e.AddNode(tlsnodes.NodeTypeTLSTerminator, "terminator")
	client := e.AddNode("almagest/HttpClient", "client")
	e.SubscribeMenus(terminator, client)
	setHTTPClientURL(e, client, strings.Replace(plain.URL, "http://", "https://", 1))
	setHTTPClientTLSConfig(e, client, "local-client")

	exerciseTLSNetworkNodeLinkCycle(t, e, ops, terminator, client, "terminating TLS", "terminator links ok",
		func() uint64 {
			return ops.ConnectByPortName(out, "left", "Network", terminator, "right", "Network")
		},
		func() uint64 {
			return ops.ConnectByPortName(terminator, "left", "Network", client, "right", "Network")
		},
	)
}

func exerciseTLSNetworkNodeLinkCycle(
	t *testing.T,
	e *e2etest.NodeE2E,
	ops *httpe2etest.HTTPOps,
	node uint64,
	client uint64,
	readyLabel string,
	response string,
	addProviderLink func() uint64,
	addConsumerLink func() uint64,
) {
	t.Helper()
	providerLink := addProviderLink()
	consumerLink := addConsumerLink()
	e.WaitNodeLabel(node, readyLabel)
	ops.AssertClientRequestReturns(client, response, 10*time.Second)

	e.Engine.RemoveLink(e2etest.E2EWorkspace, consumerLink)
	ops.WaitClientReady(client)
	ops.SendClientRequest(client)
	ops.WaitClientFailure(client, "network is not ready")
	consumerLink = addConsumerLink()
	e.WaitNodeLabel(node, readyLabel)
	ops.AssertClientRequestReturns(client, response, 10*time.Second)

	e.Engine.RemoveLink(e2etest.E2EWorkspace, providerLink)
	e.WaitNodeLabel(node, "waiting for network")
	ops.SendClientRequest(client)
	ops.WaitClientAnyFailure(client)
	providerLink = addProviderLink()
	e.WaitNodeLabel(node, readyLabel)
	ops.AssertClientRequestReturns(client, response, 10*time.Second)

	_ = providerLink
	_ = consumerLink
}

type tlsNodeTestStorage struct {
	mu      sync.Mutex
	configs map[string]*tls.Config
	localCA tls.Certificate
}

func newTLSNodeTestStorage(configs map[string]*tls.Config, localCA tls.Certificate) *tlsNodeTestStorage {
	copied := make(map[string]*tls.Config, len(configs))
	for name, cfg := range configs {
		copied[name] = nodekit.CloneTLSClientConfig(cfg)
	}
	return &tlsNodeTestStorage{configs: copied, localCA: nodekit.CloneTLSCertificate(localCA)}
}

func (s *tlsNodeTestStorage) ListConfigs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *tlsNodeTestStorage) GetClientConfig(name string, _ string) (tls.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.configs[name]
	if !ok {
		return tls.Config{}, storage.ErrTlsConfigNotFound
	}
	return *nodekit.CloneTLSClientConfig(cfg), nil
}

func (s *tlsNodeTestStorage) GetLocalCA(string) (tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.localCA.Certificate) == 0 || s.localCA.PrivateKey == nil {
		return tls.Certificate{}, storage.ErrTlsConfigNotFound
	}
	return nodekit.CloneTLSCertificate(s.localCA), nil
}

func newTLSNodeE2E(t *testing.T, tlsStore storage.TlsConfigStorage) *e2etest.NodeE2E {
	t.Helper()
	pool := e2etest.NewTestDebugPool(t)
	logf := logging.NewDebugLogFactory(16384)
	sys := &sysnetdebug.System{
		OutNetwork:   gonnect.DefaultNetwork(nil),
		LocalNetwork: gonnect.NewLoopbackNetwork(),
	}
	secrets := e2etest.NewSecretsStorage()
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		nil,
		nil,
		secrets,
		tlsStore,
		nil,
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v", err)
		}
	})
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}

func applyNodeConfig(e *e2etest.NodeE2E, node uint64, values map[string]any) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: e2etest.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values:      values,
	})
}

func setHTTPClientURL(e *e2etest.NodeE2E, node uint64, value string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FieldUpdateMessage{
		MessageBase: e2etest.FormMessageBase(formular.MessageFieldUpdate, node),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       value,
	})
}

func setHTTPClientTLSConfig(e *e2etest.NodeE2E, node uint64, value string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FieldUpdateMessage{
		MessageBase: e2etest.FormMessageBase(formular.MessageFieldUpdate, node),
		Field:       formular.FieldRef{BlockID: "request", FieldID: nodekit.TLSConfigFieldID},
		Value:       value,
	})
}

func newTLSTestHTTPSServer(t *testing.T, body string, ca tls.Certificate, names ...string) *httptest.Server {
	t.Helper()
	serverCert := newTLSTestServerCertificate(t, ca, names...)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(body))
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func newTLSTestCA(t *testing.T, commonName string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, cert
}

func newTLSTestServerCertificate(t *testing.T, ca tls.Certificate, names ...string) tls.Certificate {
	t.Helper()
	caCert, err := x509.ParseCertificate(ca.Certificate[0])
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, ca.PrivateKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse server certificate: %v", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der, ca.Certificate[0]},
		PrivateKey:  key,
		Leaf:        cert,
	}
}

func certPool(ca *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool
}
