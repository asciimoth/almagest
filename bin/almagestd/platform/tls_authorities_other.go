//go:build !linux && !windows

package platform

import (
	"context"

	"github.com/asciimoth/gonnect/tlspolicy"
)

// OSAuthorityFetcher returns fallback certificate-authority candidates on
// platforms whose native trust stores are not implemented yet.
func OSAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		return fallbackAuthorityCandidates(ctx)
	})
}
