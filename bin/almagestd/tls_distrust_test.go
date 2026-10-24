package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
	"github.com/asciimoth/gonnect/tlspolicy"
)

func TestTLSDistrustMatchesAllNormalizedSelectorKinds(t *testing.T) {
	issuer := newTestCRLAuthority(t, "issuer", 1)
	leaf := newTestCRLLeaf(t, issuer, "leaf", 42, false)
	certDigest := sha256.Sum256(leaf.Raw)
	spkiDigest := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	issuerSerial, err := asn1.Marshal(tlsIssuerSerial{Issuer: asn1.RawValue{FullBytes: leaf.RawIssuer}, Serial: leaf.SerialNumber})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := tlsplatform.Snapshot{Distrust: []tlsplatform.DistrustBlob{
		{SourceID: "der", Kind: tlsplatform.DistrustCertificateDER, Value: leaf.Raw},
		{SourceID: "cert-digest", Kind: tlsplatform.DistrustCertificateSHA256, Value: certDigest[:]},
		{SourceID: "spki", Kind: tlsplatform.DistrustSPKISHA256, Value: spkiDigest[:]},
		{SourceID: "issuer-serial", Kind: tlsplatform.DistrustIssuerSerial, Value: issuerSerial},
	}, Providers: []tlsplatform.ProviderStatus{{Source: "test", State: tlsplatform.ProviderActive, ItemCount: 4}}}
	distrust, err := parseTLSPlatformDistrust(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	matches := distrust.matches(leaf)
	if len(matches) != 4 || len(distrust.Providers) != 1 || distrust.Providers[0].State != "active" {
		t.Fatalf("matches/status = %#v / %#v", matches, distrust.Providers)
	}
	other := newTestCRLLeaf(t, issuer, "other", 43, false)
	if matches := distrust.matches(other); len(matches) != 0 {
		t.Fatalf("other certificate matches = %#v", matches)
	}
}

func TestTLSDistrustWarnsAndOmitsOSRootFromDefault(t *testing.T) {
	root := newTestCRLAuthority(t, "blocked-root", 9)
	distrust := immutableTLSDistrust{Findings: []tlsDistrustFinding{{Source: "windows:blocked", Kind: tlsplatform.DistrustCertificateDER, Value: root.cert.Raw}}}
	manager, err := openKVManagerWithOptions(t.TempDir()+"/kv.db", bboltKVManagerOptions{ImmutableTLS: immutableTLSData{
		Certs: map[string]immutableTLSCert{"os-ca:blocked": {tlsCertificateRecord: tlsCertificateRecord{PEM: root.pem}, source: api.TLSSourceOS}}, Distrust: distrust,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	cert := findTLSCertEntry(snapshot.Certs, "os-ca:blocked")
	policy := findTLSPolicyEntry(snapshot.Policies, tlsDefaultPolicyName)
	if len(cert.DistrustMatches) != 1 || len(policy.Spec.TrustAnchors) != 0 {
		t.Fatalf("blocked root/default = %#v / %#v", cert, policy)
	}
}

func TestTLSDistrustManualRefreshAtomicallyRebuildsDefault(t *testing.T) {
	root := newTestCRLAuthority(t, "refresh-root", 12)
	name := "os-ca:refresh"
	certs := map[string]immutableTLSCert{name: {tlsCertificateRecord: tlsCertificateRecord{PEM: root.pem}, source: api.TLSSourceOS}}
	manager, err := openKVManagerWithOptions(t.TempDir()+"/kv.db", bboltKVManagerOptions{ImmutableTLS: immutableTLSData{
		Certs: certs, Distrust: immutableTLSDistrust{Findings: []tlsDistrustFinding{{Source: "system:block", Kind: tlsplatform.DistrustCertificateDER, Value: root.cert.Raw}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	manager.trackTLSPolicyWatcher(tlsDefaultPolicyName, "watcher")
	result, err := manager.setTLSOSData(immutableTLSData{Certs: certs, Distrust: immutableTLSDistrust{Providers: []api.TLSPlatformProviderStatus{{Source: "system", State: "active"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.policyNotifications) != 1 || len(result.policyNotifications[0].nodes) != 1 {
		t.Fatalf("notifications = %#v", result.policyNotifications)
	}
	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	cert := findTLSCertEntry(snapshot.Certs, name)
	policy := findTLSPolicyEntry(snapshot.Policies, tlsDefaultPolicyName)
	if cert == nil || len(cert.DistrustMatches) != 0 || policy == nil || len(policy.Spec.TrustAnchors) != 1 || policy.Spec.TrustAnchors[0].Cert != name {
		t.Fatalf("refreshed certificate/default = %#v / %#v", cert, policy)
	}
}

func TestTLSPlatformRevocationUsesSameAuthorizedChain(t *testing.T) {
	issuerA := newTestCRLAuthority(t, "issuer-a", 1)
	issuerB := newTestCRLAuthority(t, "issuer-b", 2)
	leafA := newTestCRLLeaf(t, issuerA, "leaf-a", 10, false)
	leafB := newTestCRLLeaf(t, issuerB, "leaf-b", 11, false)
	checker := tlsplatform.RevocationCheckerFunc(func(_ context.Context, request tlsplatform.RevocationRequest) (tlsplatform.RevocationResult, error) {
		return tlsplatform.RevocationResult{States: []tlsplatform.RevocationState{tlsplatform.RevocationRevoked, tlsplatform.RevocationGood}}, nil
	})
	verifier := tlsRevocationVerifier{platform: checker}
	if err := verifier.VerifyServer(tlspolicy.ServerVerification{AuthorizedChains: [][]*x509.Certificate{{leafA, issuerA.cert}, {leafB, issuerB.cert}}}); err != nil {
		t.Fatal(err)
	}
	checker = tlsplatform.RevocationCheckerFunc(func(_ context.Context, request tlsplatform.RevocationRequest) (tlsplatform.RevocationResult, error) {
		return tlsplatform.RevocationResult{States: []tlsplatform.RevocationState{tlsplatform.RevocationRevoked, tlsplatform.RevocationRevoked}}, nil
	})
	if err := (tlsRevocationVerifier{platform: checker}).VerifyServer(tlspolicy.ServerVerification{AuthorizedChains: [][]*x509.Certificate{{leafA, issuerA.cert}, {leafB, issuerB.cert}}}); !errors.Is(err, errTLSCertificateRevoked) {
		t.Fatalf("all revoked error = %v", err)
	}
}
