//go:build windows

package tlsplatform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsDistrustProvider   = "windows:disallowed"
	windowsRevocationProvider = "windows:revocation-cache"
)

// systemSecuritySnapshot reads effective Disallowed certificate stores by
// using Crypt32. It does not use the registry, a shell, PowerShell, certutil,
// or a network-capable chain builder.
func systemSecuritySnapshot(ctx context.Context, request Request) (Snapshot, error) {
	result := Snapshot{}
	observed := time.Now().UTC().Unix()
	if request.IncludeDistrust {
		items, state, err := readWindowsDisallowed(ctx)
		if err != nil {
			return Snapshot{}, err
		}
		result.Distrust = items
		result.Providers = append(result.Providers, ProviderStatus{Source: windowsDistrustProvider, State: state, ObservedAt: observed, ItemCount: uint32(len(items))})
	} else {
		result.Providers = append(result.Providers, ProviderStatus{Source: windowsDistrustProvider, State: ProviderDisabled})
	}
	state := ProviderDisabled
	if request.IncludeCRLs {
		// Cache-only checks are performed by the fixed Crypt32 checker. No CRL
		// or OCSP bytes and no platform handles cross into R.
		state = ProviderActive
	}
	result.Providers = append(result.Providers, ProviderStatus{Source: windowsRevocationProvider, State: state, ObservedAt: observed})
	return result, nil
}

func readWindowsDisallowed(ctx context.Context) ([]DistrustBlob, ProviderState, error) {
	locations := []uint32{
		windows.CERT_SYSTEM_STORE_CURRENT_USER,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		windows.CERT_SYSTEM_STORE_CURRENT_USER_GROUP_POLICY,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE_GROUP_POLICY,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE_ENTERPRISE,
	}
	name, err := windows.UTF16PtrFromString("Disallowed")
	if err != nil {
		return nil, ProviderUnavailable, err
	}
	byDigest := make(map[[sha256.Size]byte][]byte)
	opened := 0
	unavailable := false
	for index, location := range locations {
		if err := ctx.Err(); err != nil {
			return nil, ProviderUnavailable, err
		}
		store, openErr := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, 0,
			location|windows.CERT_STORE_READONLY_FLAG|windows.CERT_STORE_OPEN_EXISTING_FLAG, uintptr(unsafe.Pointer(name)))
		if openErr != nil {
			// Group Policy and enterprise stores are optional. A missing store is
			// not a provider failure; current-user or machine access is enough to
			// observe the effective stores available to this daemon identity.
			if errors.Is(openErr, syscall.Errno(windows.CRYPT_E_NOT_FOUND)) || errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) {
				continue
			}
			if index < 2 {
				unavailable = true
			}
			continue
		}
		opened++
		certificates, readErr := readWindowsStore(ctx, store, windowsDistrustProvider)
		closeErr := windows.CertCloseStore(store, 0)
		if readErr != nil || closeErr != nil {
			return nil, ProviderUnavailable, errors.Join(readErr, closeErr)
		}
		for _, candidate := range certificates {
			digest := sha256.Sum256(candidate.DER)
			byDigest[digest] = bytes.Clone(candidate.DER)
		}
	}
	if opened == 0 || unavailable {
		return nil, ProviderUnavailable, nil
	}
	items := make([]DistrustBlob, 0, len(byDigest))
	for digest, der := range byDigest {
		items = append(items, DistrustBlob{SourceID: "windows-disallowed-cert:" + hex.EncodeToString(digest[:]), Kind: DistrustCertificateDER, Value: der})
	}
	slices.SortFunc(items, func(a, b DistrustBlob) int { return bytes.Compare(a.Value, b.Value) })
	return items, ProviderActive, nil
}
