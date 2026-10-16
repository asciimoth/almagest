package roleentry

import (
	"encoding/hex"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const (
	maxIsolationRoles          = 8
	maxIsolationMechanisms     = 32
	maxIsolationCapabilities   = 64
	maxIsolationGroups         = 64
	maxIsolationResourceLimits = 16
	maxIsolationWarnings       = 32
	maxIsolationEffectLength   = 256
)

func (s RuntimeIsolationStatus) present() bool {
	return s.ProtocolVersion != 0 || s.RecoveryState != "" || len(s.Roles) != 0 || len(s.Restrictions) != 0 || len(s.Capabilities) != 0 || s.adaptivePresent()
}

func (s RuntimeIsolationStatus) adaptivePresent() bool {
	return s.StatusSchemaVersion != 0 || s.RequestedPolicy != "" || s.ProfileID != "" || s.ProfileRevision != 0 || s.VerificationState != "" || s.Reduced ||
		len(s.Mechanisms) != 0 || len(s.Credentials) != 0 || len(s.CapabilitySets) != 0 || len(s.ResourceLimits) != 0 ||
		len(s.Warnings) != 0 || s.ObservationTime != "" || s.SelectedPlanDigest != ""
}

func (s RuntimeIsolationStatus) validateAdaptive() error {
	if !s.adaptivePresent() {
		return nil
	}
	if s.StatusSchemaVersion != 1 || !validRequestedPolicy(s.RequestedPolicy) || !validProfileID(s.ProfileID) || s.ProfileRevision == 0 ||
		!validVerificationState(s.VerificationState) || len(s.Mechanisms) == 0 || len(s.Mechanisms) > maxIsolationRoles ||
		len(s.Credentials) == 0 || len(s.Credentials) > maxIsolationRoles || len(s.CapabilitySets) == 0 || len(s.CapabilitySets) > maxIsolationRoles ||
		s.ResourceLimits == nil || len(s.ResourceLimits) > maxIsolationRoles || s.Warnings == nil || len(s.Warnings) > maxIsolationWarnings || !validObservationTime(s.ObservationTime) ||
		!validPlanDigest(s.SelectedPlanDigest) {
		return protocol.ErrInvalidContract
	}
	for role, mechanisms := range s.Mechanisms {
		if !validIsolationRole(role) || len(mechanisms) == 0 || len(mechanisms) > maxIsolationMechanisms {
			return protocol.ErrInvalidContract
		}
		for name, mechanism := range mechanisms {
			if !validMechanismName(name) || !validMechanismState(mechanism.State) || mechanism.Reason != "" && !validReasonCode(mechanism.Reason) {
				return protocol.ErrInvalidContract
			}
		}
		if _, ok := s.Credentials[role]; !ok {
			return protocol.ErrInvalidContract
		}
		if _, ok := s.CapabilitySets[role]; !ok {
			return protocol.ErrInvalidContract
		}
	}
	for role, state := range s.Roles {
		if state == "ready" && s.Mechanisms[role] == nil {
			return protocol.ErrInvalidContract
		}
		if state == "ready" {
			if _, ok := s.Credentials[role]; !ok {
				return protocol.ErrInvalidContract
			}
			if _, ok := s.CapabilitySets[role]; !ok {
				return protocol.ErrInvalidContract
			}
			if len(s.ResourceLimits[role]) == 0 {
				return protocol.ErrInvalidContract
			}
		}
	}
	for role, credentials := range s.Credentials {
		if !validIsolationRole(role) || s.Mechanisms[role] == nil || len(credentials.SupplementaryGroups) > maxIsolationGroups || hasDuplicateUint32(credentials.SupplementaryGroups) {
			return protocol.ErrInvalidContract
		}
	}
	for role, sets := range s.CapabilitySets {
		if !validIsolationRole(role) || s.Mechanisms[role] == nil || !validCapabilitySet(sets.Inheritable) || !validCapabilitySet(sets.Permitted) ||
			!validCapabilitySet(sets.Effective) || !validCapabilitySet(sets.Bounding) || !validCapabilitySet(sets.Ambient) {
			return protocol.ErrInvalidContract
		}
	}
	for role, limits := range s.ResourceLimits {
		if !validIsolationRole(role) || s.Mechanisms[role] == nil || len(limits) == 0 || len(limits) > maxIsolationResourceLimits {
			return protocol.ErrInvalidContract
		}
		seen := make(map[string]struct{}, len(limits))
		for _, limit := range limits {
			if !validResourceName(limit.Name) || !validResourceValue(limit.Requested) || !validResourceValue(limit.Effective) ||
				!validResourceUnit(limit.Unit) || !validResourceScope(limit.Scope) || !validResourceSource(limit.Source) || !validEnforcement(limit.Enforcement) {
				return protocol.ErrInvalidContract
			}
			if _, ok := seen[limit.Name]; ok {
				return protocol.ErrInvalidContract
			}
			seen[limit.Name] = struct{}{}
		}
	}
	for _, warning := range s.Warnings {
		if !validIsolationRole(warning.Role) || !validReasonCode(warning.Code) || !boundedUTF8(warning.Effect) || !boundedUTF8(warning.Feature) {
			return protocol.ErrInvalidContract
		}
	}
	return nil
}

func boundedUTF8(value string) bool {
	return value != "" && len(value) <= maxIsolationEffectLength && utf8.ValidString(value)
}

func validRequestedPolicy(value string) bool {
	return value == "strict" || value == "auto" || value == "off"
}
func validProfileID(value string) bool {
	return value == "linux-full-v1" || value == "linux-user-v1" || value == "windows-full-v1"
}
func validVerificationState(value string) bool {
	return value == "verified" || value == "degraded" || value == "failed" || value == "unknown"
}
func validIsolationRole(value string) bool {
	return value == "network" || value == "runtime" || value == "config_writer" || value == "command_helper" || value == "sandbox_supervisor"
}
func validMechanismState(value string) bool {
	return value == "applied" || value == "unavailable" || value == "not_requested" || value == "not_applicable" || value == "unknown"
}
func validMechanismName(value string) bool {
	switch value {
	case "landlock", "seccomp", "no-new-privileges", "descriptor-allowlist", "private-namespaces", "mount-namespace",
		"role-cgroups", "separate-host-identities", "capability-sets", "pidfd-supervision", "fixed-target", "fixed-executable",
		"trusted-broker", "held-inputs", "trusted-supervisor", "session-bound-rpc", "bounded-relay":
		return true
	default:
		return false
	}
}
func validReasonCode(value string) bool {
	switch value {
	case "profile-not-implemented", "not-delegated", "same-uid-profile", "inherited-lower", "unsupported", "not-required", "external-restriction", "verification-failed":
		return true
	default:
		return false
	}
}
func validCapability(value string) bool {
	switch value {
	case "CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL", "CAP_SETGID", "CAP_SETUID",
		"CAP_SETPCAP", "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_IPC_LOCK",
		"CAP_IPC_OWNER", "CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT", "CAP_SYS_PTRACE", "CAP_SYS_PACCT", "CAP_SYS_ADMIN",
		"CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE", "CAP_SYS_TIME", "CAP_SYS_TTY_CONFIG", "CAP_MKNOD", "CAP_LEASE",
		"CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP", "CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG", "CAP_WAKE_ALARM",
		"CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ", "CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE":
		return true
	default:
		return false
	}
}
func validCapabilitySet(values []string) bool {
	if len(values) > maxIsolationCapabilities {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validCapability(value) {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
func hasDuplicateUint32(values []uint32) bool {
	seen := make(map[uint32]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
func validResourceName(value string) bool {
	return value == "memory" || value == "memory-swap" || value == "tasks" || value == "cpu-quota" || value == "descriptors" || value == "file-size"
}
func validResourceValue(value string) bool {
	if value == "max" {
		return true
	}
	if value == "" || len(value) > 20 {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && strconv.FormatUint(parsed, 10) == value
}
func validResourceUnit(value string) bool {
	return value == "bytes" || value == "count" || value == "microseconds"
}
func validResourceScope(value string) bool {
	return value == "role" || value == "process" || value == "process-set" || value == "inherited"
}
func validResourceSource(value string) bool {
	return value == "configured" || value == "default" || value == "inherited" || value == "external"
}
func validEnforcement(value string) bool {
	return value == "applied" || value == "observed" || value == "unavailable" || value == "not_requested" || value == "not_applicable" || value == "unknown"
}
func validObservationTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
func validPlanDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == lowerASCII(value)
}
func lowerASCII(value string) string {
	result := []byte(value)
	for index, char := range result {
		if char >= 'A' && char <= 'F' {
			result[index] = char + ('a' - 'A')
		}
	}
	return string(result)
}
