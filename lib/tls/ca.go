package tlsnodes

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

const defaultLeafTTL = 24 * time.Hour

// localLeafSigner creates server leaf certificates from the engine-local CA.
// It is used only for the TlsWrapper listener side; the MITM and terminator
// nodes delegate leaf generation to gonnect/tls.
type localLeafSigner struct {
	ca      tls.Certificate
	caCert  *x509.Certificate
	leafTTL time.Duration
}

func newLocalLeafSigner(ca tls.Certificate, leafTTL time.Duration) (*localLeafSigner, error) {
	if leafTTL == 0 {
		leafTTL = defaultLeafTTL
	}
	if leafTTL < 0 {
		return nil, errors.New("leaf TTL cannot be negative")
	}
	owned, cert, err := parseLocalCA(ca)
	if err != nil {
		return nil, err
	}
	return &localLeafSigner{ca: owned, caCert: cert, leafTTL: leafTTL}, nil
}

func (s *localLeafSigner) certificate(host string) (tls.Certificate, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return tls.Certificate{}, errors.New("server name is empty")
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate leaf serial: %w", err)
	}

	now := time.Now()
	notAfter := now.Add(s.leafTTL)
	if notAfter.After(s.caCert.NotAfter) {
		notAfter = s.caCert.NotAfter
	}
	if !notAfter.After(now) {
		return tls.Certificate{}, errors.New("local CA certificate is expired")
	}

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, s.caCert, &priv.PublicKey, s.ca.PrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sign leaf certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse leaf certificate: %w", err)
	}
	chain := make([][]byte, 0, 1+len(s.ca.Certificate))
	chain = append(chain, der)
	for _, cert := range s.ca.Certificate {
		chain = append(chain, append([]byte(nil), cert...))
	}
	return tls.Certificate{Certificate: chain, PrivateKey: priv, Leaf: leaf}, nil
}

func parseLocalCA(ca tls.Certificate) (tls.Certificate, *x509.Certificate, error) {
	if len(ca.Certificate) == 0 {
		return tls.Certificate{}, nil, errors.New("local CA certificate chain is empty")
	}
	if ca.PrivateKey == nil {
		return tls.Certificate{}, nil, errors.New("local CA private key is nil")
	}
	if _, ok := ca.PrivateKey.(crypto.Signer); !ok {
		return tls.Certificate{}, nil, errors.New("local CA private key cannot sign certificates")
	}
	cert, err := x509.ParseCertificate(ca.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("parse local CA certificate: %w", err)
	}
	if !cert.IsCA {
		return tls.Certificate{}, nil, errors.New("local CA certificate is not a CA")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return tls.Certificate{}, nil, errors.New("local CA certificate cannot sign certificates")
	}
	owned := cloneTLSCertificate(ca)
	owned.Leaf = cert
	return owned, cert, nil
}
