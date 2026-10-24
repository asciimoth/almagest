package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/gonnect/tlspolicy"
)

const (
	tlsCRLClockTolerance    = 5 * time.Minute
	tlsCRLMaxSources        = 256
	tlsCRLMaxLists          = 1024
	tlsCRLMaxRevokedEntries = 1_000_000
	tlsCRLMaxSourceBytes    = 1 << 20
)

var (
	oidCRLAuthorityKeyID = []int{2, 5, 29, 35}
	oidCRLNumber         = []int{2, 5, 29, 20}
	oidCRLReason         = []int{2, 5, 29, 21}

	errTLSCertificateRevoked          = errors.New("TLS certificate is revoked")
	errTLSRevocationStatusUnavailable = errors.New("TLS revocation status unavailable")
)

// immutableTLSRevocations retains parsed, immutable CRLs outside certcat. CRL
// identity is issuer plus serial number, not a certificate fingerprint.
type immutableTLSRevocations struct {
	Enabled bool
	Lists   map[string]immutableTLSCRLSource
	Index   tlsRevocationIndex
}

type tlsRevocationIndex map[string][]tlsCRLReference

type tlsCRLReference struct {
	SourceName string
	Record     int
}

type immutableTLSCRLSource struct {
	Source  api.TLSSource
	Records []tlsCRLRecord
}

type tlsCRLRecord struct {
	DER     []byte
	List    *x509.RevocationList
	Revoked map[string]tlsCRLRevocation
}

type tlsCRLRevocation struct {
	Serial         []byte
	RevocationTime time.Time
	ReasonCode     int
}

type tlsCRLMatch struct {
	Source string
	Entry  tlsCRLRevocation
}

func immutableTLSRevocationsFromConfig(config ProtectedTLSCRLsConfig, now time.Time) (immutableTLSRevocations, error) {
	if !config.Enabled {
		if len(config.Lists) != 0 {
			return immutableTLSRevocations{}, fmt.Errorf("%w: TLS CRL lists are configured while CRL support is disabled", storage.ErrTlsConfigInvalidValue)
		}
		return immutableTLSRevocations{}, nil
	}
	if len(config.Lists) > tlsCRLMaxSources {
		return immutableTLSRevocations{}, fmt.Errorf("%w: too many TLS CRL sources", storage.ErrTlsConfigInvalidValue)
	}
	out := immutableTLSRevocations{Enabled: true, Lists: make(map[string]immutableTLSCRLSource, len(config.Lists))}
	totalLists, totalEntries := 0, 0
	seenDER := make(map[[sha256.Size]byte]string)
	for name, input := range config.Lists {
		if !validTLSName(name) {
			return immutableTLSRevocations{}, fmt.Errorf("%w: invalid TLS CRL source name %q", storage.ErrTlsConfigInvalidName, name)
		}
		path := strings.TrimSpace(input.File)
		if input.PEM != nil == (path != "") {
			return immutableTLSRevocations{}, fmt.Errorf("%w: TLS CRL %q must set exactly one of PEM or File", storage.ErrTlsConfigInvalidValue, name)
		}
		if path != "" && !filepath.IsAbs(path) {
			return immutableTLSRevocations{}, fmt.Errorf("%w: TLS CRL %q file path must be absolute", storage.ErrTlsConfigInvalidValue, name)
		}
		pemText := ""
		if input.PEM != nil {
			pemText = *input.PEM
		} else {
			data, err := readTLSCRLFile(path)
			if err != nil {
				return immutableTLSRevocations{}, fmt.Errorf("%w: read TLS CRL %q file %q: %v", storage.ErrTlsConfigInvalidValue, name, path, err)
			}
			pemText = string(data)
		}
		if len(pemText) > tlsCRLMaxSourceBytes {
			return immutableTLSRevocations{}, fmt.Errorf("%w: TLS CRL %q exceeds the 1 MiB source limit", storage.ErrTlsConfigInvalidValue, name)
		}
		records, err := parseTLSCRLPEM(pemText, now)
		if err != nil {
			return immutableTLSRevocations{}, fmt.Errorf("%w: TLS CRL %q: %v", storage.ErrTlsConfigInvalidValue, name, err)
		}
		totalLists += len(records)
		if totalLists > tlsCRLMaxLists {
			return immutableTLSRevocations{}, fmt.Errorf("%w: too many TLS CRLs", storage.ErrTlsConfigInvalidValue)
		}
		for _, record := range records {
			digest := sha256.Sum256(record.DER)
			if previous, duplicate := seenDER[digest]; duplicate {
				return immutableTLSRevocations{}, fmt.Errorf("%w: TLS CRL %q duplicates a list from %q", storage.ErrTlsConfigInvalidValue, name, previous)
			}
			seenDER[digest] = name
			totalEntries += len(record.Revoked)
			if totalEntries > tlsCRLMaxRevokedEntries {
				return immutableTLSRevocations{}, fmt.Errorf("%w: too many revoked certificate entries", storage.ErrTlsConfigInvalidValue)
			}
		}
		source := api.TLSSourceProtected
		if input.ResolvedSource == "cli" {
			source = api.TLSSourceCLI
		}
		out.Lists[name] = immutableTLSCRLSource{Source: source, Records: records}
	}
	out.Index = buildTLSCRLIndex(out.Lists)
	return out, nil
}

