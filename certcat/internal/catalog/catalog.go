package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

const FormatVersion = 1

type Document struct {
	FormatVersion int      `json:"format_version"`
	GeneratedAt   string   `json:"generated_at,omitempty"`
	GeneratedBy   string   `json:"generated_by,omitempty"`
	Entries       []Record `json:"entries"`
}

type Record struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind"`
	Name              string            `json:"name,omitempty"`
	Subject           string            `json:"subject,omitempty"`
	Issuer            string            `json:"issuer,omitempty"`
	SerialHex         string            `json:"serial_hex,omitempty"`
	NotBefore         string            `json:"not_before,omitempty"`
	NotAfter          string            `json:"not_after,omitempty"`
	CertificateSHA256 string            `json:"certificate_sha256,omitempty"`
	SPKISHA256        string            `json:"spki_sha256,omitempty"`
	HazardClass       string            `json:"hazard_class,omitempty"`
	Action            string            `json:"action"`
	Severity          string            `json:"severity"`
	Recommendation    string            `json:"recommendation,omitempty"`
	Scope             Scope             `json:"scope,omitempty"`
	Conditions        map[string]string `json:"conditions,omitempty"`
	Sources           []Source          `json:"sources,omitempty"`
}

type Scope struct {
	Basis                     string   `json:"basis,omitempty"`
	PermittedDNSSubtrees      []string `json:"permitted_dns_subtrees,omitempty"`
	PermittedIPCIDRs          []string `json:"permitted_ip_cidrs,omitempty"`
	RequiresExplicitAllowlist bool     `json:"requires_explicit_allowlist,omitempty"`
	RejectIPTargets           bool     `json:"reject_ip_targets,omitempty"`
	Context                   string   `json:"context,omitempty"`
}

type Source struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	RetrievedAt string `json:"retrieved_at,omitempty"`
	Note        string `json:"note,omitempty"`
}

func LoadDocument(path string) (*Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if doc.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("%s: unsupported format_version %d", path, doc.FormatVersion)
	}
	for n := range doc.Entries {
		NormalizeRecord(&doc.Entries[n])
		if err := ValidateRecord(doc.Entries[n]); err != nil {
			return nil, fmt.Errorf("%s entry %d: %w", path, n, err)
		}
	}
	return &doc, nil
}

func WriteDocument(path string, doc Document) error {
	doc.FormatVersion = FormatVersion
	for n := range doc.Entries {
		NormalizeRecord(&doc.Entries[n])
		if err := ValidateRecord(doc.Entries[n]); err != nil {
			return fmt.Errorf("entry %d: %w", n, err)
		}
	}
	sortRecords(doc.Entries)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func MergeDocuments(docs ...*Document) ([]Record, error) {
	var out []Record
	keys := map[string]int{}
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, rec := range doc.Entries {
			NormalizeRecord(&rec)
			if err := ValidateRecord(rec); err != nil {
				return nil, err
			}
			found := -1
			for _, key := range matchKeys(rec) {
				if n, ok := keys[key]; ok {
					found = n
					break
				}
			}
			if found < 0 {
				found = len(out)
				out = append(out, rec)
			} else {
				out[found] = mergeRecords(out[found], rec)
			}
			for _, key := range matchKeys(out[found]) {
				keys[key] = found
			}
		}
	}
	sortRecords(out)
	return out, nil
}

func NormalizeRecord(rec *Record) {
	rec.ID = strings.TrimSpace(rec.ID)
	rec.Kind = strings.TrimSpace(rec.Kind)
	rec.Action = strings.TrimSpace(rec.Action)
	rec.Severity = strings.TrimSpace(rec.Severity)
	rec.CertificateSHA256 = normalizeFingerprint(rec.CertificateSHA256)
	rec.SPKISHA256 = normalizeFingerprint(rec.SPKISHA256)
	rec.SerialHex = normalizeFingerprint(rec.SerialHex)
	rec.Scope.PermittedDNSSubtrees = cleanStrings(rec.Scope.PermittedDNSSubtrees, strings.ToLower)
	rec.Scope.PermittedIPCIDRs = cleanStrings(rec.Scope.PermittedIPCIDRs, nil)
	rec.Sources = cleanSources(rec.Sources)
	if rec.Conditions != nil && len(rec.Conditions) == 0 {
		rec.Conditions = nil
	}
}

func ValidateRecord(rec Record) error {
	if rec.ID == "" {
		return errors.New("id is required")
	}
	if rec.Kind == "" {
		return fmt.Errorf("%s: kind is required", rec.ID)
	}
	if rec.Action == "" {
		return fmt.Errorf("%s: action is required", rec.ID)
	}
	if rec.Severity == "" {
		return fmt.Errorf("%s: severity is required", rec.ID)
	}
	if rec.CertificateSHA256 == "" && rec.SPKISHA256 == "" {
		return fmt.Errorf("%s: at least one fingerprint is required", rec.ID)
	}
	if rec.CertificateSHA256 != "" && len(rec.CertificateSHA256) != 64 {
		return fmt.Errorf("%s: certificate_sha256 must have 64 hex characters", rec.ID)
	}
	if rec.SPKISHA256 != "" && len(rec.SPKISHA256) != 64 {
		return fmt.Errorf("%s: spki_sha256 must have 64 hex characters", rec.ID)
	}
	return nil
}

