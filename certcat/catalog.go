package certcat

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strings"
)

type RecordKind uint8

const (
	RecordKindUnknown RecordKind = iota
	RecordKindHazardCertificate
	RecordKindNarrowScopeCA
	RecordKindServicePrivateAnchor
	RecordKindThreatFeedLeaf
)

func (k RecordKind) String() string {
	switch k {
	case RecordKindHazardCertificate:
		return "hazard_certificate"
	case RecordKindNarrowScopeCA:
		return "narrow_scope_ca"
	case RecordKindServicePrivateAnchor:
		return "service_private_anchor"
	case RecordKindThreatFeedLeaf:
		return "threat_feed_leaf"
	default:
		return ""
	}
}

type MatchKind uint8

const (
	MatchKindUnknown MatchKind = iota
	MatchKindCertificateSHA256
	MatchKindSPKISHA256
)

func (k MatchKind) String() string {
	switch k {
	case MatchKindCertificateSHA256:
		return "certificate_sha256"
	case MatchKindSPKISHA256:
		return "spki_sha256"
	default:
		return ""
	}
}

type Action uint8

const (
	ActionUnknown Action = iota
	ActionDeny
	ActionDenyEvenIfLocal
	ActionWarn
	ActionNarrowScope
	ActionScopedServiceAnchor
	ActionFollowPlatformOrDeny
	ActionEnforcePlatformCutoff
)

func (a Action) String() string {
	switch a {
	case ActionDeny:
		return "deny"
	case ActionDenyEvenIfLocal:
		return "deny_even_if_locally_installed"
	case ActionWarn:
		return "warn"
	case ActionNarrowScope:
		return "narrow_scope"
	case ActionScopedServiceAnchor:
		return "offer_scoped_service_anchor"
	case ActionFollowPlatformOrDeny:
		return "follow_platform_policy_or_deny"
	case ActionEnforcePlatformCutoff:
		return "enforce_platform_cutoff"
	default:
		return ""
	}
}

type Severity uint8

const (
	SeverityUnknown Severity = iota
	SeverityInfo
	SeverityWarning
	SeverityHigh
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityHigh:
		return "high"
	case SeverityCritical:
		return "critical"
	default:
		return ""
	}
}

type HazardClass uint8

const (
	HazardClassUnknown HazardClass = iota
	HazardClassChromiumCertificateBlocklist
	HazardClassChromiumSPKIBlocklist
	HazardClassInterceptionCA
	HazardClassMalwareOrCommandAndControlLeaf
	HazardClassPrivateKeyDisclosure
	HazardClassStateInterceptionCA
	HazardClassTimeScopedRootProgramDistrust
	HazardClassGovernmentCA
)

func (c HazardClass) String() string {
	switch c {
	case HazardClassChromiumCertificateBlocklist:
		return "chromium_certificate_blocklist"
	case HazardClassChromiumSPKIBlocklist:
		return "chromium_spki_blocklist"
	case HazardClassGovernmentCA:
		return "government_ca"
	case HazardClassInterceptionCA:
		return "interception_ca"
	case HazardClassMalwareOrCommandAndControlLeaf:
		return "malware_or_command_and_control_leaf"
	case HazardClassPrivateKeyDisclosure:
		return "private_key_disclosure"
	case HazardClassStateInterceptionCA:
		return "state_interception_ca"
	case HazardClassTimeScopedRootProgramDistrust:
		return "time_scoped_root_program_distrust"
	default:
		return ""
	}
}

type Recommendation uint8

const (
	RecommendationUnknown Recommendation = iota
	RecommendationApplyTargetPlatformDistrustCutoff
	RecommendationDenyExactCertificateEvenIfLocal
	RecommendationDenyExactLeafCertificate
	RecommendationDenyExactMatch
	RecommendationDenyExactSPKI
	RecommendationOnlyUseForExplicitCloudflareOriginHosts
	RecommendationTrustOnlyForDeclaredScope
	RecommendationOnlyUseForRussianGovernmentResources
)

func (r Recommendation) String() string {
	switch r {
	case RecommendationApplyTargetPlatformDistrustCutoff:
		return "apply_target_platform_distrust_cutoff"
	case RecommendationDenyExactCertificateEvenIfLocal:
		return "deny_exact_certificate_even_if_user_or_os_installed"
	case RecommendationDenyExactLeafCertificate:
		return "deny_exact_leaf_certificate"
	case RecommendationDenyExactMatch:
		return "deny_exact_match"
	case RecommendationDenyExactSPKI:
		return "deny_exact_spki"
	case RecommendationOnlyUseForExplicitCloudflareOriginHosts:
		return "only_use_for_explicit_cloudflare_origin_hosts"
	case RecommendationOnlyUseForRussianGovernmentResources:
		return "only_use_for_russian_government_resources"
	case RecommendationTrustOnlyForDeclaredScope:
		return "trust_only_for_declared_scope"
	default:
		return ""
	}
}

type ScopeBasis uint8

const (
	ScopeBasisUnknown ScopeBasis = iota
	ScopeBasisServiceContext
	ScopeBasisX509NameConstraints
)

func (b ScopeBasis) String() string {
	switch b {
	case ScopeBasisServiceContext:
		return "service_context"
	case ScopeBasisX509NameConstraints:
		return "x509_name_constraints"
	default:
		return ""
	}
}

type Fingerprint [sha256.Size]byte