func buildTLSCRLIndex(lists map[string]immutableTLSCRLSource) tlsRevocationIndex {
	index := make(tlsRevocationIndex)
	for name, source := range lists {
		for recordIndex, record := range source.Records {
			issuer := string(record.List.RawIssuer)
			index[issuer] = append(index[issuer], tlsCRLReference{SourceName: name, Record: recordIndex})
		}
	}
	return index
}

func readTLSCRLFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, tlsCRLMaxSourceBytes+1))
	err = errors.Join(readErr, file.Close())
	if err != nil {
		return nil, err
	}
	if len(data) > tlsCRLMaxSourceBytes {
		return nil, errors.New("CRL source exceeds the 1 MiB limit")
	}
	return data, nil
}

// parseTLSCRLPEM accepts only one or more complete X509 CRL PEM blocks. This
// strict parser prevents a valid first block from hiding unrelated input.
func parseTLSCRLPEM(input string, now time.Time) ([]tlsCRLRecord, error) {
	rest := []byte(input)
	records := make([]tlsCRLRecord, 0, 1)
	revokedEntries := 0
	for len(bytes.TrimSpace(rest)) != 0 {
		if len(records) >= tlsCRLMaxLists {
			return nil, errors.New("too many X509 CRLs")
		}
		prefixEnd := bytes.Index(rest, []byte("-----BEGIN"))
		if prefixEnd < 0 || len(bytes.TrimSpace(rest[:prefixEnd])) != 0 {
			return nil, errors.New("non-whitespace data outside X509 CRL blocks")
		}
		block, next := pem.Decode(rest)
		if block == nil {
			return nil, errors.New("non-PEM data outside X509 CRL blocks")
		}
		if block.Type != "X509 CRL" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
		}
		list, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse X509 CRL: %w", err)
		}
		if len(list.RevokedCertificateEntries) > tlsCRLMaxRevokedEntries-revokedEntries {
			return nil, errors.New("too many revoked certificate entries")
		}
		revokedEntries += len(list.RevokedCertificateEntries)
		record, err := validateTLSCRL(list, now)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		rest = next
	}
	if len(records) == 0 {
		return nil, errors.New("no X509 CRL PEM blocks")
	}
	return records, nil
}

func validateTLSCRL(list *x509.RevocationList, now time.Time) (tlsCRLRecord, error) {
	if list == nil || len(list.Raw) == 0 || len(list.RawIssuer) == 0 {
		return tlsCRLRecord{}, errors.New("invalid empty X509 CRL")
	}
	if list.ThisUpdate.IsZero() || list.ThisUpdate.After(now.Add(tlsCRLClockTolerance)) {
		return tlsCRLRecord{}, errors.New("ThisUpdate is missing or in the future")
	}
	if list.NextUpdate.IsZero() || !list.NextUpdate.After(list.ThisUpdate) || !list.NextUpdate.After(now) {
		return tlsCRLRecord{}, errors.New("NextUpdate is missing, invalid, or expired")
	}
	for _, extension := range list.Extensions {
		if !extension.Id.Equal(oidCRLAuthorityKeyID) && !extension.Id.Equal(oidCRLNumber) {
			return tlsCRLRecord{}, fmt.Errorf("unsupported CRL extension %s", extension.Id.String())
		}
	}
	revoked := make(map[string]tlsCRLRevocation, len(list.RevokedCertificateEntries))
	for _, entry := range list.RevokedCertificateEntries {
		if entry.SerialNumber == nil || entry.SerialNumber.Sign() < 0 || entry.RevocationTime.IsZero() {
			return tlsCRLRecord{}, errors.New("invalid revoked certificate entry")
		}
		for _, extension := range entry.Extensions {
			if !extension.Id.Equal(oidCRLReason) {
				return tlsCRLRecord{}, fmt.Errorf("unsupported CRL entry extension %s", extension.Id.String())
			}
		}
		switch entry.ReasonCode {
		case 0, 1, 2, 3, 4, 5, 6, 8, 9, 10:
		default:
			return tlsCRLRecord{}, fmt.Errorf("unsupported CRL reason code %d", entry.ReasonCode)
		}
		serial := unsignedSerial(entry.SerialNumber)
		key := string(serial)
		if _, duplicate := revoked[key]; duplicate {
			return tlsCRLRecord{}, fmt.Errorf("duplicate revoked serial %x", serial)
		}
		revoked[key] = tlsCRLRevocation{Serial: serial, RevocationTime: entry.RevocationTime, ReasonCode: entry.ReasonCode}
	}
	return tlsCRLRecord{DER: bytes.Clone(list.Raw), List: list, Revoked: revoked}, nil
}

