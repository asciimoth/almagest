// Package tlsplatform defines the closed, bounded TLS platform snapshot that
// trusted launch code can give to the restricted daemon runtime.
package tlsplatform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tlspolicy"
	"github.com/kataras/golog"
)

const (
	MaxSnapshotBytes = protocol.MaxConfigBytes
	MaxItems         = 4096
	MaxItemBytes     = 1 << 20
	MaxSourceIDBytes = 128
)

// Source is a closed platform source. It cannot carry a path, URL, store name,
// registry key, or another host selector from the restricted runtime.
type Source uint8

const (
	SourceNone Source = iota
	SourceOS
)

func (s Source) Valid() bool { return s == SourceNone || s == SourceOS }

// Request selects only broker-owned platform TLS inputs and sets result
// ceilings. CRL and distrust collection stay behind the protected CRL gate.
type Request struct {
	Source          Source `json:"source"`
	IncludeCRLs     bool   `json:"include_crls"`
	IncludeDistrust bool   `json:"include_distrust"`
	MaxBytes        uint32 `json:"max_bytes"`
	MaxItems        uint32 `json:"max_items"`
}

func DefaultRequest() Request {
	return Request{Source: SourceOS, MaxBytes: MaxSnapshotBytes, MaxItems: MaxItems}
}

// ConfiguredRequest applies only the common protected CRL gate and the closed
// Windows revocation mode. Online retrieval is not implemented because it
// needs separate route and host-effect authority.
func ConfiguredRequest(enabled bool, windowsRevocation string) (Request, error) {
	request := DefaultRequest()
	switch windowsRevocation {
	case "", "off":
	case "cache-only":
		if !enabled {
			return Request{}, fmt.Errorf("%w: Windows revocation requires enabled CRL support", protocol.ErrInvalidContract)
		}
		request.IncludeCRLs = true
	case "online":
		return Request{}, fmt.Errorf("%w: online Windows revocation is not implemented", protocol.ErrCapabilityUnavailable)
	default:
		return Request{}, fmt.Errorf("%w: invalid Windows revocation mode", protocol.ErrInvalidContract)
	}
	request.IncludeDistrust = enabled
	return request, nil
}

func (r Request) Validate() error {
	if !r.Source.Valid() || r.Source != SourceOS ||
		r.MaxBytes == 0 || r.MaxBytes > MaxSnapshotBytes || r.MaxItems == 0 || r.MaxItems > MaxItems {
		return fmt.Errorf("%w: invalid TLS platform request", protocol.ErrInvalidContract)
	}
	return nil
}

type AuthorityBlob struct {
	SourceID string `json:"source_id"`
	DER      []byte `json:"der"`
}

type DistrustBlob struct {
	SourceID string       `json:"source_id"`
	Kind     DistrustKind `json:"kind"`
	Value    []byte       `json:"value"`
}

type CRLBlob struct {
	SourceID string `json:"source_id"`
	DER      []byte `json:"der"`
}

// DistrustKind is a closed selector type. Platform code can report an exact
// certificate, SPKI digest, or issuer-and-serial assertion. It cannot ask R to
// open a platform store or interpret a provider-specific object.
type DistrustKind uint8

const (
	DistrustNone DistrustKind = iota
	DistrustCertificateDER
	DistrustCertificateSHA256
	DistrustSPKISHA256
	DistrustIssuerSerial
)

func (k DistrustKind) Valid() bool {
	return k >= DistrustCertificateDER && k <= DistrustIssuerSerial
}

type ProviderState string

const (
	ProviderDisabled    ProviderState = "disabled"
	ProviderUnsupported ProviderState = "unsupported"
	ProviderUnavailable ProviderState = "unavailable"
	ProviderActive      ProviderState = "active"
)

func (s ProviderState) Valid() bool {
	return slices.Contains([]ProviderState{ProviderDisabled, ProviderUnsupported, ProviderUnavailable, ProviderActive}, s)
}

