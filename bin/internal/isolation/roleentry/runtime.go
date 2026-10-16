package roleentry

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const (
	RuntimeRegularConfigFile   = ".isolation-regular.hujson"
	RuntimeProtectedConfigFile = ".isolation-protected.hujson"
	RuntimeSnapshotFile        = ".isolation-startup.zip"
)

type RuntimeOptions struct {
	LogLevel                       string
	LogWorkers                     bool
	NoPool                         bool
	NoDNSCacheExpiration           bool
	DebugNodes                     bool
	E2ETailscaleRejectAfterSeconds uint32
	LogCacheDebug                  *int
	LogCacheInfo                   *int
	LogCacheWarn                   *int
	LogCacheError                  *int
	LogCacheFatal                  *int
}

type RuntimeAdminEndpoint struct {
	ID   uint16
	Kind string
}

type RuntimeIsolationStatus struct {
	ProtocolVersion     uint32
	StatusSchemaVersion uint32
	Restrictions        map[string][]string
	Roles               map[string]string
	Capabilities        map[string]bool
	RecoveryState       string
	RequestedPolicy     string
	ProfileID           string
	ProfileRevision     uint32
	VerificationState   string
	Reduced             bool
	Mechanisms          map[string]map[string]RuntimeIsolationMechanism
	Credentials         map[string]RuntimeIsolationCredentials
	CapabilitySets      map[string]RuntimeIsolationCapabilitySets
	ResourceLimits      map[string][]RuntimeIsolationResourceLimit
	Warnings            []RuntimeIsolationWarning
	ObservationTime     string
	SelectedPlanDigest  string
}

type RuntimeIsolationMechanism struct {
	State  string
	Reason string
}

type RuntimeIsolationCredentials struct {
	RealUID             uint32
	EffectiveUID        uint32
	SavedUID            uint32
	FilesystemUID       uint32
	RealGID             uint32
	EffectiveGID        uint32
	SavedGID            uint32
	FilesystemGID       uint32
	SupplementaryGroups []uint32
}

type RuntimeIsolationCapabilitySets struct {
	Inheritable []string
	Permitted   []string
	Effective   []string
	Bounding    []string
	Ambient     []string
}

type RuntimeIsolationResourceLimit struct {
	Name        string
	Requested   string
	Effective   string
	Unit        string
	Scope       string
	Source      string
	Enforcement string
}

type RuntimeIsolationWarning struct {
	Role    string
	Code    string
	Effect  string
	Feature string
}

// RuntimeNodeCommand is the public shape of one command that protected policy
// permits workspace nodes to select. Execution details stay in the trusted
// command helper.
type RuntimeNodeCommand struct {
	Name  string
	Slots []string
}

type RuntimePeer struct {
	PID                 int32
	UID                 uint32
	GID                 uint32
	OutsidePIDNamespace bool
}

// RuntimeInput identifies only fixed files inside R's private storage and its
// already-open service lanes. It contains no host path, executable, policy, or
// native resource selector.
type RuntimeInput struct {
	Version         uint32
	Production      bool
	ConfigWritable  bool
	StartupSnapshot bool
	AdminEndpoints  []RuntimeAdminEndpoint
	NodeCommands    []RuntimeNodeCommand
	Sandbox         bool
	SandboxPeer     RuntimePeer
	Options         RuntimeOptions
	Isolation       RuntimeIsolationStatus
}

