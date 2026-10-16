// Package sandbox defines capabilities for daemon-owned child programs.
// Implementations belong to the host. A missing service must return
// ErrUnavailable; it must never fall back to host execution.
//
// # Ownership
//
// The engine supplies durable owner identities. Nodes receive OwnerSandboxes,
// not Service, and cannot choose an owner key or change protected launch policy.
// A rename changes Name and NameRevision only. Resource ownership is checked by
// workspace, key, epoch, and instance generation. The engine must fence leases
// when it reconstructs a node, without deleting that node's live instances.
// BindOwner does not authorize an identity imported from graph metadata by
// itself: the host must check its durable registry.
//
// Acquire is idempotent for one owner, slot, and template. A different template
// requires Remove first. A failed instance cannot satisfy Acquire. Close and
// Remove stop all resources in the instance, remove temporary storage after
// verified cleanup, and preserve persistent storage. Owner deletion revokes
// every slot. An unproved cleanup must block reuse of the persistent state.
//
// # Commands
//
// Start is single-use. It returns after setup and exec succeed, not after the
// application is ready. Its context controls the command's complete lifetime.
// Adapters for programs with separate startup timeouts must pass their lifetime
// context to Start. Cancellation and Kill stop the entire command tree but do
// not destroy the reusable sandbox. Wait has a stable, repeatable result and
// returns an ExitError for a nonzero exit. Signal accepts only the portable
// Signal constants. Unsupported soft signals return ErrUnsupported.
//
// Each standard pipe can be requested once, before Start. Unrequested stdin is
// closed; unrequested output is discarded or sent to a bounded host log sink.
// Requested output has bounded buffers and normal backpressure. Callers must
// drain both output streams. Wait and cleanup must bound pipe shutdown even if
// a descendant holds a pipe open. Sandbox close revokes commands, streams,
// pending calls, and sockets. Backend cleanup must have a finite deadline and
// report an error when it cannot prove termination.
//
// # Files and sockets
//
// Path names are portable relative names, not host paths. ValidatePath is only
// a syntax check. File operations must also use held directory identities and
// reject symlink, reparse-point, and hard-link aliases, including under races.
// Root paths can be read, but cannot be removed or renamed. Metadata must not
// expose native handles or ownership data; FileInfo.Sys returns nil. The host
// must bound storage, transfers, directory pages, streams, and open files.
// ChildPath and ProgramPath only describe the child's view. They do not grant
// a node host filesystem authority.
//
// DialContext and Listen support numeric loopback TCP endpoints. ListenPacket
// supports loopback UDP only when the template and backend permit it. Every
// packet destination must be checked, including WriteTo destinations. Unix
// sockets use typed Path values and require confined, unprivileged resolution.
// Operations must use the selected sandbox network context. A backend cannot
// report private networking and then connect to host loopback. Returned network
// objects must not expose native handles, and should preserve deadlines and
// half-close where the backend supports them.
//
// Status is a copy of the host's fixed plan and current lifecycle state. It must
// contain no protected paths or secrets. Native errors must be bounded and
// sanitized. A failed selected restriction or uncertain mutation is an error
// in every mode; no implementation may retry with a weaker launch plan.
package sandbox