// ProviderStatus describes a fixed platform integration without exposing a
// path, registry key, native error, or platform handle to R.
type ProviderStatus struct {
	Source     string        `json:"source"`
	State      ProviderState `json:"state"`
	ObservedAt int64         `json:"observed_at,omitempty"`
	ItemCount  uint32        `json:"item_count,omitempty"`
}

// Snapshot contains literal platform data only. Provider-specific objects and
// host selectors are deliberately not representable.
type Snapshot struct {
	Authorities []AuthorityBlob  `json:"authorities,omitempty"`
	Distrust    []DistrustBlob   `json:"distrust,omitempty"`
	CRLs        []CRLBlob        `json:"crls,omitempty"`
	Providers   []ProviderStatus `json:"providers,omitempty"`
}

func (s Snapshot) Validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(s.Authorities)+len(s.Distrust)+len(s.CRLs)+len(s.Providers) > int(request.MaxItems) ||
		(!request.IncludeDistrust && len(s.Distrust) != 0) || (!request.IncludeCRLs && len(s.CRLs) != 0) {
		return protocol.ErrLimitExceeded
	}
	seen := make(map[string]struct{}, len(s.Authorities)+len(s.Distrust)+len(s.CRLs))
	providerSeen := make(map[string]struct{}, len(s.Providers))
	total := 0
	check := func(kind, id string, data []byte) error {
		if id == "" || len(id) > MaxSourceIDBytes || !utf8.ValidString(id) || len(data) == 0 || len(data) > MaxItemBytes {
			return fmt.Errorf("%w: invalid TLS platform %s", protocol.ErrInvalidContract, kind)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate TLS platform member", protocol.ErrInvalidContract)
		}
		seen[id] = struct{}{}
		if total > int(request.MaxBytes)-len(data) {
			return protocol.ErrLimitExceeded
		}
		total += len(data)
		return nil
	}
	for _, value := range s.Authorities {
		if err := check("authority", value.SourceID, value.DER); err != nil {
			return err
		}
	}
	for _, value := range s.Distrust {
		if !value.Kind.Valid() {
			return fmt.Errorf("%w: invalid TLS platform distrust kind", protocol.ErrInvalidContract)
		}
		if err := check("distrust", value.SourceID, value.Value); err != nil {
			return err
		}
	}
	for _, value := range s.CRLs {
		if err := check("crl", value.SourceID, value.DER); err != nil {
			return err
		}
	}
	for _, value := range s.Providers {
		if value.Source == "" || len(value.Source) > MaxSourceIDBytes || !utf8.ValidString(value.Source) || !value.State.Valid() || value.ObservedAt < 0 || value.ItemCount > MaxItems {
			return fmt.Errorf("%w: invalid TLS platform provider status", protocol.ErrInvalidContract)
		}
		if _, duplicate := providerSeen[value.Source]; duplicate {
			return fmt.Errorf("%w: duplicate TLS platform provider status", protocol.ErrInvalidContract)
		}
		providerSeen[value.Source] = struct{}{}
	}
	return nil
}

