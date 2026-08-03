//go:build !linux

package platform

import (
	"context"

	"github.com/asciimoth/gonnect/tlspolicy"
)

// OSAuthorityFetcher returns no certificates on platforms whose native trust
// stores are not implemented yet. The daemon still creates the immutable
// "default" TLS policy; it is fail-closed when no OS certificates are present.
func OSAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		return nil, nil
	})
}
