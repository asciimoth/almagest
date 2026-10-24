//go:build windows

package tlsplatform

import (
	"context"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	certContextRevocationType              = 1
	certVerifyRevChainFlag                 = 0x00000001
	certVerifyCacheOnlyBasedRevocationFlag = 0x00000002
	certVerifyRevAccumulativeTimeoutFlag   = 0x00000004
	windowsRevocationTimeoutMilliseconds   = 1500
)

var certVerifyRevocation = windows.NewLazySystemDLL("crypt32.dll").NewProc("CertVerifyRevocation")

type certRevocationStatus struct {
	Size             uint32
	Index            uint32
	Error            uint32
	Reason           uint32
	HasFreshnessTime int32
	FreshnessTime    uint32
}

type certRevocationPara struct {
	Size                uint32
	IssuerCert          *windows.CertContext
	CertStoreCount      uint32
	CertStores          unsafe.Pointer
	CRLStore            windows.Handle
	TimeToUse           unsafe.Pointer
	URLRetrievalTimeout uint32
	CheckFreshnessTime  int32
	FreshnessTime       uint32
	CurrentTime         unsafe.Pointer
	CRLInfo             unsafe.Pointer
}

type windowsRevocationChecker struct{}

func SystemRevocationChecker() RevocationChecker { return windowsRevocationChecker{} }

func (windowsRevocationChecker) CheckRevocation(ctx context.Context, request RevocationRequest) (RevocationResult, error) {
	if err := request.Validate(); err != nil {
		return RevocationResult{}, err
	}
	result := RevocationResult{States: make([]RevocationState, len(request.Chains))}
	for i, chain := range request.Chains {
		if err := ctx.Err(); err != nil {
			return RevocationResult{}, err
		}
		result.States[i] = checkWindowsChainRevocation(ctx, chain)
	}
	return result, result.Validate(request)
}

func checkWindowsChainRevocation(ctx context.Context, chain [][]byte) RevocationState {
	state := RevocationGood
	for i := 0; i+1 < len(chain); i++ {
		if ctx.Err() != nil {
			return RevocationPlatformError
		}
		certificate, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING, &chain[i][0], uint32(len(chain[i])))
		if err != nil {
			return RevocationPlatformError
		}
		issuer, issuerErr := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING, &chain[i+1][0], uint32(len(chain[i+1])))
		if issuerErr != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			return RevocationPlatformError
		}
		pairState := verifyWindowsCertificateRevocation(certificate, issuer)
		_ = windows.CertFreeCertificateContext(issuer)
		_ = windows.CertFreeCertificateContext(certificate)
		if pairState == RevocationRevoked {
			return pairState
		}
		if pairState != RevocationGood {
			state = pairState
		}
	}
	return state
}

func verifyWindowsCertificateRevocation(certificate, issuer *windows.CertContext) RevocationState {
	contexts := [1]unsafe.Pointer{unsafe.Pointer(certificate)}
	parameters := certRevocationPara{Size: uint32(unsafe.Sizeof(certRevocationPara{})), IssuerCert: issuer, URLRetrievalTimeout: windowsRevocationTimeoutMilliseconds}
	status := certRevocationStatus{Size: uint32(unsafe.Sizeof(certRevocationStatus{}))}
	result, _, _ := certVerifyRevocation.Call(
		windows.X509_ASN_ENCODING,
		certContextRevocationType,
		1,
		uintptr(unsafe.Pointer(&contexts[0])),
		certVerifyRevChainFlag|certVerifyCacheOnlyBasedRevocationFlag|certVerifyRevAccumulativeTimeoutFlag,
		uintptr(unsafe.Pointer(&parameters)),
		uintptr(unsafe.Pointer(&status)),
	)
	runtime.KeepAlive(contexts)
	runtime.KeepAlive(parameters)
	runtime.KeepAlive(status)
	if result != 0 {
		return RevocationGood
	}
	switch syscall.Errno(status.Error) {
	case syscall.Errno(windows.CRYPT_E_REVOKED):
		return RevocationRevoked
	case syscall.Errno(windows.CRYPT_E_NO_REVOCATION_CHECK), syscall.Errno(windows.CRYPT_E_NO_REVOCATION_DLL), syscall.Errno(windows.CRYPT_E_NOT_IN_REVOCATION_DATABASE):
		return RevocationUnknown
	case syscall.Errno(windows.CRYPT_E_REVOCATION_OFFLINE):
		return RevocationOffline
	default:
		return RevocationPlatformError
	}
}
