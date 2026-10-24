package tlsplatform

import (
	"context"
	"os"

	"github.com/asciimoth/gonnect/tlspolicy"
	fallbackbundle "golang.org/x/crypto/x509roots/fallback/bundle"
)

const forceFallbackAuthoritiesEnv = "ALMAGEST_TLS_FORCE_FALLBACK_ROOTS"

func fallbackAuthorityCandidates(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
	source := tlspolicy.AuthoritySource{Name: "fallback:nss", Trust: tlspolicy.TrustHintAnchor}
	var out []tlspolicy.AuthorityCandidate
	for root := range fallbackbundle.Roots() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if root.Constraint == nil {
			out = append(out, tlspolicy.AuthorityCandidate{DER: append([]byte(nil), root.Certificate...), Source: source})
		}
	}
	return out, nil
}

func forceFallbackAuthorities() bool { return os.Getenv(forceFallbackAuthoritiesEnv) != "" }
