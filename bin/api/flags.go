package api

type FlagsMethod string

const (
	MethodFlagsSnapshot FlagsMethod = "snapshot"
	MethodFlagsReplace  FlagsMethod = "replace"
	EventFlagsSnapshot  FlagsMethod = "snapshot"
)

func FlagsMethods() []string {
	return []string{string(MethodFlagsSnapshot), string(MethodFlagsReplace)}
}

// FlagsDocument is the complete content of flags.json.
type FlagsDocument struct {
	Flags map[string]bool `json:"flags,omitempty"`
}