func Marshal(snapshot Snapshot, request Request) ([]byte, error) {
	if err := snapshot.Validate(request); err != nil {
		return nil, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(data) > int(request.MaxBytes) {
		return nil, protocol.ErrLimitExceeded
	}
	return data, nil
}

func Unmarshal(data []byte, request Request) (Snapshot, error) {
	if len(data) == 0 || len(data) > int(request.MaxBytes) {
		return Snapshot{}, protocol.ErrLimitExceeded
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := rejectDuplicateJSONMembers(data); err != nil {
		if errors.Is(err, protocol.ErrLimitExceeded) {
			return Snapshot{}, protocol.ErrLimitExceeded
		}
		return Snapshot{}, protocol.ErrInvalidContract
	}
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, protocol.ErrInvalidContract
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Snapshot{}, protocol.ErrInvalidContract
	}
	if err := snapshot.Validate(request); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func rejectDuplicateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth >= protocol.MaxControlDepth {
			return protocol.ErrLimitExceeded
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter == '{' {
			seen := make(map[string]struct{})
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return protocol.ErrInvalidContract
				}
				if _, exists := seen[name]; exists {
					return protocol.ErrInvalidContract
				}
				seen[name] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		if delimiter == '[' {
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return protocol.ErrInvalidContract
	}
	return walk(0)
}

// Provider is implemented only by trusted bootstrap or broker code.
type Provider interface {
	Snapshot(context.Context, Request) (Snapshot, error)
}

type ProviderFunc func(context.Context, Request) (Snapshot, error)

func (f ProviderFunc) Snapshot(ctx context.Context, request Request) (Snapshot, error) {
	return f(ctx, request)
}

type authorityProvider struct{ fetcher tlspolicy.AuthorityFetcher }

// NewAuthorityProvider adapts the existing OS authority catalog to the closed
// snapshot contract. It intentionally does not collect CRLs or distrust data.
// Certificates that the TLS parser rejects are logged and omitted. A bad OS
// store entry must not prevent startup or hide the remaining valid entries.
func NewAuthorityProvider(fetcher tlspolicy.AuthorityFetcher) Provider {
	return authorityProvider{fetcher: fetcher}
}

type systemProvider struct {
	authorities Provider
	allowed     Request
}

// NewSystemProvider returns the platform provider used by direct mode and N.
// The allowed request is fixed from protected launch authority. R cannot turn
// on a provider later by changing fields in a refresh request.
func NewSystemProvider(allowed Request) Provider {
	return systemProvider{authorities: NewAuthorityProvider(SystemAuthorityFetcher()), allowed: allowed}
}

func (p systemProvider) Snapshot(ctx context.Context, request Request) (Snapshot, error) {
	if request != p.allowed {
		return Snapshot{}, fmt.Errorf("%w: TLS platform request exceeds launch authority", protocol.ErrInvalidContract)
	}
	result, err := p.authorities.Snapshot(ctx, DefaultRequest())
	if err != nil {
		return Snapshot{}, err
	}
	if !request.IncludeDistrust && !request.IncludeCRLs {
		return result, nil
	}
	system, err := systemSecuritySnapshot(ctx, request)
	if err != nil {
		return Snapshot{}, err
	}
	result.Distrust = system.Distrust
	result.CRLs = system.CRLs
	result.Providers = system.Providers
	if err := result.Validate(request); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

func (p authorityProvider) Snapshot(ctx context.Context, request Request) (Snapshot, error) {
	if p.fetcher == nil {
		return Snapshot{}, protocol.ErrInvalidContract
	}
	if err := request.Validate(); err != nil {
		return Snapshot{}, err
	}
	catalog, err := tlspolicy.FetchAuthorityCatalog(ctx, tlspolicy.AuthorityFetcherFunc(func(ctx context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		candidates, err := p.fetcher.FetchAuthorities(ctx)
		if err != nil {
			return nil, err
		}
		valid := make([]tlspolicy.AuthorityCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, err := tlspolicy.ParseAuthorityDER(candidate.DER); err != nil {
				golog.Warnf("TLS certificates: skipping invalid OS authority source=%s sha256=%x: %v",
					candidate.Source.Name, sha256.Sum256(candidate.DER), err)
				continue
			}
			valid = append(valid, candidate)
		}
		return valid, nil
	}))
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Authorities: make([]AuthorityBlob, 0, len(catalog.Records()))}
	for _, record := range catalog.Records() {
		der, err := record.Authority.MarshalBinary()
		if err != nil {
			return Snapshot{}, err
		}
		digest := sha256.Sum256(der)
		result.Authorities = append(result.Authorities, AuthorityBlob{SourceID: hex.EncodeToString(digest[:]), DER: der})
	}
	if err := result.Validate(request); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}
