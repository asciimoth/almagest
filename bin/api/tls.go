package api

type TLSMethod string

const (
	MethodTLSSnapshot      TLSMethod = "snapshot"
	MethodTLSSubscribe     TLSMethod = "subscribe"
	MethodTLSUnsubscribe   TLSMethod = "unsubscribe"
	MethodTLSBuiltinCA     TLSMethod = "builtin_ca"
	MethodTLSSetBuiltinCA  TLSMethod = "set_builtin_ca_options"
	MethodTLSReloadOSCerts TLSMethod = "reload_os_certs"
	MethodTLSSetCert       TLSMethod = "set_cert"
	MethodTLSRenameCert    TLSMethod = "rename_cert"
	MethodTLSDeleteCert    TLSMethod = "delete_cert"
	MethodTLSSetPolicy     TLSMethod = "set_policy"
	MethodTLSRenamePolicy  TLSMethod = "rename_policy"
	MethodTLSDeletePolicy  TLSMethod = "delete_policy"

	EventTLSUpdate TLSMethod = "update"
)

// TLSMethods returns request methods for the TLS admin surface.
func TLSMethods() []string {
	return []string{
		string(MethodTLSSnapshot),
		string(MethodTLSSubscribe),
		string(MethodTLSUnsubscribe),
		string(MethodTLSBuiltinCA),
		string(MethodTLSSetBuiltinCA),
		string(MethodTLSReloadOSCerts),
		string(MethodTLSSetCert),
		string(MethodTLSRenameCert),
		string(MethodTLSDeleteCert),
		string(MethodTLSSetPolicy),
		string(MethodTLSRenamePolicy),
		string(MethodTLSDeletePolicy),
	}
}

func IsTLSMutation(method string) bool {
	switch TLSMethod(method) {
	case MethodTLSSetBuiltinCA,
		MethodTLSReloadOSCerts,
		MethodTLSSetCert,
		MethodTLSRenameCert,
		MethodTLSDeleteCert,
		MethodTLSSetPolicy,
		MethodTLSRenamePolicy,
		MethodTLSDeletePolicy:
		return true
	default:
		return false
	}
}

type TLSSource string

const (
	TLSSourceMutable   TLSSource = "mutable"
	TLSSourceProtected TLSSource = "protected"
	TLSSourceOS        TLSSource = "os"
	TLSSourceBuiltin   TLSSource = "builtin"
)

// TLSCertEntry is one effective certificate authority visible to admin clients.
// When a read-only certificate and a mutable certificate use the same name, the
// read-only certificate is reported here.
type TLSCertEntry struct {
	Name            string            `json:"name"`
	PEM             string            `json:"pem,omitempty"`
	Fingerprint     string            `json:"fingerprint,omitempty"`
	SPKIFingerprint string            `json:"spki_fingerprint,omitempty"`
	Subject         string            `json:"subject,omitempty"`
	Issuer          string            `json:"issuer,omitempty"`
	SerialNumber    string            `json:"serial_number,omitempty"`
	NotBefore       string            `json:"not_before,omitempty"`
	NotAfter        string            `json:"not_after,omitempty"`
	IsCA            bool              `json:"is_ca,omitempty"`
	ReadOnly        bool              `json:"read_only,omitempty"`
	Source          TLSSource         `json:"source,omitempty"`
	CertcatMatches  []TLSCertcatMatch `json:"certcat_matches,omitempty"`
}

type TLSBuiltinCAOptions struct {
	Issuer       string `json:"issuer"`
	ValidityDays int    `json:"validity_days"`
	KeyAlgorithm string `json:"key_algorithm"`
	Curve        string `json:"curve,omitempty"`
	KeyBits      int    `json:"key_bits,omitempty"`
}

type TLSBuiltinCAInfo struct {
	Cert                   TLSCertEntry        `json:"cert"`
	Options                TLSBuiltinCAOptions `json:"options"`
	Immutable              bool                `json:"immutable,omitempty"`
	RegenerationDisabled   bool                `json:"regeneration_disabled,omitempty"`
	RegenerationDisabledBy string              `json:"regeneration_disabled_by,omitempty"`
}

type TLSDNSRule struct {
	Domain            string `json:"domain"`
	IncludeSubdomains bool   `json:"include_subdomains,omitempty"`
}

// TLSScope selects the server identities for one trust rule. IPPrefixes are
// CIDR strings. Any cannot be combined with DNS or IPPrefixes.
type TLSScope struct {
	Any        bool         `json:"any,omitempty"`
	DNS        []TLSDNSRule `json:"dns,omitempty"`
	IPPrefixes []string     `json:"ip_prefixes,omitempty"`
}

