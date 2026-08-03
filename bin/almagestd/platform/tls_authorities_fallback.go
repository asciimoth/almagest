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