func unsignedSerial(serial *big.Int) []byte {
	if serial == nil {
		return nil
	}
	value := serial.Bytes()
	if len(value) == 0 {
		return []byte{0}
	}
	return bytes.Clone(value)
}

func (record tlsCRLRecord) appliesTo(issuer *x509.Certificate) bool {
	if issuer == nil || !bytes.Equal(record.List.RawIssuer, issuer.RawSubject) {
		return false
	}
	if len(record.List.AuthorityKeyId) != 0 && len(issuer.SubjectKeyId) != 0 && !bytes.Equal(record.List.AuthorityKeyId, issuer.SubjectKeyId) {
		return false
	}
	if issuer.KeyUsage != 0 && issuer.KeyUsage&x509.KeyUsageCRLSign == 0 {
		return false
	}
	return record.List.CheckSignatureFrom(issuer) == nil
}

type namedTLSCRLRecord struct {
	name   string
	source api.TLSSource
	record tlsCRLRecord
}

func (r immutableTLSRevocations) recordsForIssuer(issuer *x509.Certificate) []namedTLSCRLRecord {
	if !r.Enabled || issuer == nil {
		return nil
	}
	out := make([]namedTLSCRLRecord, 0)
	for _, reference := range r.Index[string(issuer.RawSubject)] {
		source, ok := r.Lists[reference.SourceName]
		if !ok || reference.Record < 0 || reference.Record >= len(source.Records) {
			continue
		}
		record := source.Records[reference.Record]
		if record.appliesTo(issuer) {
			out = append(out, namedTLSCRLRecord{name: reference.SourceName, source: source.Source, record: record})
		}
	}
	return out
}

func selectTLSCRL(records []namedTLSCRLRecord) (namedTLSCRLRecord, error) {
	if len(records) == 0 {
		return namedTLSCRLRecord{}, nil
	}
	slices.SortStableFunc(records, func(a, b namedTLSCRLRecord) int {
		if a.record.List.Number != nil && b.record.List.Number != nil {
			if compared := b.record.List.Number.Cmp(a.record.List.Number); compared != 0 {
				return compared
			}
		} else if a.record.List.Number != nil {
			return -1
		} else if b.record.List.Number != nil {
			return 1
		}
		if compared := b.record.List.ThisUpdate.Compare(a.record.List.ThisUpdate); compared != 0 {
			return compared
		}
		return strings.Compare(a.name, b.name)
	})
	selected := records[0]
	for _, other := range records[1:] {
		sameRank := false
		if selected.record.List.Number != nil && other.record.List.Number != nil {
			sameRank = selected.record.List.Number.Cmp(other.record.List.Number) == 0
		} else if selected.record.List.Number == nil && other.record.List.Number == nil {
			sameRank = selected.record.List.ThisUpdate.Equal(other.record.List.ThisUpdate)
		}
		if sameRank && !bytes.Equal(selected.record.DER, other.record.DER) {
			return namedTLSCRLRecord{}, errors.New("ambiguous TLS CRLs have the same selection rank")
		}
		if !sameRank {
			break
		}
	}
	return selected, nil
}

func (r immutableTLSRevocations) matchCertificate(cert, issuer *x509.Certificate, now time.Time) (*tlsCRLMatch, bool, error) {
	selected, err := selectTLSCRL(r.recordsForIssuer(issuer))
	if err != nil {
		return nil, true, err
	}
	if selected.record.List == nil {
		return nil, false, nil
	}
	if !selected.record.List.NextUpdate.After(now) {
		return nil, true, errTLSRevocationStatusUnavailable
	}
	entry, found := selected.record.Revoked[string(unsignedSerial(cert.SerialNumber))]
	if !found || entry.RevocationTime.After(now) {
		return nil, true, nil
	}
	return &tlsCRLMatch{Source: selected.name, Entry: entry}, true, nil
}

func (r immutableTLSRevocations) matchCatalogCertificate(cert *x509.Certificate, catalog []*x509.Certificate, now time.Time) (*tlsCRLMatch, error) {
	for _, issuer := range catalog {
		if cert == nil || issuer == nil || !bytes.Equal(cert.RawIssuer, issuer.RawSubject) || cert.CheckSignatureFrom(issuer) != nil {
			continue
		}
		match, _, err := r.matchCertificate(cert, issuer, now)
		if err != nil || match != nil {
			return match, err
		}
	}
	return nil, nil
}

