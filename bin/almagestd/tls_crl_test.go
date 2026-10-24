package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
	"github.com/asciimoth/gonnect/tlspolicy"
)

type testCRLAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCRLAuthority(t testing.TB, name string, serial int64) testCRLAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte(name)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCRLAuthority{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func newTestCRLLeaf(t testing.TB, issuer testCRLAuthority, name string, serial int64, isCA bool) *x509.Certificate {
	cert, _ := newTestCRLIssued(t, issuer, name, serial, isCA)
	return cert
}

func newTestCRLIssued(t testing.TB, issuer testCRLAuthority, name string, serial int64, isCA bool) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: isCA, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature}
	if isCA {
		template.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer.cert, &key.PublicKey, issuer.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func newTestCRLPEM(t testing.TB, issuer testCRLAuthority, number int64, thisUpdate, nextUpdate time.Time, entries []x509.RevocationListEntry, extensions []pkix.Extension) string {
	t.Helper()
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(number),
		ThisUpdate: thisUpdate, NextUpdate: nextUpdate, RevokedCertificateEntries: entries, ExtraExtensions: extensions}, issuer.cert, issuer.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
}

func testRevocation(serial int64, at time.Time) x509.RevocationListEntry {
	return x509.RevocationListEntry{SerialNumber: big.NewInt(serial), RevocationTime: at, ReasonCode: 1}
}

func parsedTestRevocations(t *testing.T, name, pemText string, now time.Time) immutableTLSRevocations {
	t.Helper()
	value := pemText
	revocations, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Enabled: true,
		Lists: map[string]ProtectedTLSCRLSourceConfig{name: {PEM: &value}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return revocations
}

func TestTLSCRLDisabledGateDoesNotParse(t *testing.T) {
	bad := "not a CRL"
	_, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Lists: map[string]ProtectedTLSCRLSourceConfig{
		"dormant": {PEM: &bad},
	}}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled lists error = %v", err)
	}
	got, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{}, time.Now())
	if err != nil || got.Enabled || len(got.Lists) != 0 {
		t.Fatalf("disabled revocations = %#v, %v", got, err)
	}
}

func TestTLSCRLAuthorityIsRejectedFromRegularConfig(t *testing.T) {
	_, err := ParseConfig([]byte(`{"Config":{"TLS":{"CRLs":{"Enabled":true}}}}`), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "only in protected config") {
		t.Fatalf("regular CRL config error = %v", err)
	}
}

func TestTLSCRLSourceRequiresExactlyOneInput(t *testing.T) {
	inline := "value"
	for _, source := range []ProtectedTLSCRLSourceConfig{{}, {PEM: &inline, File: "/list.pem"}} {
		_, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Enabled: true,
			Lists: map[string]ProtectedTLSCRLSourceConfig{"invalid": source}}, time.Now())
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("exclusive CRL input error = %v", err)
		}
	}
}

func TestTLSCRLInputLimitsFailBeforeParsing(t *testing.T) {
	lists := make(map[string]ProtectedTLSCRLSourceConfig, tlsCRLMaxSources+1)
	value := "invalid"
	for i := 0; i <= tlsCRLMaxSources; i++ {
		lists[big.NewInt(int64(i)).String()] = ProtectedTLSCRLSourceConfig{PEM: &value}
	}
	if _, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Enabled: true, Lists: lists}, time.Now()); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("source limit error = %v", err)
	}
	large := strings.Repeat("x", tlsCRLMaxSourceBytes+1)
	if _, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Enabled: true,
		Lists: map[string]ProtectedTLSCRLSourceConfig{"large": {PEM: &large}}}, time.Now()); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("source byte limit error = %v", err)
	}
}

