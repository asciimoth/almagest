package tlsnodes

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func TestLocalLeafSignerCertificates(t *testing.T) {
	ca := testLocalCertificateAuthority(t, true, x509.KeyUsageCertSign, time.Now().Add(time.Hour))
	signer, err := newLocalLeafSigner(ca, 0)
	if err != nil {
		t.Fatal(err)
	}
	if signer.leafTTL != defaultLeafTTL {
		t.Fatalf("default leaf TTL = %v", signer.leafTTL)
	}
	for _, host := range []string{"example.test", "192.0.2.1"} {
		leaf, err := signer.certificate(host)
		if err != nil {
			t.Fatalf("certificate(%q): %v", host, err)
		}
		if leaf.Leaf == nil || len(leaf.Certificate) != 2 || leaf.Leaf.Subject.CommonName != host {
			t.Fatalf("certificate(%q) = %#v", host, leaf)
		}
		if host == "example.test" && len(leaf.Leaf.DNSNames) != 1 {
			t.Fatalf("DNS names = %#v", leaf.Leaf.DNSNames)
		}
		if host == "192.0.2.1" && len(leaf.Leaf.IPAddresses) != 1 {
			t.Fatalf("IP addresses = %#v", leaf.Leaf.IPAddresses)
		}
	}
	if _, err := signer.certificate("  "); err == nil {
		t.Fatal("empty server name was accepted")
	}
	if _, err := newLocalLeafSigner(ca, -time.Second); err == nil {
		t.Fatal("negative leaf TTL was accepted")
	}

	expired := testLocalCertificateAuthority(t, true, x509.KeyUsageCertSign, time.Now().Add(-time.Minute))
	expiredSigner, err := newLocalLeafSigner(expired, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expiredSigner.certificate("example.test"); err == nil {
		t.Fatal("expired CA signed a certificate")
	}
}

func TestParseLocalCAValidation(t *testing.T) {
	valid := testLocalCertificateAuthority(t, true, x509.KeyUsageCertSign, time.Now().Add(time.Hour))
	cases := []tls.Certificate{
		{},
		{Certificate: valid.Certificate},
		{Certificate: valid.Certificate, PrivateKey: struct{}{}},
		{Certificate: [][]byte{{1, 2, 3}}, PrivateKey: valid.PrivateKey},
		testLocalCertificateAuthority(t, false, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour)),
		testLocalCertificateAuthority(t, true, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour)),
	}
	for i, candidate := range cases {
		if _, _, err := parseLocalCA(candidate); err == nil {
			t.Errorf("case %d was accepted", i)
		}
	}
}

func testLocalCertificateAuthority(t *testing.T, isCA bool, usage x509.KeyUsage, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test local CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
