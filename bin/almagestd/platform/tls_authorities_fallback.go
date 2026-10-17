package platform

import (
	"context"
	"os"

	"github.com/asciimoth/gonnect/tlspolicy"
	fallbackbundle "golang.org/x/crypto/x509roots/fallback/bundle"
)

const forceFallbackAuthoritiesEnv = "ALMAGEST_TLS_FORCE_FALLBACK_ROOTS"

func forceFallbackAuthorities() bool {
	return os.Getenv(forceFallbackAuthoritiesEnv) != ""
}

// FallbackAuthorityFetcher returns the public root catalog embedded in the
// daemon. Isolated runtime roles use it because their filesystem view does not
// include the host certificate store.
func FallbackAuthorityFetcher() tlspolicy.AuthorityFetcher {
	return tlspolicy.AuthorityFetcherFunc(fallbackAuthorityCandidates)
}

func fallbackAuthorityCandidates(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
	source := tlspolicy.AuthoritySource{
		Name:  "fallback:nss",
		Trust: tlspolicy.TrustHintAnchor,
	}
	var out []tlspolicy.AuthorityCandidate
	for root := range fallbackbundle.Roots() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The policy model stores DER roots only, so skip Go-only constraints.
		if root.Constraint != nil {
			continue
		}
		out = append(out, tlspolicy.AuthorityCandidate{
			DER:    append([]byte(nil), root.Certificate...),
			Source: source,
		})
	}
	return out, nil
}