type TLSCertcatScope struct {
	Basis                     string   `json:"basis,omitempty"`
	PermittedDNSSubtrees      []string `json:"permitted_dns_subtrees,omitempty"`
	PermittedIPCIDRs          []string `json:"permitted_ip_cidrs,omitempty"`
	RequiresExplicitAllowlist bool     `json:"requires_explicit_allowlist,omitempty"`
	RejectIPTargets           bool     `json:"reject_ip_targets,omitempty"`
}

type TLSCertcatPlatformCutoff struct {
	SCTTimestampAfter string `json:"sct_timestamp_after,omitempty"`
	EnforcementStart  string `json:"enforcement_start,omitempty"`
	FirstMajorVersion uint16 `json:"first_major_version,omitempty"`
}

type TLSCertcatMatch struct {
	Fingerprint    string                    `json:"fingerprint,omitempty"`
	MatchKind      string                    `json:"match_kind,omitempty"`
	Kind           string                    `json:"kind,omitempty"`
	HazardClass    string                    `json:"hazard_class,omitempty"`
	Action         string                    `json:"action,omitempty"`
	Severity       string                    `json:"severity,omitempty"`
	Recommendation string                    `json:"recommendation,omitempty"`
	Scope          *TLSCertcatScope          `json:"scope,omitempty"`
	PlatformCutoff *TLSCertcatPlatformCutoff `json:"platform_cutoff,omitempty"`
}

type TLSTrustAnchor struct {
	Cert  string   `json:"cert"`
	Scope TLSScope `json:"scope"`
}

type TLSAuthorityConstraint struct {
	Cert  string   `json:"cert"`
	Match string   `json:"match,omitempty"`
	Scope TLSScope `json:"scope"`
}

type TLSPin struct {
	Kind        string   `json:"kind"`
	Fingerprint string   `json:"fingerprint"`
	Scope       TLSScope `json:"scope"`
}

// TLSPolicySpec stores the named-certificate references that are compiled to a
// gonnect/tlspolicy ServerPolicy on demand.
type TLSPolicySpec struct {
	TrustAnchors []TLSTrustAnchor         `json:"trust_anchors,omitempty"`
	Constraints  []TLSAuthorityConstraint `json:"constraints,omitempty"`
	Pins         []TLSPin                 `json:"pins,omitempty"`
}

type TLSPolicyEntry struct {
	Name     string        `json:"name"`
	Spec     TLSPolicySpec `json:"spec"`
	ReadOnly bool          `json:"read_only,omitempty"`
	Source   TLSSource     `json:"source,omitempty"`
	Valid    bool          `json:"valid"`
	Error    string        `json:"error,omitempty"`
	Watchers []string      `json:"watchers,omitempty"`
}

type TLSSnapshot struct {
	BuiltinCA *TLSBuiltinCAInfo `json:"builtin_ca,omitempty"`
	Certs     []TLSCertEntry    `json:"certs,omitempty"`
	Policies  []TLSPolicyEntry  `json:"policies,omitempty"`
}

type TLSSubscribeResponse struct {
	SubscriptionID uint64      `json:"subscription_id"`
	Snapshot       TLSSnapshot `json:"snapshot"`
}

type TLSChangeKind string

const (
	TLSChangeBuiltinCA TLSChangeKind = "builtin_ca"
	TLSChangeCert      TLSChangeKind = "cert"
	TLSChangePolicy    TLSChangeKind = "policy"
)

type TLSChange struct {
	Kind      TLSChangeKind     `json:"kind"`
	Name      string            `json:"name,omitempty"`
	Deleted   bool              `json:"deleted,omitempty"`
	BuiltinCA *TLSBuiltinCAInfo `json:"builtin_ca,omitempty"`
	Cert      *TLSCertEntry     `json:"cert,omitempty"`
	Policy    *TLSPolicyEntry   `json:"policy,omitempty"`
}

type TLSEvent struct {
	SubscriptionID uint64    `json:"subscription_id"`
	Change         TLSChange `json:"change"`
}

type TLSNameRequest struct {
	Name string `json:"name"`
}

type TLSSetCertRequest struct {
	Name string `json:"name"`
	PEM  string `json:"pem"`
}

type TLSSetBuiltinCARequest struct {
	Options TLSBuiltinCAOptions `json:"options"`
}

type TLSRenameRequest struct {
	Name    string `json:"name"`
	NewName string `json:"new_name"`
}

type TLSSetPolicyRequest struct {
	Name string        `json:"name"`
	Spec TLSPolicySpec `json:"spec"`
}

type TLSSubscriptionRequest struct {
	SubscriptionID uint64 `json:"subscription_id"`
}