type Record struct {
	Fingerprint    Fingerprint
	Scope          *Scope
	PlatformCutoff *PlatformCutoff
	MatchKind      MatchKind
	Kind           RecordKind
	HazardClass    HazardClass
	Action         Action
	Severity       Severity
	Recommendation Recommendation
}

type Scope struct {
	Basis                     ScopeBasis
	PermittedDNSSubtrees      []string
	PermittedIPCIDRs          []string
	RequiresExplicitAllowlist bool
	RejectIPTargets           bool
}

type PlatformCutoff struct {
	SCTTimestampAfter string
	EnforcementStart  string
	FirstMajorVersion uint16
}

type Index struct {
	byCertificateSHA256 map[Fingerprint][]int
	bySPKISHA256        map[Fingerprint][]int
	records             []Record
}

func NewIndex(records []Record) *Index {
	idx := &Index{
		byCertificateSHA256: map[Fingerprint][]int{},
		bySPKISHA256:        map[Fingerprint][]int{},
		records:             records,
	}
	for i := range records {
		if records[i].Fingerprint.IsZero() {
			continue
		}
		switch records[i].MatchKind {
		case MatchKindCertificateSHA256:
			idx.byCertificateSHA256[records[i].Fingerprint] = append(idx.byCertificateSHA256[records[i].Fingerprint], i)
		case MatchKindSPKISHA256:
			idx.bySPKISHA256[records[i].Fingerprint] = append(idx.bySPKISHA256[records[i].Fingerprint], i)
		}
	}
	return idx
}

func (i *Index) Lookup(kind MatchKind, fingerprint string) []Record {
	fp, ok := ParseFingerprint(fingerprint)
	if !ok {
		return nil
	}
	return i.LookupFingerprint(kind, fp)
}

func (i *Index) LookupFingerprint(kind MatchKind, fingerprint Fingerprint) []Record {
	if i == nil {
		return nil
	}
	var matches []int
	switch kind {
	case MatchKindCertificateSHA256:
		matches = i.byCertificateSHA256[fingerprint]
	case MatchKindSPKISHA256:
		matches = i.bySPKISHA256[fingerprint]
	default:
		return nil
	}
	out := make([]Record, 0, len(matches))
	for _, n := range matches {
		out = append(out, i.records[n])
	}
	return out
}

func (i *Index) MatchCertificate(cert *x509.Certificate) []Record {
	if cert == nil {
		return nil
	}
	seen := map[matchKey]bool{}
	out := appendMatches(nil, seen, i.LookupFingerprint(MatchKindCertificateSHA256, certificateSHA256Fingerprint(cert)))
	out = appendMatches(out, seen, i.LookupFingerprint(MatchKindSPKISHA256, spkiSHA256Fingerprint(cert)))
	return out
}

func (i *Index) MatchChain(chain []*x509.Certificate) []Record {
	seen := map[matchKey]bool{}
	var out []Record
	for _, cert := range chain {
		out = appendMatches(out, seen, i.MatchCertificate(cert))
	}
	return out
}

type matchKey struct {
	kind        MatchKind
	fingerprint Fingerprint
}

func appendMatches(out []Record, seen map[matchKey]bool, records []Record) []Record {
	for _, rec := range records {
		key := matchKey{kind: rec.MatchKind, fingerprint: rec.Fingerprint}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rec)
	}
	return out
}

func CertificateSHA256(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	return certificateSHA256Fingerprint(cert).String()
}

func SPKISHA256(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	return spkiSHA256Fingerprint(cert).String()
}

func certificateSHA256Fingerprint(cert *x509.Certificate) Fingerprint {
	return sha256.Sum256(cert.Raw)
}

func spkiSHA256Fingerprint(cert *x509.Certificate) Fingerprint {
	return sha256.Sum256(cert.RawSubjectPublicKeyInfo)
}

func (f Fingerprint) String() string {
	return strings.ToUpper(hex.EncodeToString(f[:]))
}

func (f Fingerprint) IsZero() bool {
	return f == Fingerprint{}
}

func ParseFingerprint(fingerprint string) (Fingerprint, bool) {
	var out Fingerprint
	var hexChars [sha256.Size * 2]byte
	n := 0
	for _, r := range fingerprint {
		var c byte
		switch {
		case r >= '0' && r <= '9':
			c = byte(r)
		case r >= 'a' && r <= 'f':
			c = byte(r - 'a' + 'A')
		case r >= 'A' && r <= 'F':
			c = byte(r)
		default:
			continue
		}
		if n == len(hexChars) {
			return Fingerprint{}, false
		}
		hexChars[n] = c
		n++
	}
	if n != len(hexChars) {
		return Fingerprint{}, false
	}
	if _, err := hex.Decode(out[:], hexChars[:]); err != nil {
		return Fingerprint{}, false
	}
	return out, true
}

// DNSNameMatchesSubtree reports whether a normalized ASCII DNS name is inside
// an RFC 5280 dNSName subtree. Callers must IDNA-normalize names before calling.
func DNSNameMatchesSubtree(host, subtree string) bool {
	host = normalizeDNSName(host)
	subtree = normalizeDNSName(subtree)
	if host == "" || subtree == "" {
		return false
	}
	if strings.HasPrefix(subtree, ".") {
		base := strings.TrimPrefix(subtree, ".")
		return strings.HasSuffix(host, "."+base)
	}
	return host == subtree || strings.HasSuffix(host, "."+subtree)
}

func normalizeDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}
