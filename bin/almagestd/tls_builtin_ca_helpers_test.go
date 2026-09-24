package main

import (
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
)

func TestTLSBuiltinCAOptionVariants(t *testing.T) {
	tests := []struct {
		in   api.TLSBuiltinCAOptions
		want api.TLSBuiltinCAOptions
	}{
		{api.TLSBuiltinCAOptions{}, api.TLSBuiltinCAOptions{Issuer: "Almagest", KeyAlgorithm: "ecdsa", Curve: "P256"}},
		{api.TLSBuiltinCAOptions{Issuer: " issuer ", KeyAlgorithm: "ED25519", Curve: "ignored", KeyBits: 1}, api.TLSBuiltinCAOptions{Issuer: "issuer", KeyAlgorithm: "ed25519"}},
		{api.TLSBuiltinCAOptions{Issuer: "issuer", KeyAlgorithm: "rsa"}, api.TLSBuiltinCAOptions{Issuer: "issuer", KeyAlgorithm: "rsa", KeyBits: 3072}},
		{api.TLSBuiltinCAOptions{Issuer: "issuer", KeyAlgorithm: "ecdsa", Curve: "secp384r1"}, api.TLSBuiltinCAOptions{Issuer: "issuer", KeyAlgorithm: "ecdsa", Curve: "P384"}},
	}
	for _, tt := range tests {
		got, err := normalizeTLSBuiltinCAOptions(tt.in)
		if err != nil || !tlsBuiltinCAOptionsEqual(got, tt.want) {
			t.Errorf("normalizeTLSBuiltinCAOptions(%#v) = %#v, %v; want %#v", tt.in, got, err, tt.want)
		}
		if describeTLSBuiltinCAOptions(got) == "" {
			t.Errorf("empty option description for %#v", got)
		}
	}
	for _, curve := range []string{"P256", "prime256v1", "P384", "P521", "secp521r1"} {
		if _, err := normalizeTLSBuiltinCACurve(curve); err != nil {
			t.Errorf("normalizeTLSBuiltinCACurve(%q): %v", curve, err)
		}
	}
}

func TestTLSBuiltinCAOptionErrors(t *testing.T) {
	tests := []api.TLSBuiltinCAOptions{
		{Issuer: string([]byte{0xff})},
		{Issuer: strings.Repeat("a", tlsBuiltinCAMaxIssuerRunes+1)},
		{ValidityDays: -1},
		{KeyAlgorithm: "unknown"},
		{KeyAlgorithm: "ecdsa", Curve: "unknown"},
		{KeyAlgorithm: "rsa", KeyBits: 1},
		{KeyAlgorithm: "rsa", KeyBits: tlsBuiltinCAMaxRSAKeyBits + 1},
	}
	for _, options := range tests {
		if _, err := normalizeTLSBuiltinCAOptions(options); err == nil {
			t.Errorf("normalizeTLSBuiltinCAOptions(%#v) succeeded", options)
		}
	}
}

func TestTLSBuiltinCAKeyAndSignatureHelpers(t *testing.T) {
	for _, options := range []api.TLSBuiltinCAOptions{
		{KeyAlgorithm: "ecdsa", Curve: "P256"},
		{KeyAlgorithm: "ecdsa", Curve: "P384"},
		{KeyAlgorithm: "ecdsa", Curve: "P521"},
		{KeyAlgorithm: "ed25519"},
	} {
		signer, pemText, err := generateTLSBuiltinCAKey(options)
		if err != nil || signer == nil || pemText == "" {
			t.Errorf("generateTLSBuiltinCAKey(%#v) = %T, %q, %v", options, signer, pemText, err)
		}
		if tlsBuiltinCASignatureAlgorithm(options) == x509.UnknownSignatureAlgorithm {
			t.Errorf("unknown signature algorithm for %#v", options)
		}
	}
	if _, _, err := generateTLSBuiltinCAKey(api.TLSBuiltinCAOptions{KeyAlgorithm: "ecdsa", Curve: "bad"}); err == nil {
		t.Fatal("bad ECDSA curve succeeded")
	}
	if _, _, err := generateTLSBuiltinCAKey(api.TLSBuiltinCAOptions{KeyAlgorithm: "bad"}); err == nil {
		t.Fatal("bad key algorithm succeeded")
	}
	if tlsBuiltinCASignatureAlgorithm(api.TLSBuiltinCAOptions{KeyAlgorithm: "rsa", KeyBits: 2048}) != x509.SHA256WithRSA ||
		tlsBuiltinCASignatureAlgorithm(api.TLSBuiltinCAOptions{KeyAlgorithm: "rsa", KeyBits: 4096}) != x509.SHA512WithRSA ||
		tlsBuiltinCASignatureAlgorithm(api.TLSBuiltinCAOptions{KeyAlgorithm: "bad"}) != x509.UnknownSignatureAlgorithm {
		t.Fatal("RSA or unknown signature algorithm is incorrect")
	}
	if tlsBuiltinCACurveName(elliptic.P256()) != "P256" || tlsBuiltinCACurveName(elliptic.P384()) != "P384" || tlsBuiltinCACurveName(elliptic.P521()) != "P521" || tlsBuiltinCACurveName(nil) != "" {
		t.Fatal("tlsBuiltinCACurveName returned an incorrect value")
	}
}

func TestTLSBuiltinCAParsingErrorsAndDescriptions(t *testing.T) {
	if describeTLSBuiltinCACert(nil) != "certificate=<nil>" {
		t.Fatal("nil certificate description is incorrect")
	}
	cert := &x509.Certificate{Issuer: pkix.Name{CommonName: "issuer"}, NotAfter: time.Unix(100, 0)}
	if !strings.Contains(describeTLSBuiltinCACert(&tlsBuiltinCA{certificate: cert}), "issuer") {
		t.Fatal("certificate description has no issuer")
	}
	for _, typ := range []string{"PRIVATE KEY", "EC PRIVATE KEY", "RSA PRIVATE KEY", "UNKNOWN"} {
		if _, err := parseTLSBuiltinCAPrivateKeyDER(typ, []byte("bad")); err == nil {
			t.Errorf("parseTLSBuiltinCAPrivateKeyDER(%q) succeeded", typ)
		}
	}
	for _, text := range []string{"", "trailing", "-----BEGIN UNKNOWN-----\nAQID\n-----END UNKNOWN-----\n"} {
		if _, _, err := splitTLSBuiltinCACombinedPEM(text); err == nil {
			t.Errorf("splitTLSBuiltinCACombinedPEM(%q) succeeded", text)
		}
	}
	if days := deriveTLSBuiltinCAValidityDays(&x509.Certificate{NotBefore: time.Unix(0, 0), NotAfter: time.Unix(36*60*60, 0)}); days != 2 {
		t.Fatalf("deriveTLSBuiltinCAValidityDays = %d, want 2", days)
	}
	if days := deriveTLSBuiltinCAValidityDays(&x509.Certificate{NotBefore: time.Unix(10, 0), NotAfter: time.Unix(0, 0)}); days != 0 {
		t.Fatalf("invalid validity days = %d", days)
	}
}
