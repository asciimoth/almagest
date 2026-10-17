package sandboxrole

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const (
	maxPolicyBytes = 8 * 1024 * 1024
	maxChunkBytes  = 32 * 1024
)

type policyHeader struct {
	Size        uint32             `json:"size"`
	Digest      [32]byte           `json:"digest"`
	Unavailable SandboxDiagnostics `json:"unavailable,omitempty"`
}

// SandboxDiagnostics carries trusted, runtime-only discovery failures beside
// protected policy. It lets the supervisor reject only the affected template
// or program when it is used.
type SandboxDiagnostics struct {
	Templates map[string]string `json:"templates,omitempty"`
	Programs  map[string]string `json:"programs,omitempty"`
}

type policyChunk struct {
	Sequence uint32 `json:"sequence"`
	Data     []byte `json:"data"`
}

type RuntimeBinding struct {
	PID uint32 `json:"pid"`
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

func SendPolicy(ctx context.Context, stream transport.Stream, bootstrap roleentry.Bootstrap, data []byte, unavailable SandboxDiagnostics) error {
	if ctx == nil || stream == nil || bootstrap.Validate(protocol.RoleSandboxSupervisor) != nil || len(data) == 0 || len(data) > maxPolicyBytes {
		return protocol.ErrInvalidContract
	}
	if !validDiagnostics(unavailable) {
		return protocol.ErrInvalidContract
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	deadline, _ := bounded.Deadline()
	if err := transport.WriteFrameDeadline(stream, bootstrap, deadline); err != nil {
		return err
	}
	if err := transport.WriteFrameDeadline(stream, policyHeader{Size: uint32(len(data)), Digest: sha256.Sum256(data), Unavailable: unavailable}, deadline); err != nil {
		return err
	}
	var sequence uint32
	for len(data) != 0 {
		sequence++
		chunk := data[:min(len(data), maxChunkBytes)]
		if err := transport.WriteFrameDeadline(stream, policyChunk{Sequence: sequence, Data: chunk}, deadline); err != nil {
			return err
		}
		data = data[len(chunk):]
	}
	return nil
}

func ReceivePolicy(ctx context.Context, stream transport.Stream) (roleentry.Bootstrap, []byte, SandboxDiagnostics, error) {
	bootstrap, err := roleentry.ReceiveBootstrap(ctx, stream, protocol.RoleSandboxSupervisor)
	if err != nil {
		return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	deadline, _ := bounded.Deadline()
	var header policyHeader
	if err := transport.ReadFrameDeadline(stream, &header, deadline); err != nil {
		return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, err
	}
	if header.Size == 0 || header.Size > maxPolicyBytes {
		return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, protocol.ErrLimitExceeded
	}
	if !validDiagnostics(header.Unavailable) {
		return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, protocol.ErrInvalidContract
	}
	data := make([]byte, 0, header.Size)
	var sequence uint32
	for len(data) < int(header.Size) {
		var chunk policyChunk
		if err := transport.ReadFrameDeadline(stream, &chunk, deadline); err != nil {
			return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, err
		}
		sequence++
		if chunk.Sequence != sequence || len(chunk.Data) == 0 || len(chunk.Data) > maxChunkBytes || len(data)+len(chunk.Data) > int(header.Size) {
			return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, protocol.ErrInvalidContract
		}
		data = append(data, chunk.Data...)
	}
	if sha256.Sum256(data) != header.Digest {
		return roleentry.Bootstrap{}, nil, SandboxDiagnostics{}, protocol.ErrInvalidContract
	}
	return bootstrap, data, header.Unavailable, nil
}

func validDiagnostics(value SandboxDiagnostics) bool {
	if len(value.Templates) > 1024 || len(value.Programs) > 1024 {
		return false
	}
	for name, reason := range value.Templates {
		if name == "" || len(name) > 128 || reason == "" || len(reason) > 4096 {
			return false
		}
	}
	for name, reason := range value.Programs {
		if name == "" || len(name) > 128 || reason == "" || len(reason) > 4096 {
			return false
		}
	}
	return true
}

func SendRuntimeBinding(ctx context.Context, stream transport.Stream, binding RuntimeBinding) error {
	if ctx == nil || stream == nil || binding.PID == 0 || binding.UID == ^uint32(0) || binding.GID == ^uint32(0) {
		return protocol.ErrInvalidContract
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	deadline, _ := bounded.Deadline()
	return transport.WriteFrameDeadline(stream, binding, deadline)
}

func ReceiveRuntimeBinding(ctx context.Context, stream transport.Stream) (RuntimeBinding, error) {
	if ctx == nil || stream == nil {
		return RuntimeBinding{}, protocol.ErrInvalidContract
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	deadline, _ := bounded.Deadline()
	var binding RuntimeBinding
	if err := transport.ReadFrameDeadline(stream, &binding, deadline); err != nil {
		return RuntimeBinding{}, err
	}
	if binding.PID == 0 || binding.UID == ^uint32(0) || binding.GID == ^uint32(0) {
		return RuntimeBinding{}, protocol.ErrInvalidContract
	}
	return binding, nil
}

func ReadReady(ctx context.Context, stream transport.Stream, bootstrap roleentry.Bootstrap) (roleentry.Ready, error) {
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	deadline, _ := bounded.Deadline()
	var ready roleentry.Ready
	if err := transport.ReadFrameDeadline(stream, &ready, deadline); err != nil {
		return roleentry.Ready{}, err
	}
	if err := ready.Validate(bootstrap); err != nil {
		return roleentry.Ready{}, err
	}
	return ready, nil
}
