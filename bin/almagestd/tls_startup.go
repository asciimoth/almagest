package main

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/gonnect/tlspolicy"
)

const (
	tlsOSCertFallbackLabel = "certificate"
	tlsOSCertLabelMaxRunes = 64
)

func loadOSTLSData(ctx context.Context, fetcher tlspolicy.AuthorityFetcher) (immutableTLSData, error) {
	catalog, err := tlspolicy.FetchAuthorityCatalog(ctx, fetcher)
	if err != nil {
		return immutableTLSData{}, err
	}
	out := immutableTLSData{
		Certs: map[string]immutableTLSCert{},
	}
	for _, record := range catalog.Records() {
		name := tlsOSCertName(record)
		der, err := record.Authority.MarshalBinary()
		if err != nil {
			return immutableTLSData{}, fmt.Errorf("marshal OS TLS authority %s: %w", record.CertificateFingerprint, err)
		}
		out.Certs[name] = immutableTLSCert{
			tlsCertificateRecord: tlsCertificateRecord{PEM: tlsPEMFromDER(der)},
			source:               api.TLSSourceOS,
		}
	}
	return out, nil
}

func mergeImmutableTLSData(values ...immutableTLSData) immutableTLSData {
	var out immutableTLSData
	for _, value := range values {
		if len(value.Certs) > 0 {
			if out.Certs == nil {
				out.Certs = map[string]immutableTLSCert{}
			}
			for name, cert := range value.Certs {
				out.Certs[name] = cert
			}
		}
		if len(value.Policies) > 0 {
			if out.Policies == nil {
				out.Policies = map[string]immutableTLSPolicy{}
			}
			for name, policy := range value.Policies {
				out.Policies[name] = policy
			}
		}
	}
	return out
}

func tlsOSCertName(record tlspolicy.AuthorityRecord) string {
	return tlsOSCertPrefix + tlsOSCertLabel(record) + ":" + record.CertificateFingerprint.String()
}

func tlsOSCertLabel(record tlspolicy.AuthorityRecord) string {
	if cert, err := record.Authority.Certificate(); err == nil {
		if label := tlsOSCertNamePart(cert.Subject.CommonName); label != "" {
			return label
		}
		if len(cert.Subject.Organization) > 0 {
			if label := tlsOSCertNamePart(strings.Join(cert.Subject.Organization, " ")); label != "" {
				return label
			}
		}
		if len(cert.DNSNames) > 0 {
			if label := tlsOSCertNamePart(cert.DNSNames[0]); label != "" {
				return label
			}
		}
	}
	if label := tlsOSCertNamePart(record.Subject); label != "" {
		return label
	}
	return tlsOSCertFallbackLabel
}

func tlsOSCertNamePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}

	var builder strings.Builder
	prevDash := false
	runes := 0
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if runes >= tlsOSCertLabelMaxRunes {
				break
			}
			builder.WriteRune(unicode.ToLower(r))
			prevDash = false
			runes++
			continue
		}
		if builder.Len() == 0 || prevDash {
			continue
		}
		if runes >= tlsOSCertLabelMaxRunes {
			break
		}
		builder.WriteByte('-')
		prevDash = true
		runes++
	}

	return strings.TrimRight(builder.String(), "-")
}
