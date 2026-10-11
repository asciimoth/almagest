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
}

type RuntimeAdminEndpoint struct {
	ID   uint16
	Kind string
}

// RuntimeInput identifies only fixed files inside R's private storage and its
// already-open service lanes. It contains no host path, executable, policy, or
// native resource selector.
type RuntimeInput struct {
	Version         uint32
	ConfigWritable  bool
	StartupSnapshot bool
	AdminEndpoints  []RuntimeAdminEndpoint
	Options         RuntimeOptions
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
	return nil
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
