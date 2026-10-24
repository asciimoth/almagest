package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
)

type immutableTLSDistrust struct {
	Findings  []tlsDistrustFinding
	Providers []api.TLSPlatformProviderStatus
}

type tlsDistrustFinding struct {
	Source string
	Kind   tlsplatform.DistrustKind
	Value  []byte
}

type tlsIssuerSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

func parseTLSPlatformDistrust(snapshot tlsplatform.Snapshot) (immutableTLSDistrust, error) {
	out := immutableTLSDistrust{Findings: make([]tlsDistrustFinding, 0, len(snapshot.Distrust)), Providers: make([]api.TLSPlatformProviderStatus, 0, len(snapshot.Providers))}
	for _, item := range snapshot.Distrust {
		switch item.Kind {
		case tlsplatform.DistrustCertificateDER:
			cert, err := x509.ParseCertificate(item.Value)
			if err != nil || !bytes.Equal(cert.Raw, item.Value) {
				return immutableTLSDistrust{}, fmt.Errorf("invalid platform distrust certificate %q", item.SourceID)
			}
		case tlsplatform.DistrustCertificateSHA256, tlsplatform.DistrustSPKISHA256:
			if len(item.Value) != sha256.Size {
				return immutableTLSDistrust{}, fmt.Errorf("invalid platform distrust digest %q", item.SourceID)
			}
		case tlsplatform.DistrustIssuerSerial:
			var selector tlsIssuerSerial
			rest, err := asn1.Unmarshal(item.Value, &selector)
			if err != nil || len(rest) != 0 || selector.Serial == nil || selector.Serial.Sign() < 0 || len(selector.Issuer.FullBytes) == 0 {
				return immutableTLSDistrust{}, fmt.Errorf("invalid platform distrust issuer and serial %q", item.SourceID)
			}
		default:
			return immutableTLSDistrust{}, fmt.Errorf("invalid platform distrust kind %q", item.SourceID)
		}
		out.Findings = append(out.Findings, tlsDistrustFinding{Source: item.SourceID, Kind: item.Kind, Value: bytes.Clone(item.Value)})
	}
	for _, status := range snapshot.Providers {
		out.Providers = append(out.Providers, api.TLSPlatformProviderStatus{Source: status.Source, State: string(status.State), ObservedAt: status.ObservedAt, ItemCount: status.ItemCount})
	}
	slices.SortFunc(out.Findings, func(a, b tlsDistrustFinding) int { return strings.Compare(a.Source, b.Source) })
	slices.SortFunc(out.Providers, func(a, b api.TLSPlatformProviderStatus) int { return strings.Compare(a.Source, b.Source) })
	return out, nil
}

func (d immutableTLSDistrust) matches(cert *x509.Certificate) []api.TLSDistrustMatch {
	if cert == nil {
		return nil
	}
	certDigest := sha256.Sum256(cert.Raw)
	spkiDigest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	var matches []api.TLSDistrustMatch
	for _, finding := range d.Findings {
		matched := false
		switch finding.Kind {
		case tlsplatform.DistrustCertificateDER:
			matched = bytes.Equal(cert.Raw, finding.Value)
		case tlsplatform.DistrustCertificateSHA256:
			matched = bytes.Equal(certDigest[:], finding.Value)
		case tlsplatform.DistrustSPKISHA256:
			matched = bytes.Equal(spkiDigest[:], finding.Value)
		case tlsplatform.DistrustIssuerSerial:
			var selector tlsIssuerSerial
			if rest, err := asn1.Unmarshal(finding.Value, &selector); err == nil && len(rest) == 0 && selector.Serial != nil {
				matched = bytes.Equal(cert.RawIssuer, selector.Issuer.FullBytes) && cert.SerialNumber.Cmp(selector.Serial) == 0
			}
		}
		if matched {
			matches = append(matches, api.TLSDistrustMatch{Source: finding.Source, Kind: distrustKindName(finding.Kind)})
		}
	}
	return matches
}

func distrustKindName(kind tlsplatform.DistrustKind) string {
	switch kind {
	case tlsplatform.DistrustCertificateDER:
		return "certificate"
	case tlsplatform.DistrustCertificateSHA256:
		return "certificate_sha256"
	case tlsplatform.DistrustSPKISHA256:
		return "spki_sha256"
	case tlsplatform.DistrustIssuerSerial:
		return "issuer_serial"
	default:
		return "unknown"
	}
}

func cloneTLSDistrust(in immutableTLSDistrust) immutableTLSDistrust {
	out := immutableTLSDistrust{Findings: make([]tlsDistrustFinding, len(in.Findings)), Providers: append([]api.TLSPlatformProviderStatus(nil), in.Providers...)}
	for i, finding := range in.Findings {
		out.Findings[i] = tlsDistrustFinding{Source: finding.Source, Kind: finding.Kind, Value: bytes.Clone(finding.Value)}
	}
	return out
}
