package tlsplatform

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
)

const (
	MaxRevocationChains     = 8
	MaxRevocationChainCerts = 16
	MaxRevocationChainBytes = 256 << 10
)

type RevocationState string

const (
	RevocationGood          RevocationState = "good"
	RevocationRevoked       RevocationState = "revoked"
	RevocationUnknown       RevocationState = "unknown"
	RevocationOffline       RevocationState = "offline"
	RevocationPlatformError RevocationState = "platform_error"
)

func (s RevocationState) Valid() bool {
	switch s {
	case RevocationGood, RevocationRevoked, RevocationUnknown, RevocationOffline, RevocationPlatformError:
		return true
	default:
		return false
	}
}

type RevocationRequest struct {
	Chains [][][]byte `json:"chains"`
}

func (r RevocationRequest) Validate() error {
	if len(r.Chains) == 0 || len(r.Chains) > MaxRevocationChains {
		return errors.New("invalid platform revocation chain count")
	}
	total := 0
	for _, chain := range r.Chains {
		if len(chain) < 2 || len(chain) > MaxRevocationChainCerts {
			return errors.New("invalid platform revocation certificate count")
		}
		for _, der := range chain {
			if len(der) == 0 || total > MaxRevocationChainBytes-len(der) {
				return errors.New("platform revocation chain exceeds byte limit")
			}
			total += len(der)
			if cert, err := x509.ParseCertificate(der); err != nil || len(cert.Raw) != len(der) {
				return errors.New("invalid platform revocation certificate")
			}
		}
	}
	return nil
}

type RevocationResult struct {
	States []RevocationState `json:"states"`
}

func (r RevocationResult) Validate(request RevocationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(r.States) != len(request.Chains) {
		return errors.New("invalid platform revocation result count")
	}
	for _, state := range r.States {
		if !state.Valid() {
			return fmt.Errorf("invalid platform revocation state %q", state)
		}
	}
	return nil
}

type RevocationChecker interface {
	CheckRevocation(context.Context, RevocationRequest) (RevocationResult, error)
}

type RevocationCheckerFunc func(context.Context, RevocationRequest) (RevocationResult, error)

func (f RevocationCheckerFunc) CheckRevocation(ctx context.Context, request RevocationRequest) (RevocationResult, error) {
	return f(ctx, request)
}

func RevocationRequestFromChains(chains [][]*x509.Certificate) (RevocationRequest, error) {
	request := RevocationRequest{Chains: make([][][]byte, len(chains))}
	for i, chain := range chains {
		request.Chains[i] = make([][]byte, len(chain))
		for j, cert := range chain {
			if cert != nil {
				request.Chains[i][j] = append([]byte(nil), cert.Raw...)
			}
		}
	}
	return request, request.Validate()
}
