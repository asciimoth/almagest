package commandhelper

import (
	"context"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

// SendBootstrap sends the fixed H identity and protected command profiles,
// then waits until H has installed restrictions and validated every profile.
func SendBootstrap(ctx context.Context, stream transport.Stream, value roleentry.Bootstrap, config Config) (roleentry.Ready, error) {
	if ctx == nil || stream == nil || value.Role != protocol.RoleCommandHelper || value.Validate(protocol.RoleCommandHelper) != nil {
		return roleentry.Ready{}, protocol.ErrInvalidContract
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deadline, ok := bounded.Deadline()
	if !ok {
		return roleentry.Ready{}, protocol.ErrInvalidContract
	}
	if err := transport.WriteFrameDeadline(stream, value, deadline); err != nil {
		return roleentry.Ready{}, err
	}
	if err := transport.WriteFrameDeadline(stream, config, deadline); err != nil {
		return roleentry.Ready{}, err
	}
	var ready roleentry.Ready
	if err := transport.ReadFrameDeadline(stream, &ready, deadline); err != nil {
		return roleentry.Ready{}, err
	}
	if err := ready.Validate(value); err != nil {
		return roleentry.Ready{}, err
	}
	return ready, nil
}