type tlsRevocationVerifier struct {
	revocations immutableTLSRevocations
	platform    tlsplatform.RevocationChecker
	now         func() time.Time
}

func (v tlsRevocationVerifier) VerifyServer(verification tlspolicy.ServerVerification) error {
	if !v.revocations.Enabled && v.platform == nil {
		return nil
	}
	now := time.Now()
	if v.now != nil {
		now = v.now()
	}
	platformStates := make([]tlsplatform.RevocationState, len(verification.AuthorizedChains))
	if v.platform != nil {
		request, err := tlsplatform.RevocationRequestFromChains(verification.AuthorizedChains)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			result, checkErr := v.platform.CheckRevocation(ctx, request)
			cancel()
			if checkErr == nil && result.Validate(request) == nil {
				copy(platformStates, result.States)
			}
		}
	}
	var unavailable bool
	for chainIndex, chain := range verification.AuthorizedChains {
		chainRevoked, chainUnavailable := false, false
		if v.revocations.Enabled {
			for i := 0; i+1 < len(chain); i++ {
				match, applicable, err := v.revocations.matchCertificate(chain[i], chain[i+1], now)
				if err != nil {
					chainUnavailable = true
					break
				}
				if applicable && match != nil {
					chainRevoked = true
					break
				}
			}
		}
		// Cache-only platform failures, offline state, and unknown status use a
		// documented soft-fail policy. Only an explicit revoked result rejects
		// a chain. Static configured CRLs retain their fail-closed expiry rule.
		if chainIndex < len(platformStates) && platformStates[chainIndex] == tlsplatform.RevocationRevoked {
			chainRevoked = true
		}
		if !chainRevoked && !chainUnavailable {
			return nil
		}
		unavailable = unavailable || chainUnavailable
	}
	if unavailable {
		return errTLSRevocationStatusUnavailable
	}
	return errTLSCertificateRevoked
}

func tlsCRLSourceEntries(revocations immutableTLSRevocations, certs map[string]tlsEffectiveCert, now time.Time) []api.TLSCRLSourceEntry {
	entries := make([]api.TLSCRLSourceEntry, 0)
	for name, source := range revocations.Lists {
		for i, record := range source.Records {
			entryName := name
			if len(source.Records) > 1 {
				entryName = fmt.Sprintf("%s#%d", name, i+1)
			}
			entry := api.TLSCRLSourceEntry{Name: entryName, Source: source.Source, Status: "unbound", Issuer: record.List.Issuer.String(),
				ThisUpdate: record.List.ThisUpdate.UTC().Format(time.RFC3339Nano), NextUpdate: record.List.NextUpdate.UTC().Format(time.RFC3339Nano), RevokedCount: len(record.Revoked)}
			if record.List.Number != nil {
				entry.Number = record.List.Number.Text(10)
			}
			if !record.List.NextUpdate.After(now) {
				entry.Status = "expired"
				entry.Error = errTLSRevocationStatusUnavailable.Error()
			} else {
				for _, cert := range certs {
					if record.appliesTo(cert.Certificate) {
						entry.Status = "valid"
						break
					}
				}
			}
			entries = append(entries, entry)
		}
	}
	slices.SortFunc(entries, func(a, b api.TLSCRLSourceEntry) int { return strings.Compare(a.Name, b.Name) })
	return entries
}

func cloneTLSRevocations(in immutableTLSRevocations) immutableTLSRevocations {
	out := immutableTLSRevocations{Enabled: in.Enabled}
	if len(in.Lists) == 0 {
		return out
	}
	out.Lists = make(map[string]immutableTLSCRLSource, len(in.Lists))
	for name, source := range in.Lists {
		copySource := immutableTLSCRLSource{Source: source.Source, Records: make([]tlsCRLRecord, len(source.Records))}
		for i, record := range source.Records {
			copySource.Records[i] = tlsCRLRecord{DER: bytes.Clone(record.DER), List: record.List, Revoked: make(map[string]tlsCRLRevocation, len(record.Revoked))}
			for serial, entry := range record.Revoked {
				entry.Serial = bytes.Clone(entry.Serial)
				copySource.Records[i].Revoked[serial] = entry
			}
		}
		out.Lists[name] = copySource
	}
	out.Index = buildTLSCRLIndex(out.Lists)
	return out
}

func validateTLSRevocationSelections(data immutableTLSData) error {
	if !data.Revocations.Enabled {
		return nil
	}
	for name, record := range data.Certs {
		_, cert, err := parseTLSAuthorityPEM(record.PEM)
		if err != nil {
			return err
		}
		if _, err := selectTLSCRL(data.Revocations.recordsForIssuer(cert)); err != nil {
			return fmt.Errorf("TLS CRL selection for certificate %q: %w", name, err)
		}
	}
	return nil
}