func TestTLSCRLParserAcceptsMultipleStrictPEMBlocks(t *testing.T) {
	issuer := newTestCRLAuthority(t, "issuer", 1)
	now := time.Now().UTC().Truncate(time.Second)
	one := newTestCRLPEM(t, issuer, 1, now.Add(-time.Hour), now.Add(time.Hour), nil, nil)
	two := newTestCRLPEM(t, issuer, 2, now.Add(-time.Minute), now.Add(2*time.Hour), []x509.RevocationListEntry{testRevocation(9, now.Add(-time.Minute))}, nil)
	records, err := parseTLSCRLPEM("\n"+one+" \n"+two, now)
	if err != nil || len(records) != 2 || len(records[1].Revoked) != 1 {
		t.Fatalf("parse multiple CRLs = %d, %v", len(records), err)
	}
	for _, input := range []string{
		"prefix" + one,
		one + "suffix",
		strings.Replace(one, "X509 CRL", "CERTIFICATE", 2),
		"-----BEGIN X509 CRL-----\nAAAA\n-----END X509 CRL-----\n",
	} {
		if _, err := parseTLSCRLPEM(input, now); err == nil {
			t.Fatalf("parseTLSCRLPEM accepted invalid input %q", input[:min(len(input), 40)])
		}
	}
}

func TestTLSCRLParserRejectsInvalidTimesAndExtensions(t *testing.T) {
	issuer := newTestCRLAuthority(t, "issuer", 1)
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name string
		pem  string
	}{
		{"future", newTestCRLPEM(t, issuer, 1, now.Add(10*time.Minute), now.Add(time.Hour), nil, nil)},
		{"expired", newTestCRLPEM(t, issuer, 1, now.Add(-2*time.Hour), now.Add(-time.Hour), nil, nil)},
		{"issuing distribution point", newTestCRLPEM(t, issuer, 1, now.Add(-time.Hour), now.Add(time.Hour), nil,
			[]pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 28}, Value: []byte{0x30, 0x00}}})},
		{"delta", newTestCRLPEM(t, issuer, 1, now.Add(-time.Hour), now.Add(time.Hour), nil,
			[]pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 27}, Value: []byte{0x02, 0x01, 0x01}}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseTLSCRLPEM(test.pem, now); err == nil {
				t.Fatal("invalid CRL succeeded")
			}
		})
	}
}

func TestTLSCRLIssuerSerialMatchingAndUnboundStatus(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issuerA := newTestCRLAuthority(t, "issuer-a", 1)
	issuerB := newTestCRLAuthority(t, "issuer-b", 2)
	leafA := newTestCRLLeaf(t, issuerA, "leaf-a", 42, false)
	leafB := newTestCRLLeaf(t, issuerB, "leaf-b", 42, false)
	revocations := parsedTestRevocations(t, "a", newTestCRLPEM(t, issuerA, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(42, now.Add(-time.Minute))}, nil), now)
	if match, applicable, err := revocations.matchCertificate(leafA, issuerA.cert, now); err != nil || !applicable || match == nil {
		t.Fatalf("issuer A match = %#v, %t, %v", match, applicable, err)
	}
	if match, applicable, err := revocations.matchCertificate(leafB, issuerB.cert, now); err != nil || applicable || match != nil {
		t.Fatalf("issuer B serial collision = %#v, %t, %v", match, applicable, err)
	}
	entries := tlsCRLSourceEntries(revocations, map[string]tlsEffectiveCert{"wrong-issuer": {Certificate: issuerB.cert}}, now)
	if len(entries) != 1 || entries[0].Status != "unbound" {
		t.Fatalf("unbound entries = %#v", entries)
	}
}

func TestTLSCRLWrongSignatureStaysUnbound(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	realIssuer := newTestCRLAuthority(t, "same-subject", 1)
	forger := newTestCRLAuthority(t, "same-subject", 2)
	revocations := parsedTestRevocations(t, "forged", newTestCRLPEM(t, forger, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(1, now.Add(-time.Minute))}, nil), now)
	cert := tlsEffectiveCert{Certificate: realIssuer.cert}
	entries := tlsCRLSourceEntries(revocations, map[string]tlsEffectiveCert{"real": cert}, now)
	match, applicable, err := revocations.matchCertificate(realIssuer.cert, realIssuer.cert, now)
	if err != nil || applicable || match != nil || len(entries) != 1 || entries[0].Status != "unbound" {
		t.Fatalf("forged CRL affected issuer: match=%#v applicable=%t error=%v status=%#v", match, applicable, err, entries)
	}
}

