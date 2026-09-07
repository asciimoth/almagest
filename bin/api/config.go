package api

import almagest "github.com/asciimoth/almagest/lib"

// ConfigMethod identifies an operation on the editable main HuJSON config.
// Protected daemon config is a separate store and is never part of this
// surface.
type ConfigMethod string

const (
	MethodConfigSnapshot ConfigMethod = "snapshot"
	MethodConfigUpdate   ConfigMethod = "update"
)

// ConfigMethods returns all methods implemented by the config surface.
func ConfigMethods() []string {
	return []string{
		string(MethodConfigSnapshot),
		string(MethodConfigUpdate),
	}
}

// IsConfigMutation reports whether a config method changes daemon state.
func IsConfigMutation(method string) bool {
	return ConfigMethod(method) == MethodConfigUpdate
}

// ConfigSnapshot is the complete editable HuJSON document. Revision is an
// opaque content token for optimistic concurrency control.
type ConfigSnapshot struct {
	Text     string `json:"text"`
	Revision string `json:"revision"`
	ReadOnly bool   `json:"read_only"`
}

// ConfigUpdateRequest replaces the editable HuJSON document. Revision should
// be copied from ConfigSnapshot. The server rejects a stale non-empty revision.
type ConfigUpdateRequest struct {
	Text     string `json:"text"`
	Revision string `json:"revision,omitempty"`
}

// ConfigUpdateResponse returns the canonical saved document and the partial
// live graph operations that were required to apply it.
type ConfigUpdateResponse struct {
	Snapshot ConfigSnapshot                 `json:"snapshot"`
	Changes  almagest.ConfigReconcileResult `json:"changes"`
}
