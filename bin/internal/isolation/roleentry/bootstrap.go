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
const finalIsolationStatusTimeout = 20 * time.Second

// Bootstrap is fixed by trusted launch code. Target is a single file name in
// C's already selected private directory; it is never an absolute path.
type Bootstrap struct {
	Version     uint32           `json:"version"`
	Role        protocol.Role    `json:"role"`
	Session     protocol.Session `json:"session"`
	Target      string           `json:"target,omitempty"`
	ProfileID   string           `json:"profile_id,omitempty"`
	PlanDigest  string           `json:"plan_digest,omitempty"`
	ExpectedUID uint32           `json:"expected_uid,omitempty"`
	ExpectedGID uint32           `json:"expected_gid,omitempty"`
}

type AppliedIsolation struct {
	ProfileID        string                         `json:"profile_id"`
	PlanDigest       string                         `json:"plan_digest"`
	ProfileRevision  uint32                         `json:"profile_revision"`
	LauncherRevision uint32                         `json:"launcher_revision"`
	SeccompRevision  uint32                         `json:"seccomp_revision"`
	LandlockRevision uint32                         `json:"landlock_revision"`
	LandlockABI      uint32                         `json:"landlock_abi"`
	NoNewPrivileges  bool                           `json:"no_new_privileges"`
	SeccompMode      uint32                         `json:"seccomp_mode"`
	DenialsVerified  bool                           `json:"denials_verified"`
	Credentials      RuntimeIsolationCredentials    `json:"credentials"`
	CapabilitySets   RuntimeIsolationCapabilitySets `json:"capability_sets"`
}

type Ready struct {
	Version             uint32            `json:"version"`
	Role                protocol.Role     `json:"role"`
	PolicyDigest        [32]byte          `json:"policy_digest"`
	RestrictionsApplied bool              `json:"restrictions_applied"`
	Applied             *AppliedIsolation `json:"applied,omitempty"`
}

func (r Ready) Validate(value Bootstrap) error {
	if r.Version != protocol.Version || r.Role != value.Role || r.PolicyDigest != value.Session.PolicyDigest || !r.RestrictionsApplied {
		return fmt.Errorf("%w: invalid role readiness version=%d role=%q restrictions=%t", protocol.ErrInvalidContract,
			r.Version, r.Role, r.RestrictionsApplied)
	}
	if value.ProfileID != "" {
		if r.Applied == nil || r.Applied.validate(value) != nil {
			return fmt.Errorf("%w: invalid applied isolation record", protocol.ErrInvalidContract)
		}
	} else if r.Applied != nil {
		return protocol.ErrInvalidContract
	}
	return nil
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
	adaptive := b.ProfileID != "" || b.PlanDigest != "" || b.ExpectedUID != 0 || b.ExpectedGID != 0
	if adaptive && (!validProfileID(b.ProfileID) || !validPlanDigest(b.PlanDigest) || b.ExpectedUID == 0 || b.ExpectedGID == 0) {
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
	if err := ready.Validate(value); err != nil {
		return Ready{}, err
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
	ready := Ready{Version: protocol.Version, Role: value.Role, PolicyDigest: value.Session.PolicyDigest, RestrictionsApplied: true}
	if value.ProfileID != "" {
		applied, err := observeAppliedIsolation(value)
		if err != nil {
			return err
		}
		ready.Applied = &applied
	}
	return transport.WriteFrameDeadline(stream, ready, deadline)
}

func (a AppliedIsolation) validate(value Bootstrap) error {
	if a.ProfileID != value.ProfileID || a.PlanDigest != value.PlanDigest || a.ProfileRevision != 1 || a.LauncherRevision != 1 ||
		a.SeccompRevision != 1 || a.LandlockRevision != 1 || a.LandlockABI < 3 || !a.NoNewPrivileges || a.SeccompMode != 2 || !a.DenialsVerified ||
		a.Credentials.RealUID != value.ExpectedUID || a.Credentials.EffectiveUID != value.ExpectedUID || a.Credentials.SavedUID != value.ExpectedUID || a.Credentials.FilesystemUID != value.ExpectedUID ||
		a.Credentials.RealGID != value.ExpectedGID || a.Credentials.EffectiveGID != value.ExpectedGID || a.Credentials.SavedGID != value.ExpectedGID || a.Credentials.FilesystemGID != value.ExpectedGID ||
		!validCapabilitySet(a.CapabilitySets.Inheritable) || !validCapabilitySet(a.CapabilitySets.Permitted) || !validCapabilitySet(a.CapabilitySets.Effective) ||
		!validCapabilitySet(a.CapabilitySets.Bounding) || !validCapabilitySet(a.CapabilitySets.Ambient) {
		return protocol.ErrInvalidContract
	}
	if len(a.CapabilitySets.Inheritable) != 0 || len(a.CapabilitySets.Permitted) != 0 || len(a.CapabilitySets.Effective) != 0 || len(a.CapabilitySets.Ambient) != 0 {
		return protocol.ErrInvalidContract
	}
	if len(a.Credentials.SupplementaryGroups) > maxIsolationGroups || hasDuplicateUint32(a.Credentials.SupplementaryGroups) ||
		a.ProfileID == "linux-full-v1" && (len(a.Credentials.SupplementaryGroups) != 0 || len(a.CapabilitySets.Bounding) != 0) {
		return protocol.ErrInvalidContract
	}
	return nil
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
