//go:build windows

package platform

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

const windowsRootStoreName = "Root"

type windowsAuthorityStore struct {
	location uint32
	source   string
}

var windowsAuthorityStores = []windowsAuthorityStore{
	{location: windows.CERT_SYSTEM_STORE_CURRENT_USER, source: `windows:CurrentUser\Root`},
	{location: windows.CERT_SYSTEM_STORE_LOCAL_MACHINE, source: `windows:LocalMachine\Root`},
}

var windowsOpenAuthorityStore = openWindowsAuthorityStore

// OSAuthorityFetcher returns public certificate candidates from the Root
// stores for the daemon's current Windows identity and the local machine,
// followed by the embedded fallback roots. Windows sources come first so that
// duplicate certificates retain local-source priority in the authority
// catalog. The candidates remain untrusted until an Almagest TLS policy
// selects them.
func OSAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		if forceFallbackAuthorities() {
			return fallbackAuthorityCandidates(ctx)
		}
		candidates, err := windowsFetchAuthorities(ctx, windowsAuthorityStores)
		if err != nil {
			return nil, err
		}
		fallback, err := fallbackAuthorityCandidates(ctx)
		if err != nil {
			return nil, err
		}
		return append(candidates, fallback...), nil
	})
}

func windowsFetchAuthorities(ctx context.Context, stores []windowsAuthorityStore) ([]tlspolicy.AuthorityCandidate, error) {
	var out []tlspolicy.AuthorityCandidate
	for _, store := range stores {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidates, err := windowsFetchAuthoritiesFromStore(ctx, store)
		if err != nil {
			return nil, fmt.Errorf("read certificate store %s: %w", store.source, err)
		}
		out = append(out, candidates...)
	}
	return out, nil
}

func windowsFetchAuthoritiesFromStore(ctx context.Context, spec windowsAuthorityStore) (out []tlspolicy.AuthorityCandidate, err error) {
	store, err := windowsOpenAuthorityStore(spec.location, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := windows.CertCloseStore(store, 0); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close store: %w", closeErr))
		}
	}()

	var previous *windows.CertContext
	for {
		if err := ctx.Err(); err != nil {
			if previous != nil {
				_ = windows.CertFreeCertificateContext(previous)
			}
			return nil, err
		}
		certificate, enumErr := windows.CertEnumCertificatesInStore(store, previous)
		previous = certificate
		if enumErr != nil {
			if errors.Is(enumErr, syscall.Errno(windows.CRYPT_E_NOT_FOUND)) {
				return out, nil
			}
			return nil, fmt.Errorf("enumerate certificates: %w", enumErr)
		}
		if certificate == nil || certificate.EncodedCert == nil || certificate.Length == 0 {
			if certificate != nil {
				_ = windows.CertFreeCertificateContext(certificate)
			}
			return nil, errors.New("enumerate certificates: invalid empty certificate context")
		}
		der := bytes.Clone(unsafe.Slice(certificate.EncodedCert, certificate.Length))
		out = append(out, tlspolicy.AuthorityCandidate{
			DER: der,
			Source: tlspolicy.AuthoritySource{
				Name:  spec.source,
				Trust: tlspolicy.TrustHintAnchor,
			},
		})
	}
}

func openWindowsAuthorityStore(location uint32, readOnly bool) (windows.Handle, error) {
	return openWindowsNamedAuthorityStore(windowsRootStoreName, location, readOnly)
}

func openWindowsNamedAuthorityStore(storeName string, location uint32, readOnly bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(storeName)
	if err != nil {
		return 0, err
	}
	flags := location
	if readOnly {
		flags |= windows.CERT_STORE_READONLY_FLAG | windows.CERT_STORE_OPEN_EXISTING_FLAG
	}
	return windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		0,
		flags,
		uintptr(unsafe.Pointer(name)),
	)
}
