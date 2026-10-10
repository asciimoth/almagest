// Package network implements the session-owned network broker. It is not part
// of the direct daemon composition. Native objects never cross its boundary.
package network

import (
	"context"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

// OwnedDNSEndpoint is a bootstrap-created, bounded relay to R. Resolver is
// consumed only by the selected native backend. It does not expose a host DNS
// provider, file, command, D-Bus connection, WFP object, or driver handle.
// Application resolution stays in R. Native backend DNS parsing may run in N;
// this relay does not grant R general host DNS control.
type OwnedDNSEndpoint interface {
	OwnedTun
	Resolver() gdns.Interface
}

// DNSBackend applies the selected backend's managed host-DNS operation to one
// owned default TUN. Provider selection and native arguments remain in N.
type DNSBackend interface {
	ConfigureDNS(context.Context, OwnedTun, OwnedDNSEndpoint, bool) error
}

// Backend belongs exclusively to one Broker. Check is read-only and Create
// must reject name collisions, never open an existing device by name. On a
// partial failure Create returns its owned object as well as the error. If it
// cannot account for all effects it returns ErrRecoveryRequired (also when an
// object is returned). A nil object with an error is conservatively uncertain.
// Implementations must honor context deadlines. A stuck implementation holds
// the one native worker slot and cannot cause unbounded goroutine creation.
type Backend interface {
	Capabilities(context.Context) (protocol.CapabilitySnapshot, error)
	Check(context.Context, protocol.CreateTunRequest) (protocol.ValidationResult, error)
	Create(context.Context, protocol.CreateTunRequest) (OwnedTun, error)
}

// NoEffectsError is only for a native preflight rejection that proves no
// mutation was attempted. Ordinary native errors never imply this guarantee.
type NoEffectsError struct{ Err error }

func (e *NoEffectsError) Error() string { return e.Err.Error() }
func (e *NoEffectsError) Unwrap() error { return e.Err }

// OwnedTun retains a native identity, not a mutable interface name. Verify
// compares that identity with the live device before each operation. Update
// and Close must also enforce identity inside the native operation, and must
// not remove or overwrite external changes. Close succeeds only after native
// I/O and owned side effects have stopped. Identity mismatches and unproved
// teardown return ErrRecoveryRequired. These are backend obligations, not
// permissions granted by possession of a resource ID.
type OwnedTun interface {
	Verify(context.Context) error
	State(context.Context) (protocol.ResourceState, error)
	Capabilities(context.Context) (protocol.CapabilitySnapshot, error)
	Update(context.Context, protocol.UpdateTunRequest) error
	Close(context.Context) error
	Events() <-chan tun.Event
}
