package api

type FlagsMethod string

const (
	MethodFlagsSnapshot   FlagsMethod = "snapshot"
	MethodFlagsReplace    FlagsMethod = "replace"
	MethodFlagsMarkOpened FlagsMethod = "mark_opened"
	EventFlagsSnapshot    FlagsMethod = "snapshot"
)

func FlagsMethods() []string {
	return []string{string(MethodFlagsSnapshot), string(MethodFlagsReplace), string(MethodFlagsMarkOpened)}
}

// FlagsDocument is the complete content of flags.json.
type FlagsDocument struct {
	Flags           map[string]bool `json:"flags,omitempty"`
	DocsOpened      bool            `json:"docs_opened,omitempty"`
	SnapshotsOpened bool            `json:"snapshots_opened,omitempty"`
}

type FlagsMarkOpenedRequest struct {
	Page string `json:"page"`
}
