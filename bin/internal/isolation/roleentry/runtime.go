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
	LogLevel             string
	LogWorkers           bool
	NoPool               bool
	NoDNSCacheExpiration bool
	DebugNodes           bool
	LogCacheDebug        *int
	LogCacheInfo         *int
	LogCacheWarn         *int
	LogCacheError        *int
	LogCacheFatal        *int
}

type RuntimeAdminEndpoint struct {
	ID   uint16
	Kind string
}

type RuntimeIsolationStatus struct {
	ProtocolVersion uint32
	Restrictions    map[string][]string
	Roles           map[string]string
	Capabilities    map[string]bool
	RecoveryState   string
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
	Options         RuntimeOptions
	Isolation       RuntimeIsolationStatus
}

func (i RuntimeInput) Validate() error {
	if i.Version != protocol.Version || len(i.AdminEndpoints) > 8 || len(i.Options.LogLevel) > 16 || !utf8.ValidString(i.Options.LogLevel) {
		return protocol.ErrInvalidContract
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
	statusPresent := i.Isolation.ProtocolVersion != 0 || i.Isolation.RecoveryState != "" || len(i.Isolation.Roles) != 0 || len(i.Isolation.Restrictions) != 0 || len(i.Isolation.Capabilities) != 0
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
