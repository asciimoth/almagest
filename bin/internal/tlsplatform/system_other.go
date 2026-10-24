//go:build !linux && !windows

package tlsplatform

import (
	"context"
	"github.com/asciimoth/gonnect/tlspolicy"
)

func SystemAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		return fallbackAuthorityCandidates(ctx)
	})
}
