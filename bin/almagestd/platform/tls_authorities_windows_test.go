//go:build windows

package platform

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/tlspolicy"
	"golang.org/x/sys/windows"
)

func TestWindowsCertificateStoreCurrentIdentityDiscovery(t *testing.T) {
	der := windowsAuthorityTestCertificate(t, "Almagest current identity")
	remove := addWindowsAuthorityTestCertificate(t, windows.CERT_SYSTEM_STORE_CURRENT_USER, der)

	candidates, err := OSAuthorityFetcher().FetchAuthorities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsAuthorityCandidate(t, candidates, der, `windows:CurrentUser\Root`)

	remove()
	candidates, err = OSAuthorityFetcher().FetchAuthorities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsAuthorityCandidateAbsent(t, candidates, der)
}

func TestWindowsCertificateStoreMachineIdentityAndDuplicates(t *testing.T) {
	der := windowsAuthorityTestCertificate(t, "Almagest machine identity")
	removeUser := addWindowsAuthorityTestCertificate(t, windows.CERT_SYSTEM_STORE_CURRENT_USER, der)
	defer removeUser()
	removeMachine := addWindowsAuthorityTestCertificate(t, windows.CERT_SYSTEM_STORE_LOCAL_MACHINE, der)
	defer removeMachine()

	candidates, err := OSAuthorityFetcher().FetchAuthorities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tlspolicy.NewAuthorityCatalog(candidates)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range catalog.Records() {
		encoded, marshalErr := record.Authority.MarshalBinary()
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if !bytes.Equal(encoded, der) {
			continue
		}
		if len(record.Sources) != 2 ||
			record.Sources[0].Name != `windows:CurrentUser\Root` ||
			record.Sources[1].Name != `windows:LocalMachine\Root` {
			t.Fatalf("certificate sources = %#v, want current-user and machine stores", record.Sources)
		}
		return
	}
	t.Fatal("test certificate is absent from deduplicated catalog")
}

func TestWindowsCertificateStoreCancellationFailureAndFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OSAuthorityFetcher().FetchAuthorities(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fetch error = %v, want context.Canceled", err)
	}

	originalOpen := windowsOpenAuthorityStore
	t.Cleanup(func() { windowsOpenAuthorityStore = originalOpen })
	windowsOpenAuthorityStore = func(uint32, bool) (windows.Handle, error) {
		return 0, windows.ERROR_ACCESS_DENIED
	}
	if candidates, err := OSAuthorityFetcher().FetchAuthorities(context.Background()); err == nil || candidates != nil || !strings.Contains(err.Error(), `windows:CurrentUser\Root`) {
		t.Fatalf("failed-store result = (%d candidates, %v), want observable current-user error", len(candidates), err)
	}

	t.Setenv(forceFallbackAuthoritiesEnv, "1")
	candidates, err := OSAuthorityFetcher().FetchAuthorities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 || candidates[0].Source.Name != "fallback:nss" {
		t.Fatalf("forced fallback returned %d candidates, first source %q", len(candidates), candidates[0].Source.Name)
	}
}

func TestWindowsCertificateStoreRejectsMalformedCatalogEntry(t *testing.T) {
	_, err := tlspolicy.NewAuthorityCatalog([]tlspolicy.AuthorityCandidate{{
		DER: []byte("not a certificate"),
		Source: tlspolicy.AuthoritySource{
			Name:  `windows:CurrentUser\Root`,
			Trust: tlspolicy.TrustHintAnchor,
		},
	}})
	if err == nil {
		t.Fatal("malformed Windows store candidate was accepted")
	}
}

func windowsAuthorityTestCertificate(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func addWindowsAuthorityTestCertificate(t *testing.T, location uint32, der []byte) func() {
	t.Helper()
	store, err := openWindowsAuthorityStore(location, false)
	if err != nil {
		t.Fatalf("open test certificate store: %v", err)
	}
	input, err := windows.CertCreateCertificateContext(
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		&der[0], uint32(len(der)),
	)
	if err != nil {
		_ = windows.CertCloseStore(store, 0)
		t.Fatalf("create test certificate context: %v", err)
	}
	var added *windows.CertContext
	err = windows.CertAddCertificateContextToStore(store, input, windows.CERT_STORE_ADD_NEW, &added)
	_ = windows.CertFreeCertificateContext(input)
	if err != nil {
		_ = windows.CertCloseStore(store, 0)
		t.Fatalf("add test certificate: %v", err)
	}

	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		if err := windows.CertDeleteCertificateFromStore(added); err != nil {
			t.Errorf("remove exact test certificate: %v", err)
		}
		if err := windows.CertCloseStore(store, 0); err != nil {
			t.Errorf("close test certificate store: %v", err)
		}
	}
	t.Cleanup(remove)
	return remove
}

func assertWindowsAuthorityCandidate(t *testing.T, candidates []tlspolicy.AuthorityCandidate, der []byte, source string) {
	t.Helper()
	for _, candidate := range candidates {
		if bytes.Equal(candidate.DER, der) && candidate.Source.Name == source {
			return
		}
	}
	t.Fatalf("test certificate from %s is absent from %d candidates", source, len(candidates))
}

func assertWindowsAuthorityCandidateAbsent(t *testing.T, candidates []tlspolicy.AuthorityCandidate, der []byte) {
	t.Helper()
	for _, candidate := range candidates {
		if bytes.Equal(candidate.DER, der) {
			t.Fatal("removed test certificate remains in the reloaded catalog")
		}
	}
}