func TestTLSCRLSelectionUsesNewestBaseList(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issuer := newTestCRLAuthority(t, "issuer", 1)
	leaf := newTestCRLLeaf(t, issuer, "leaf", 7, false)
	oldPEM := newTestCRLPEM(t, issuer, 1, now.Add(-2*time.Hour), now.Add(time.Hour), []x509.RevocationListEntry{testRevocation(7, now.Add(-time.Hour))}, nil)
	newPEM := newTestCRLPEM(t, issuer, 2, now.Add(-time.Hour), now.Add(2*time.Hour), nil, nil)
	revocations := parsedTestRevocations(t, "base", oldPEM+newPEM, now)
	match, applicable, err := revocations.matchCertificate(leaf, issuer.cert, now)
	if err != nil || !applicable || match != nil {
		t.Fatalf("new base CRL did not remove old entry: %#v, %t, %v", match, applicable, err)
	}
}

func TestTLSCRLSelectionRejectsAmbiguousNumber(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issuer := newTestCRLAuthority(t, "issuer", 1)
	one := parsedTestRevocations(t, "one", newTestCRLPEM(t, issuer, 7, now.Add(-2*time.Hour), now.Add(time.Hour), nil, nil), now)
	two := parsedTestRevocations(t, "two", newTestCRLPEM(t, issuer, 7, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(9, now.Add(-time.Minute))}, nil), now)
	records := append(one.recordsForIssuer(issuer.cert), two.recordsForIssuer(issuer.cert)...)
	if _, err := selectTLSCRL(records); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous selection error = %v", err)
	}
}

func TestTLSCRLStatusOrderingIsDeterministic(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issuerA := newTestCRLAuthority(t, "issuer-a", 1)
	issuerB := newTestCRLAuthority(t, "issuer-b", 2)
	valueA := newTestCRLPEM(t, issuerA, 1, now.Add(-time.Hour), now.Add(time.Hour), nil, nil)
	valueB := newTestCRLPEM(t, issuerB, 1, now.Add(-time.Hour), now.Add(time.Hour), nil, nil)
	revocations, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Enabled: true, Lists: map[string]ProtectedTLSCRLSourceConfig{
		"z-last": {PEM: &valueB}, "a-first": {PEM: &valueA},
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	entries := tlsCRLSourceEntries(revocations, nil, now)
	if len(entries) != 2 || entries[0].Name != "a-first" || entries[1].Name != "z-last" {
		t.Fatalf("CRL source ordering = %#v", entries)
	}
}

func TestTLSRevocationVerifierRejectsLeafAndAllowsAlternativeChain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issuer := newTestCRLAuthority(t, "issuer", 1)
	other := newTestCRLAuthority(t, "other", 2)
	revoked := newTestCRLLeaf(t, issuer, "revoked", 7, false)
	good := newTestCRLLeaf(t, other, "good", 8, false)
	revocations := parsedTestRevocations(t, "leaf-list", newTestCRLPEM(t, issuer, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(7, now.Add(-time.Minute))}, nil), now)
	verifier := tlsRevocationVerifier{revocations: revocations, now: func() time.Time { return now }}
	revokedChain := []*x509.Certificate{revoked, issuer.cert}
	if err := verifier.VerifyServer(tlspolicy.ServerVerification{AuthorizedChains: [][]*x509.Certificate{revokedChain}}); !errors.Is(err, errTLSCertificateRevoked) {
		t.Fatalf("revoked leaf error = %v", err)
	}
	if err := verifier.VerifyServer(tlspolicy.ServerVerification{AuthorizedChains: [][]*x509.Certificate{revokedChain, {good, other.cert}}}); err != nil {
		t.Fatalf("alternative good chain error = %v", err)
	}
	verifier.now = func() time.Time { return now.Add(2 * time.Hour) }
	if err := verifier.VerifyServer(tlspolicy.ServerVerification{AuthorizedChains: [][]*x509.Certificate{revokedChain}}); !errors.Is(err, errTLSRevocationStatusUnavailable) {
		t.Fatalf("expired CRL error = %v", err)
	}
}