func (i RuntimeInput) Validate() error {
	if i.Version != protocol.Version || len(i.AdminEndpoints) > 8 || len(i.NodeCommands) > 128 || len(i.Options.LogLevel) > 16 || !utf8.ValidString(i.Options.LogLevel) ||
		i.Options.E2ETailscaleRejectAfterSeconds > 86400 {
		return protocol.ErrInvalidContract
	}
	if i.Sandbox {
		if i.SandboxPeer.UID == ^uint32(0) || i.SandboxPeer.GID == ^uint32(0) ||
			i.SandboxPeer.PID < 0 || (i.SandboxPeer.PID == 0) != i.SandboxPeer.OutsidePIDNamespace {
			return protocol.ErrInvalidContract
		}
	} else if i.SandboxPeer != (RuntimePeer{}) {
		return protocol.ErrInvalidContract
	}
	commands := make(map[string]struct{}, len(i.NodeCommands))
	for _, command := range i.NodeCommands {
		if command.Name == "" || len(command.Name) > 128 || !utf8.ValidString(command.Name) || len(command.Slots) > 128 {
			return protocol.ErrInvalidContract
		}
		if _, exists := commands[command.Name]; exists {
			return protocol.ErrInvalidContract
		}
		commands[command.Name] = struct{}{}
		slots := make(map[string]struct{}, len(command.Slots))
		for _, slot := range command.Slots {
			if slot == "" || len(slot) > 64 || !utf8.ValidString(slot) {
				return protocol.ErrInvalidContract
			}
			if _, exists := slots[slot]; exists {
				return protocol.ErrInvalidContract
			}
			slots[slot] = struct{}{}
		}
	}
	seen := make(map[uint16]struct{}, len(i.AdminEndpoints))
	for _, endpoint := range i.AdminEndpoints {
		if endpoint.ID == 0 || endpoint.Kind != "web" && endpoint.Kind != "unix" && endpoint.Kind != "pipe" {
			return protocol.ErrInvalidContract
		}
		if _, exists := seen[endpoint.ID]; exists {
			return protocol.ErrInvalidContract
		}
		seen[endpoint.ID] = struct{}{}
	}
	statusPresent := i.Isolation.present()
	if i.Production && (i.Isolation.ProtocolVersion != protocol.Version || i.Isolation.RecoveryState != "complete" ||
		i.Isolation.Roles["network"] != "ready" || i.Isolation.Roles["runtime"] != "ready" ||
		len(i.Isolation.Restrictions["network"]) == 0 || len(i.Isolation.Restrictions["runtime"]) == 0) {
		return protocol.ErrInvalidContract
	}
	if statusPresent {
		if i.Isolation.ProtocolVersion != protocol.Version || len(i.Isolation.Roles) > 16 || len(i.Isolation.Restrictions) > 16 || len(i.Isolation.Capabilities) > 32 || !boundedStatusString(i.Isolation.RecoveryState) {
			return protocol.ErrInvalidContract
		}
		for role, state := range i.Isolation.Roles {
			if !boundedStatusString(role) || !boundedStatusString(state) {
				return protocol.ErrInvalidContract
			}
		}
		for role, restrictions := range i.Isolation.Restrictions {
			if !boundedStatusString(role) || len(restrictions) == 0 || len(restrictions) > 16 {
				return protocol.ErrInvalidContract
			}
			for _, restriction := range restrictions {
				if !boundedStatusString(restriction) {
					return protocol.ErrInvalidContract
				}
			}
		}
		for capability := range i.Isolation.Capabilities {
			if !boundedStatusString(capability) {
				return protocol.ErrInvalidContract
			}
		}
		if err := i.Isolation.validateAdaptive(); err != nil {
			return err
		}
	}
	return nil
}

func boundedStatusString(value string) bool {
	return value != "" && len(value) <= 64 && utf8.ValidString(value)
}

func ReceiveRuntimeBootstrap(ctx context.Context, stream transport.Stream) (Bootstrap, RuntimeInput, error) {
	bootstrap, err := ReceiveBootstrap(ctx, stream, protocol.RoleRuntime)
	if err != nil {
		return Bootstrap{}, RuntimeInput{}, err
	}
	deadline, cancel := boundedDeadline(ctx)
	defer cancel()
	var input RuntimeInput
	if err := transport.ReadFrameDeadline(stream, &input, deadline); err != nil {
		return Bootstrap{}, RuntimeInput{}, err
	}
	if err := input.Validate(); err != nil {
		return Bootstrap{}, RuntimeInput{}, err
	}
	return bootstrap, input, nil
}

func SendRuntimeBootstrap(ctx context.Context, stream transport.Stream, value Bootstrap, input RuntimeInput) (Ready, error) {
	if ctx == nil || stream == nil || value.Role != protocol.RoleRuntime || value.Validate(protocol.RoleRuntime) != nil || input.Validate() != nil {
		return Ready{}, protocol.ErrInvalidContract
	}
	deadline, cancel := boundedDeadline(ctx)
	defer cancel()
	if err := transport.WriteFrameDeadline(stream, value, deadline); err != nil {
		return Ready{}, err
	}
	if err := transport.WriteFrameDeadline(stream, input, deadline); err != nil {
		return Ready{}, err
	}
	var ready Ready
	if err := transport.ReadFrameDeadline(stream, &ready, deadline); err != nil {
		return Ready{}, err
	}
	if err := ready.Validate(value); err != nil {
		return Ready{}, fmt.Errorf("runtime readiness: %w", err)
	}
	return ready, nil
}

// SendFinalIsolationStatus publishes the broker-validated applied profile
// before the runtime starts its application services.
func SendFinalIsolationStatus(ctx context.Context, stream transport.Stream, status RuntimeIsolationStatus) error {
	if ctx == nil || stream == nil || status.VerificationState != "verified" || status.validateAdaptive() != nil {
		return protocol.ErrInvalidContract
	}
	bounded, cancel := context.WithTimeout(ctx, finalIsolationStatusTimeout)
	defer cancel()
	deadline, _ := bounded.Deadline()
	return transport.WriteFrameDeadline(stream, status, deadline)
}

func ReceiveFinalIsolationStatus(ctx context.Context, stream transport.Stream) (RuntimeIsolationStatus, error) {
	if ctx == nil || stream == nil {
		return RuntimeIsolationStatus{}, protocol.ErrInvalidContract
	}
	bounded, cancel := context.WithTimeout(ctx, finalIsolationStatusTimeout)
	defer cancel()
	deadline, _ := bounded.Deadline()
	var status RuntimeIsolationStatus
	if err := transport.ReadFrameDeadline(stream, &status, deadline); err != nil {
		return RuntimeIsolationStatus{}, err
	}
	if status.VerificationState != "verified" || status.validateAdaptive() != nil {
		return RuntimeIsolationStatus{}, protocol.ErrInvalidContract
	}
	return status, nil
}
