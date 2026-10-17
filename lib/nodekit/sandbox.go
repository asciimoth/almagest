package nodekit

import (
	"context"

	sandbox "github.com/asciimoth/gobox"
)

// SandboxProvider exposes only the calling node's bound capabilities. The host
// creates this view after it authorizes durable owner identity and lifetime.
// It must not expose Service or let a node choose an owner key.
type SandboxProvider interface {
	Sandboxes(context.Context) (sandbox.OwnerSandboxes, error)
}

// SandboxRuntime is implemented by hosts that bind sandbox capabilities to a
// concrete engine node. Node code cannot supply identity fields.
type SandboxRuntime interface {
	SandboxesForNode(context.Context, uint64, string, string) (sandbox.OwnerSandboxes, error)
}

// UnavailableSandboxes is the default for hosts without sandbox support.
// There is deliberately no fallback to an ordinary host process.
type UnavailableSandboxes struct{}

func (UnavailableSandboxes) Sandboxes(context.Context) (sandbox.OwnerSandboxes, error) {
	return nil, sandbox.ErrUnavailable
}

// Sandboxes returns this node's owner-bound sandbox view.
func (b *Base) Sandboxes(ctx context.Context) (sandbox.OwnerSandboxes, error) {
	if b == nil || b.Runtime == nil || b.ID == 0 {
		return nil, sandbox.ErrUnavailable
	}
	runtime, ok := b.Runtime.(SandboxRuntime)
	if !ok {
		return nil, sandbox.ErrUnavailable
	}
	if b.Class == "" || b.Name == "" {
		return nil, sandbox.ErrUnavailable
	}
	return runtime.SandboxesForNode(ctx, b.ID, b.Class, b.Name)
}
