package protocol

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Valid reports whether o is a defined version 1 operation.
func (o Operation) Valid() bool {
	switch o {
	case OperationHandshake,
		OperationCapabilities,
		OperationStatus,
		OperationRuntimeReady,
		OperationTLSPlatformRefresh,
		OperationCheckTun,
		OperationCheckDefaultTun,
		OperationCheckRule,
		OperationCompleteRule,
		OperationReserveIP,
		OperationReserveSubnet,
		OperationReleaseAllocation,
		OperationCreateTun,
		OperationCreateDefaultTun,
		OperationSetTunMTU,
		OperationSetTunAddresses,
		OperationSetTunRoutes,
		OperationSetTunName,
		OperationGetTunState,
		OperationCreateDNSEndpoint,
		OperationConfigureDNS,
		OperationOpenSocket,
		OperationChannel,
		OperationListInterfaces,
		OperationResolverSnapshot,
		OperationCreateMatcher,
		OperationMatch,
		OperationCloseResource,
		OperationReleaseGraphEpoch,
		OperationReadConfig,
		OperationReplaceConfig,
		OperationGetOperationResult,
		OperationRunNodeCommand,
		OperationRunCallback:
		return true
	default:
		return false
	}
}

func (s Session) Validate() error {
	if !s.ID.Valid() || s.Epoch == 0 {
		return fmt.Errorf("%w: session identity or epoch is zero", ErrInvalidContract)
	}
	if s.Version != Version {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, s.Version, Version)
	}
	return nil
}

func (m RequestMeta) Validate(session Session, mutation bool) error {
	if err := session.Validate(); err != nil {
		return err
	}
	if m.RequestID == 0 {
		return fmt.Errorf("%w: request ID is zero", ErrInvalidContract)
	}
	if m.SessionID != session.ID || m.SessionEpoch != session.Epoch {
		return ErrForeignResource
	}
	if m.GraphEpoch == 0 {
		return fmt.Errorf("%w: graph epoch is zero", ErrInvalidContract)
	}
	if mutation && (m.OperationID == "" || len(m.OperationID) > MaxOperationIDBytes) {
		return fmt.Errorf("%w: mutation operation ID length", ErrInvalidContract)
	}
	if !mutation && m.OperationID != "" {
		return fmt.Errorf("%w: read-only request has an operation ID", ErrInvalidContract)
	}
	return nil
}

func (r ResourceRef) Validate(session Session, expectedType ResourceType, graphEpoch, generation uint64) error {
	if !r.ID.Valid() {
		return fmt.Errorf("%w: resource ID is zero", ErrInvalidContract)
	}
	if r.SessionID != session.ID {
		return ErrForeignResource
	}
	if r.Type != expectedType {
		return ErrResourceType
	}
	if r.GraphEpoch != graphEpoch {
		return ErrStaleGraphEpoch
	}
	if r.Generation != generation {
		return ErrStaleGeneration
	}
	return nil
}

// CanAttempt reports whether a caller can make a typed request. Unknown is not
// implicitly denied: AttemptAllowed carries the backend's attempt semantics.
func (c Capability) CanAttempt() bool {
	return c.AttemptAllowed && c.State != CapabilityUnsupported && c.State != CapabilityUnavailable
}

func (c Capability) Validate() error {
	switch c.State {
	case CapabilityUnknown, CapabilityUnsupported, CapabilityUnavailable, CapabilitySupported:
	default:
		return fmt.Errorf("%w: capability state %q", ErrInvalidContract, c.State)
	}
	if (c.State == CapabilityUnsupported || c.State == CapabilityUnavailable) && c.AttemptAllowed {
		return fmt.Errorf("%w: %s capability permits attempts", ErrInvalidContract, c.State)
	}
	return nil
}

func (r MutationResult) Validate() error {
	if r.OperationID == "" || len(r.OperationID) > MaxOperationIDBytes {
		return fmt.Errorf("%w: result operation ID length", ErrInvalidContract)
	}
	switch r.Outcome {
	case OutcomeRejected, OutcomeCommitted, OutcomeCompensated, OutcomeUncertain:
		return nil
	default:
		return fmt.Errorf("%w: result outcome %q", ErrInvalidContract, r.Outcome)
	}
}

func (r DiagnosticRecord) Validate() error {
	if r.Timestamp.IsZero() || r.Sequence == 0 || !r.SessionID.Valid() ||
		len(r.ProcessInstance) == 0 || len(r.ProcessInstance) > MaxDiagnosticSourceBytes ||
		len(r.LaunchID) == 0 || len(r.LaunchID) > MaxDiagnosticSourceBytes ||
		!diagnosticSafe(r.ProcessInstance) || !diagnosticSafe(r.LaunchID) {
		return fmt.Errorf("%w: diagnostic identity or sequence is empty", ErrInvalidContract)
	}
	switch r.Role {
	case RoleRuntime, RoleNetworkBroker, RoleConfigWriter, RoleCommandHelper, RoleSandboxSupervisor:
	default:
		return fmt.Errorf("%w: diagnostic role %q", ErrInvalidContract, r.Role)
	}
	switch r.Severity {
	case SeverityDebug, SeverityInfo, SeverityWarn, SeverityError, SeverityFatal:
	default:
		return fmt.Errorf("%w: diagnostic severity %q", ErrInvalidContract, r.Severity)
	}
	if len(r.Source) == 0 || len(r.Source) > MaxDiagnosticSourceBytes ||
		len(r.Message) > MaxDiagnosticMessageBytes || !diagnosticSafe(r.Source) || !diagnosticSafe(r.Message) {
		return fmt.Errorf("%w: diagnostic source or message limit", ErrInvalidContract)
	}
	if len(r.OperationID) > MaxOperationIDBytes || !diagnosticSafe(r.OperationID) ||
		len(r.Decision) > MaxDiagnosticSourceBytes || !diagnosticSafe(r.Decision) {
		return fmt.Errorf("%w: diagnostic operation ID length", ErrInvalidContract)
	}
	if r.ResourceID.Valid() != (r.ResourceGeneration != 0) || r.Operation != "" && !r.Operation.Valid() {
		return fmt.Errorf("%w: diagnostic correlation", ErrInvalidContract)
	}
	if r.Outcome != "" {
		switch r.Outcome {
		case OutcomeRejected, OutcomeCommitted, OutcomeCompensated, OutcomeUncertain:
		default:
			return fmt.Errorf("%w: diagnostic outcome", ErrInvalidContract)
		}
	}
	return nil
}

func diagnosticSafe(value string) bool {
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}
