// Package tlsplatform defines the closed, bounded TLS platform snapshot that
// trusted launch code can give to the restricted daemon runtime.
package tlsplatform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tlspolicy"
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
// ceilings. CRL and distrust collection stay disabled until their providers
// are implemented behind the protected CRL enable gate.
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

func (r Request) Validate() error {
	if !r.Source.Valid() || r.Source != SourceOS || r.IncludeCRLs || r.IncludeDistrust ||
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
	SourceID string `json:"source_id"`
	DER      []byte `json:"der"`
}

type CRLBlob struct {
	SourceID string `json:"source_id"`
	DER      []byte `json:"der"`
}

// Snapshot contains literal platform data only. Provider-specific objects and
// host selectors are deliberately not representable.
type Snapshot struct {
	Authorities []AuthorityBlob `json:"authorities,omitempty"`
	Distrust    []DistrustBlob  `json:"distrust,omitempty"`
	CRLs        []CRLBlob       `json:"crls,omitempty"`
}

func (s Snapshot) Validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(s.Authorities)+len(s.Distrust)+len(s.CRLs) > int(request.MaxItems) ||
		(!request.IncludeDistrust && len(s.Distrust) != 0) || (!request.IncludeCRLs && len(s.CRLs) != 0) {
		return protocol.ErrLimitExceeded
	}
	seen := make(map[string]struct{}, len(s.Authorities)+len(s.Distrust)+len(s.CRLs))
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
		if err := check("distrust", value.SourceID, value.DER); err != nil {
			return err
		}
	}
	for _, value := range s.CRLs {
		if err := check("crl", value.SourceID, value.DER); err != nil {
			return err
		}
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
	var walk func() error
	walk = func() error {
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
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		if delimiter == '[' {
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return protocol.ErrInvalidContract
	}
	return walk()
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
func NewAuthorityProvider(fetcher tlspolicy.AuthorityFetcher) Provider {
	return authorityProvider{fetcher: fetcher}
}

func (p authorityProvider) Snapshot(ctx context.Context, request Request) (Snapshot, error) {
	if p.fetcher == nil {
		return Snapshot{}, protocol.ErrInvalidContract
	}
	if err := request.Validate(); err != nil {
		return Snapshot{}, err
	}
	catalog, err := tlspolicy.FetchAuthorityCatalog(ctx, p.fetcher)
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
