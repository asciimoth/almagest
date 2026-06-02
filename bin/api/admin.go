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
	ProjectVersionStatus ProjectVersionStatus `json:"project_version_status"`
	Surfaces             map[string]any       `json:"surfaces,omitempty"`
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
