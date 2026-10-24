//go:build windows

package tlsplatform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/asciimoth/gonnect/tlspolicy"
	"golang.org/x/sys/windows"
)

// SystemAuthorityFetcher reads the current-user and local-machine Root stores
// in N. The restricted runtime receives only the resulting DER certificates.
func SystemAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		if forceFallbackAuthorities() {
			return fallbackAuthorityCandidates(ctx)
		}
		stores := []struct {
			location uint32
			source   string
		}{
			{windows.CERT_SYSTEM_STORE_CURRENT_USER, `windows:CurrentUser\Root`},
			{windows.CERT_SYSTEM_STORE_LOCAL_MACHINE, `windows:LocalMachine\Root`},
		}
		var out []tlspolicy.AuthorityCandidate
		for _, spec := range stores {
			name, err := windows.UTF16PtrFromString("Root")
			if err != nil {
				return nil, err
			}
			store, err := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, 0,
				spec.location|windows.CERT_STORE_READONLY_FLAG|windows.CERT_STORE_OPEN_EXISTING_FLAG, uintptr(unsafe.Pointer(name)))
			if err != nil {
				return nil, fmt.Errorf("read certificate store %s: %w", spec.source, err)
			}
			candidates, readErr := readWindowsStore(ctx, store, spec.source)
			closeErr := windows.CertCloseStore(store, 0)
			if readErr != nil || closeErr != nil {
				return nil, errors.Join(readErr, closeErr)
			}
			out = append(out, candidates...)
		}
		fallback, err := fallbackAuthorityCandidates(ctx)
		if err != nil {
			return nil, err
		}
		return append(out, fallback...), nil
	})
}

func readWindowsStore(ctx context.Context, store windows.Handle, source string) ([]tlspolicy.AuthorityCandidate, error) {
	var out []tlspolicy.AuthorityCandidate
	var previous *windows.CertContext
	for {
		if err := ctx.Err(); err != nil {
			if previous != nil {
				_ = windows.CertFreeCertificateContext(previous)
			}
			return nil, err
		}
		certificate, err := windows.CertEnumCertificatesInStore(store, previous)
		previous = certificate
		if errors.Is(err, syscall.Errno(windows.CRYPT_E_NOT_FOUND)) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if certificate == nil || certificate.EncodedCert == nil || certificate.Length == 0 {
			return nil, errInvalidWindowsCertificate
		}
		out = append(out, tlspolicy.AuthorityCandidate{DER: bytes.Clone(unsafe.Slice(certificate.EncodedCert, certificate.Length)),
			Source: tlspolicy.AuthoritySource{Name: source, Trust: tlspolicy.TrustHintAnchor}})
	}
}

var errInvalidWindowsCertificate = errors.New("invalid empty Windows certificate context")
