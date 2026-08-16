package httpnodes_test

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
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

func TestHTTPClientE2EHTTPSRequestUsesCustomTLSConfig(t *testing.T) {
	server, roots := newRuntimeCAHTTPSServer(t, "HTTPS custom TLS ok")
	storage := newHTTPClientE2ETLSConfigStorage(map[string]*x509.CertPool{
		nodekit.DefaultTLSConfigName: nil,
		"runtime-ca":                 roots,
	})
	e := newHTTPClientE2EWithTLSStorage(t, storage)

	out := e.AddNode(NodeTypeOutNetwork, "out")
	client := e.AddNode(NodeTypeHTTPClient, "client")
	e.SubscribeMenus(client)
	e.connectByPortName(out, "left", "Network", client, "right", "Network")
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       server.URL,
	})
	e.waitClientRequestField(client, "url", server.URL)

	e.sendClientRequest(client)
	e.waitClientFailure(client, "certificate signed by unknown authority")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: nodekit.TLSConfigFieldID},
		Value:       "runtime-ca",
	})
	e.waitClientRequestField(client, nodekit.TLSConfigFieldID, "runtime-ca")

	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "HTTPS custom TLS ok")
	e.AssertNodeLabel(client, "200 OK")
}

func newHTTPClientE2EWithTLSStorage(t *testing.T, storage TlsConfigStorage) *httpE2E {
	t.Helper()

	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := &sysnetdebug.System{
		OutNetwork:   gonnect.DefaultNetwork(nil),
		LocalNetwork: gonnect.NewLoopbackNetwork(),
	}
	secrets := newE2ESecretsStorage()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		nil,
		nil,
		secrets,
		storage,
		nil,
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
	return newHTTPE2EFromNodeE2E(e)
}

type httpClientE2ETLSConfigStorage struct {
	mu      sync.Mutex
	configs map[string]*x509.CertPool
}

func newHTTPClientE2ETLSConfigStorage(configs map[string]*x509.CertPool) *httpClientE2ETLSConfigStorage {
	return &httpClientE2ETLSConfigStorage{configs: configs}
}

func (s *httpClientE2ETLSConfigStorage) ListConfigs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *httpClientE2ETLSConfigStorage) GetClientConfig(name string, _ string) (tls.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roots, ok := s.configs[name]
	if !ok {
		return tls.Config{}, ErrTlsConfigNotFound
	}
	if roots != nil {
		return tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots.Clone()}, nil
	}
	return tls.Config{MinVersion: tls.VersionTLS12}, nil
}

func newRuntimeCAHTTPSServer(t *testing.T, response string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	caCert, caKey := newRuntimeTestCA(t)
	serverCert := newRuntimeTestServerCertificate(t, caCert, caKey)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(response))
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return server, roots
}

func newRuntimeTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
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

func newRuntimeTestServerCertificate(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
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