func MachineSourcesValid(doc Document) error {
	for n, rec := range doc.Entries {
		if len(rec.Sources) == 0 {
			return fmt.Errorf("entry %d %s has no source", n, rec.ID)
		}
		for _, src := range rec.Sources {
			if src.ID == "" || src.URL == "" || src.RetrievedAt == "" {
				return fmt.Errorf("entry %d %s has incomplete source", n, rec.ID)
			}
		}
	}
	return nil
}

func mergeRecords(a, b Record) Record {
	if actionRank(b.Action) > actionRank(a.Action) {
		a.Action = b.Action
	}
	if severityRank(b.Severity) > severityRank(a.Severity) {
		a.Severity = b.Severity
	}
	a.ID = firstNonEmpty(a.ID, b.ID)
	a.Kind = chooseKind(a.Kind, b.Kind)
	a.Name = firstNonEmpty(a.Name, b.Name)
	a.Subject = firstNonEmpty(a.Subject, b.Subject)
	a.Issuer = firstNonEmpty(a.Issuer, b.Issuer)
	a.SerialHex = firstNonEmpty(a.SerialHex, b.SerialHex)
	a.NotBefore = firstNonEmpty(a.NotBefore, b.NotBefore)
	a.NotAfter = firstNonEmpty(a.NotAfter, b.NotAfter)
	a.CertificateSHA256 = firstNonEmpty(a.CertificateSHA256, b.CertificateSHA256)
	a.SPKISHA256 = firstNonEmpty(a.SPKISHA256, b.SPKISHA256)
	a.HazardClass = firstNonEmpty(a.HazardClass, b.HazardClass)
	a.Recommendation = firstNonEmpty(a.Recommendation, b.Recommendation)
	a.Scope = mergeScope(a.Scope, b.Scope)
	a.Conditions = mergeMap(a.Conditions, b.Conditions)
	a.Sources = mergeSources(a.Sources, b.Sources)
	return a
}

func mergeScope(a, b Scope) Scope {
	a.Basis = firstNonEmpty(a.Basis, b.Basis)
	a.PermittedDNSSubtrees = mergeStrings(a.PermittedDNSSubtrees, b.PermittedDNSSubtrees)
	a.PermittedIPCIDRs = mergeStrings(a.PermittedIPCIDRs, b.PermittedIPCIDRs)
	a.RequiresExplicitAllowlist = a.RequiresExplicitAllowlist || b.RequiresExplicitAllowlist
	a.RejectIPTargets = a.RejectIPTargets || b.RejectIPTargets
	a.Context = firstNonEmpty(a.Context, b.Context)
	return a
}

func mergeMap(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range b {
		out[k] = v
	}
	for k, v := range a {
		out[k] = v
	}
	return out
}

func mergeSources(a, b []Source) []Source {
	seen := map[string]Source{}
	var order []string
	for _, src := range append(a, b...) {
		key := src.ID + "\x00" + src.URL
		if _, ok := seen[key]; !ok {
			order = append(order, key)
			seen[key] = src
			continue
		}
		old := seen[key]
		if old.RetrievedAt == "" {
			old.RetrievedAt = src.RetrievedAt
		}
		if old.Note == "" {
			old.Note = src.Note
		}
		seen[key] = old
	}
	out := make([]Source, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key])
	}
	return out
}

func cleanSources(in []Source) []Source {
	var out []Source
	seen := map[string]bool{}
	for _, src := range in {
		src.ID = strings.TrimSpace(src.ID)
		src.URL = strings.TrimSpace(src.URL)
		src.RetrievedAt = strings.TrimSpace(src.RetrievedAt)
		src.Note = strings.TrimSpace(src.Note)
		if src.ID == "" && src.URL == "" {
			continue
		}
		key := src.ID + "\x00" + src.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, src)
	}
	return out
}

func cleanStrings(in []string, transform func(string) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if transform != nil {
			item = transform(item)
		}
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func mergeStrings(a, b []string) []string {
	return cleanStrings(append(slices.Clone(a), b...), nil)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func chooseKind(a, b string) string {
	if kindRank(b) > kindRank(a) {
		return b
	}
	return firstNonEmpty(a, b)
}

func matchKeys(rec Record) []string {
	var keys []string
	if rec.CertificateSHA256 != "" {
		keys = append(keys, "certificate_sha256:"+rec.CertificateSHA256)
	}
	if rec.SPKISHA256 != "" {
		keys = append(keys, "spki_sha256:"+rec.SPKISHA256)
	}
	return keys
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		a := sortKey(records[i])
		b := sortKey(records[j])
		return a < b
	})
}

func sortKey(rec Record) string {
	for _, key := range matchKeys(rec) {
		return key
	}
	return rec.ID
}

func normalizeFingerprint(fingerprint string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r >= 'a' && r <= 'f':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'F':
			return r
		default:
			return -1
		}
	}, fingerprint))
}

func actionRank(action string) int {
	switch action {
	case "deny_even_if_locally_installed":
		return 100
	case "deny":
		return 90
	case "enforce_platform_cutoff":
		return 80
	case "follow_platform_policy_or_deny":
		return 70
	case "narrow_scope", "offer_scoped_service_anchor":
		return 60
	case "warn":
		return 50
	default:
		return 0
	}
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 100
	case "high":
		return 80
	case "warning":
		return 60
	case "info":
		return 20
	default:
		return 0
	}
}

func kindRank(kind string) int {
	switch kind {
	case "hazard_certificate":
		return 100
	case "threat_feed_leaf":
		return 80
	case "narrow_scope_ca":
		return 60
	case "service_private_anchor":
		return 40
	default:
		return 0
	}
}
