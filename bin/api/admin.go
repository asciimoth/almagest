package api

type AdminMethod string

const (
	MethodAdminHello           AdminMethod = "hello"
	MethodAdminPing            AdminMethod = "ping"
	MethodAdminColorMap        AdminMethod = "colormap"
	MethodAdminAcquireMutation AdminMethod = "acquire_mutation"
	MethodAdminRenewMutation   AdminMethod = "renew_mutation"
	MethodAdminReleaseMutation AdminMethod = "release_mutation"

	EventAdminProjectVersionStatus AdminMethod = "project_version_status"
)

func AdminMethods() []string {
	return []string{
		string(MethodAdminHello),
		string(MethodAdminPing),
		string(MethodAdminColorMap),
		string(MethodAdminAcquireMutation),
		string(MethodAdminRenewMutation),
		string(MethodAdminReleaseMutation),
	}
}

type HelloRequest struct {
	ClientName string   `json:"client_name,omitempty"`
	Version    string   `json:"version,omitempty"`
	Surfaces   []string `json:"surfaces,omitempty"`
}

type HelloResponse struct {
	Version              string               `json:"version"`
	Daemon               string               `json:"daemon,omitempty"`
	ExecutionMode        string               `json:"execution_mode"`
	ProjectVersionStatus ProjectVersionStatus `json:"project_version_status"`
	Surfaces             map[string]any       `json:"surfaces,omitempty"`
	Isolation            *IsolationStatus     `json:"isolation,omitempty"`
}

// IsolationStatus exposes only non-secret, effective process guarantees. An
// isolated listener is published after all listed roles and recovery are ready.
type IsolationStatus struct {
	ProtocolVersion     uint32                                   `json:"protocol_version"`
	StatusSchemaVersion uint32                                   `json:"status_schema_version,omitempty"`
	Restrictions        map[string][]string                      `json:"restrictions"`
	Roles               map[string]string                        `json:"roles"`
	Capabilities        map[string]bool                          `json:"capabilities"`
	RecoveryState       string                                   `json:"recovery_state"`
	RequestedPolicy     string                                   `json:"requested_policy,omitempty"`
	ProfileID           string                                   `json:"profile_id,omitempty"`
	ProfileRevision     uint32                                   `json:"profile_revision,omitempty"`
	VerificationState   string                                   `json:"verification_state,omitempty"`
	Reduced             bool                                     `json:"reduced"`
	Mechanisms          map[string]map[string]IsolationMechanism `json:"mechanisms"`
	Credentials         map[string]IsolationCredentials          `json:"credentials"`
	CapabilitySets      map[string]IsolationCapabilitySets       `json:"capability_sets"`
	ResourceLimits      map[string][]IsolationResourceLimit      `json:"resource_limits"`
	Warnings            []IsolationWarning                       `json:"warnings"`
	ObservationTime     string                                   `json:"observation_time,omitempty"`
	SelectedPlanDigest  string                                   `json:"selected_plan_digest,omitempty"`
}

type IsolationMechanism struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type IsolationCredentials struct {
	RealUID             uint32   `json:"real_uid"`
	EffectiveUID        uint32   `json:"effective_uid"`
	SavedUID            uint32   `json:"saved_uid"`
	FilesystemUID       uint32   `json:"filesystem_uid"`
	RealGID             uint32   `json:"real_gid"`
	EffectiveGID        uint32   `json:"effective_gid"`
	SavedGID            uint32   `json:"saved_gid"`
	FilesystemGID       uint32   `json:"filesystem_gid"`
	SupplementaryGroups []uint32 `json:"supplementary_groups"`
}

type IsolationCapabilitySets struct {
	Inheritable []string `json:"inheritable"`
	Permitted   []string `json:"permitted"`
	Effective   []string `json:"effective"`
	Bounding    []string `json:"bounding"`
	Ambient     []string `json:"ambient"`
}

type IsolationResourceLimit struct {
	Name        string `json:"name"`
	Requested   string `json:"requested"`
	Effective   string `json:"effective"`
	Unit        string `json:"unit"`
	Scope       string `json:"scope"`
	Source      string `json:"source"`
	Enforcement string `json:"enforcement"`
}

type IsolationWarning struct {
	Role    string `json:"role"`
	Code    string `json:"code"`
	Effect  string `json:"effect"`
	Feature string `json:"feature"`
}

type ProjectVersionHighlight string

const (
	ProjectVersionHighlightNone     ProjectVersionHighlight = "none"
	ProjectVersionHighlightWarn     ProjectVersionHighlight = "warn"
	ProjectVersionHighlightCritical ProjectVersionHighlight = "critical"
)

type ProjectVersionStatus struct {
	Version      string                  `json:"version"`
	Highlight    ProjectVersionHighlight `json:"highlight"`
	HoverMessage string                  `json:"hover_message,omitempty"`
}

type PingRequest struct {
	Nonce string `json:"nonce,omitempty"`
}

type PingResponse struct {
	Nonce string `json:"nonce,omitempty"`
}

// MutationLease is advisory. Servers can allow mutation requests without a
// lease, but clients that intend to edit graph or daemon state should acquire
// and renew one so other clients can present useful conflict warnings.
type MutationLease struct {
	ID        string `json:"id"`
	Holder    string `json:"holder,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

type AcquireMutationRequest struct {
	Holder          string `json:"holder,omitempty"`
	StealExpired    bool   `json:"steal_expired,omitempty"`
	TTLMilliseconds int64  `json:"ttl_ms,omitempty"`
}

type AcquireMutationResponse struct {
	Acquired bool           `json:"acquired"`
	Lease    *MutationLease `json:"lease,omitempty"`
	Active   *MutationLease `json:"active,omitempty"`
}

type RenewMutationRequest struct {
	LeaseID         string `json:"lease_id"`
	TTLMilliseconds int64  `json:"ttl_ms,omitempty"`
}

type ReleaseMutationRequest struct {
	LeaseID string `json:"lease_id"`
}