func TestTLSRevocationVerifierRejectsIntermediate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := newTestCRLAuthority(t, "root", 1)
	intermediateCert, intermediateKey := newTestCRLIssued(t, root, "intermediate", 22, true)
	intermediate := testCRLAuthority{cert: intermediateCert, key: intermediateKey}
	leaf := newTestCRLLeaf(t, intermediate, "leaf", 23, false)
	revocations := parsedTestRevocations(t, "intermediate-list", newTestCRLPEM(t, root, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(22, now.Add(-time.Minute))}, nil), now)
	verifier := tlsRevocationVerifier{revocations: revocations, now: func() time.Time { return now }}
	if err := verifier.VerifyServer(tlspolicy.ServerVerification{AuthorizedChains: [][]*x509.Certificate{{leaf, intermediateCert, root.cert}}}); !errors.Is(err, errTLSCertificateRevoked) {
		t.Fatalf("revoked intermediate error = %v", err)
	}
}

func TestTLSCRLWarnsOSRootButExplicitPolicyStillWorks(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := newTestCRLAuthority(t, "revoked-root", 55)
	revocations := parsedTestRevocations(t, "root-list", newTestCRLPEM(t, root, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(55, now.Add(-time.Minute))}, nil), now)
	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{ImmutableTLS: immutableTLSData{
		Certs:       map[string]immutableTLSCert{"os-ca:revoked": {tlsCertificateRecord: tlsCertificateRecord{PEM: root.pem}, source: api.TLSSourceOS}},
		Policies:    map[string]immutableTLSPolicy{"explicit": {tlsPolicyRecord: tlsPolicyRecord{Spec: testTLSPolicy("os-ca:revoked")}, source: api.TLSSourceProtected}},
		Revocations: revocations,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	rootEntry := findTLSCertEntry(snapshot.Certs, "os-ca:revoked")
	defaultPolicy := findTLSPolicyEntry(snapshot.Policies, tlsDefaultPolicyName)
	if rootEntry == nil || len(rootEntry.RevocationMatches) != 1 || defaultPolicy == nil || len(defaultPolicy.Spec.TrustAnchors) != 0 {
		t.Fatalf("revoked root/default = %#v, %#v", rootEntry, defaultPolicy)
	}
	if _, err := manager.GetClientConfig("explicit", ""); err != nil {
		t.Fatalf("explicit policy no longer compiles: %v", err)
	}
}

func TestTLSCRLDefaultPolicyRebuildNotifiesWatchersOnOSRefresh(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	revokedRoot := newTestCRLAuthority(t, "revoked-root", 55)
	goodRoot := newTestCRLAuthority(t, "good-root", 56)
	revocations := parsedTestRevocations(t, "root-list", newTestCRLPEM(t, revokedRoot, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(55, now.Add(-time.Minute))}, nil), now)
	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{ImmutableTLS: immutableTLSData{
		Certs:       map[string]immutableTLSCert{"os-ca:revoked": {tlsCertificateRecord: tlsCertificateRecord{PEM: revokedRoot.pem}, source: api.TLSSourceOS}},
		Revocations: revocations,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	if _, err := manager.GetClientConfig(tlsDefaultPolicyName, "watching-node"); err != nil {
		t.Fatal(err)
	}
	result, err := manager.setTLSOSData(immutableTLSData{Certs: map[string]immutableTLSCert{
		"os-ca:good": {tlsCertificateRecord: tlsCertificateRecord{PEM: goodRoot.pem}, source: api.TLSSourceOS},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.policyNotifications) != 1 || len(result.policyNotifications[0].nodes) != 1 || result.policyNotifications[0].nodes[0] != "watching-node" {
		t.Fatalf("default policy notifications = %#v", result.policyNotifications)
	}
	if !manager.immutableTLS.Revocations.Enabled {
		t.Fatal("OS refresh discarded static CRLs")
	}
}

func TestProtectedTLSCRLCLIEnablementAndDuplicates(t *testing.T) {
	config := ProtectedConfig{TLS: ProtectedTLSConfig{CRLs: ProtectedTLSCRLsConfig{Lists: map[string]ProtectedTLSCRLSourceConfig{"protected": {PEM: new(string)}}}}}
	if err := config.MergeTLSCRLArguments(args.Args{}); err == nil {
		t.Fatal("dormant protected CRL succeeded")
	}
	absolute := filepath.Join(t.TempDir(), "list.pem")
	config = ProtectedConfig{}
	if err := config.MergeTLSCRLArguments(args.Args{TLSCRLFiles: []string{"cli=" + absolute}}); err != nil || !config.TLS.CRLs.Enabled || config.TLS.CRLs.Lists["cli"].ResolvedSource != "cli" {
		t.Fatalf("CLI CRL merge = %#v, %v", config.TLS.CRLs, err)
	}
	if err := config.MergeTLSCRLArguments(args.Args{TLSCRLFiles: []string{"cli=" + absolute}}); err == nil {
		t.Fatal("duplicate CLI/protected CRL name succeeded")
	}
}

func TestProtectedTLSSystemIntegrationsRequireGateAndRejectOnline(t *testing.T) {
	config, err := ParseConfig([]byte(`{}`), []byte(`{"TLS":{"CRLs":{"System":{"WindowsRevocation":"cache-only"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Protected.MergeTLSCRLArguments(args.Args{}); err == nil {
		t.Fatal("disabled system integration succeeded")
	}
	config.Protected.TLS.CRLs.Enabled = true
	request, err := tlsplatform.ConfiguredRequest(config.Protected.TLS.CRLs.Enabled, config.Protected.TLS.CRLs.System.WindowsRevocation)
	if err != nil || !request.IncludeCRLs || !request.IncludeDistrust {
		t.Fatalf("cache-only request = %#v, %v", request, err)
	}
	if _, err := tlsplatform.ConfiguredRequest(true, "online"); err == nil {
		t.Fatal("online Windows revocation succeeded")
	}
}

func FuzzParseTLSCRLPEM(f *testing.F) {
	now := time.Now().UTC().Truncate(time.Second)
	issuer := newTestCRLAuthority(f, "fuzz-issuer", 1)
	valid := newTestCRLPEM(f, issuer, 1, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.RevocationListEntry{testRevocation(42, now.Add(-time.Minute))}, nil)
	f.Add("")
	f.Add("-----BEGIN X509 CRL-----\nAAAA\n-----END X509 CRL-----\n")
	f.Add(valid)
	f.Add("\n" + valid + valid)
	f.Fuzz(func(t *testing.T, input string) {
		records, err := parseTLSCRLPEM(input, now)
		if len(input) > tlsCRLMaxSourceBytes && err == nil {
			t.Fatal("oversized CRL input was accepted")
		}
		if err != nil {
			return
		}
		if len(records) == 0 || len(records) > tlsCRLMaxLists {
			t.Fatalf("successful parse returned %d records", len(records))
		}
		for _, record := range records {
			validated, validateErr := validateTLSCRL(record.List, now)
			if validateErr != nil || !bytes.Equal(validated.DER, record.DER) {
				t.Fatalf("successful parse returned invalid record: %v", validateErr)
			}
		}
	})
}

func FuzzTLSCRLProtectedSource(f *testing.F) {
	now := time.Now().UTC().Truncate(time.Second)
	issuer := newTestCRLAuthority(f, "fuzz-config-issuer", 1)
	valid := newTestCRLPEM(f, issuer, 1, now.Add(-time.Hour), now.Add(time.Hour), nil, nil)
	f.Add("source", valid, "protected", true)
	f.Add("../source", "not a CRL", "cli", true)
	f.Add("source", "", "unknown", false)
	f.Fuzz(func(t *testing.T, name, input, source string, enabled bool) {
		value := input
		revocations, err := immutableTLSRevocationsFromConfig(ProtectedTLSCRLsConfig{Enabled: enabled,
			Lists: map[string]ProtectedTLSCRLSourceConfig{name: {PEM: &value, ResolvedSource: source}}}, now)
		if err != nil {
			return
		}
		if !enabled || !revocations.Enabled || len(revocations.Lists) != 1 {
			t.Fatalf("successful parse lost the protected CRL gate: %#v", revocations)
		}
		clone := cloneTLSRevocations(revocations)
		if len(clone.Lists) != 1 || len(clone.Index) != len(revocations.Index) {
			t.Fatal("successful parse did not produce a stable clone")
		}
	})
}
