// Package matcherexperiment defines the private checkpoint 12 matcher probe
// messages. It is not a production isolation protocol.
package matcherexperiment

import "github.com/asciimoth/gonnect/sockowner"

const MaxMessageBytes = 4096

type Flow struct {
	Name  string              `json:"name"`
	Tuple sockowner.FlowTuple `json:"tuple"`
}

type Request struct {
	Kind       string              `json:"kind"`
	Flow       sockowner.FlowTuple `json:"flow"`
	Iterations int                 `json:"iterations,omitempty"`
}

type Response struct {
	Flows      []Flow `json:"flows,omitempty"`
	Matched    bool   `json:"matched,omitempty"`
	DurationNS int64  `json:"duration_ns,omitempty"`
	Error      string `json:"error,omitempty"`
}
