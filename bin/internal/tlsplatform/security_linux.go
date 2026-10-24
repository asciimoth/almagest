//go:build linux

package tlsplatform

import (
	"context"
	"os"
)

const linuxDistrustProvider = "linux:p11-kit"

// systemSecuritySnapshot reports Linux distrust capability conservatively.
// Debian-family certificate bundles contain positive anchors only. They are
// never interpreted as negative trust. A native p11-kit adapter is required
// before this provider can become active.
func systemSecuritySnapshot(ctx context.Context, request Request) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	state := ProviderUnsupported
	// A p11-kit trust module can exist on a host where this build cannot load
	// it. Report unavailable in that case, instead of silently claiming that
	// the effective layered distrust policy was read.
	for _, path := range []string{
		"/usr/lib64/pkcs11/p11-kit-trust.so",
		"/usr/lib/x86_64-linux-gnu/pkcs11/p11-kit-trust.so",
		"/usr/lib/pkcs11/p11-kit-trust.so",
	} {
		if _, err := os.Stat(path); err == nil {
			state = ProviderUnavailable
			break
		}
	}
	if !request.IncludeDistrust {
		state = ProviderDisabled
	}
	return Snapshot{Providers: []ProviderStatus{{Source: linuxDistrustProvider, State: state}}}, nil
}
