package protocol

import "time"

type Severity string

const (
	SeverityDebug Severity = "debug"
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// DiagnosticRecord is bounded diagnostic data, never an acknowledgement or
// instruction. The receiving trusted channel assigns Role and ProcessInstance;
// it does not accept those identities from the record producer.
type DiagnosticRecord struct {
	Timestamp          time.Time
	Role               Role
	LaunchID           string
	ProcessInstance    string
	Sequence           uint64
	Severity           Severity
	Source             string
	Message            string
	SessionID          SessionID
	ResourceID         ResourceID
	ResourceGeneration uint64
	OperationID        string
	Operation          Operation
	Decision           string
	Outcome            Outcome
}
