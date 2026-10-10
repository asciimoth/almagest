package roleentry

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const bootstrapTimeout = 5 * time.Second

// Bootstrap is fixed by trusted launch code. Target is a single file name in
// C's already selected private directory; it is never an absolute path.
type Bootstrap struct {
	Version uint32           `json:"version"`
	Role    protocol.Role    `json:"role"`
	Session protocol.Session `json:"session"`
	Target  string           `json:"target,omitempty"`
}

type Ready struct {
	Version             uint32        `json:"version"`
	Role                protocol.Role `json:"role"`
	PolicyDigest        [32]byte      `json:"policy_digest"`
	RestrictionsApplied bool          `json:"restrictions_applied"`
}

func (b Bootstrap) Validate(expected protocol.Role) error {
	if b.Version != protocol.Version || b.Role != expected || b.Session.Version != protocol.Version || b.Session.Validate() != nil {
		return protocol.ErrInvalidContract
	}
	if expected == protocol.RoleConfigWriter {
		if !validTarget(b.Target) {
			return protocol.ErrInvalidContract
		}
	} else if b.Target != "" {
		return protocol.ErrInvalidContract
	}
	return nil
}

func validTarget(name string) bool {
	return name != "" && len(name) <= 128 && utf8.ValidString(name) && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\\:\x00\r\n") && strings.TrimSpace(name) == name
}

// ReceiveBootstrap reads the one trusted launch record after platform
// restrictions are installed and before application construction.
func ReceiveBootstrap(ctx context.Context, stream transport.Stream, expected protocol.Role) (Bootstrap, error) {
	if stream == nil {
		return Bootstrap{}, ErrStandaloneRole
	}
	deadline, cancel := boundedDeadline(ctx)
	defer cancel()
	var value Bootstrap
	if err := transport.ReadFrameDeadline(stream, &value, deadline); err != nil {
		return Bootstrap{}, err
	}
	if err := value.Validate(expected); err != nil {
		return Bootstrap{}, err
	}
	return value, nil
}

// SendBootstrap sends a trusted record and waits for proof that the child has
// applied its final restrictions. It does not negotiate the later RPC peer.
func SendBootstrap(ctx context.Context, stream transport.Stream, value Bootstrap) (Ready, error) {
	if stream == nil || value.Validate(value.Role) != nil {
		return Ready{}, protocol.ErrInvalidContract
	}
	deadline, cancel := boundedDeadline(ctx)
	defer cancel()
	if err := transport.WriteFrameDeadline(stream, value, deadline); err != nil {
		return Ready{}, err
	}
	var ready Ready
	if err := transport.ReadFrameDeadline(stream, &ready, deadline); err != nil {
		return Ready{}, err
	}
	if ready.Version != protocol.Version || ready.Role != value.Role || ready.PolicyDigest != value.Session.PolicyDigest || !ready.RestrictionsApplied {
		return Ready{}, fmt.Errorf("%w: invalid role readiness version=%d role=%q restrictions=%t", protocol.ErrInvalidContract,
			ready.Version, ready.Role, ready.RestrictionsApplied)
	}
	return ready, nil
}

// AcknowledgeBootstrap publishes readiness only after the role has constructed
// its fixed service successfully.
func AcknowledgeBootstrap(ctx context.Context, stream transport.Stream, value Bootstrap) error {
	if stream == nil || value.Validate(value.Role) != nil {
		return protocol.ErrInvalidContract
	}
	deadline, cancel := boundedDeadline(ctx)
	defer cancel()
	return transport.WriteFrameDeadline(stream, Ready{Version: protocol.Version, Role: value.Role,
		PolicyDigest: value.Session.PolicyDigest, RestrictionsApplied: true}, deadline)
}

func boundedDeadline(ctx context.Context) (time.Time, context.CancelFunc) {
	bounded, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	deadline, ok := bounded.Deadline()
	if !ok {
		cancel()
		return time.Time{}, func() {}
	}
	return deadline, cancel
}
