package network

import (
	"context"
	"fmt"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
)

const tlsPlatformChunkBytes = transport.ConfigChunkPayload

// SetTLSPlatformProvider installs the fixed broker-owned TLS provider before
// Serve starts. It is not a runtime-selectable backend.
func (b *Broker) SetTLSPlatformProvider(provider tlsplatform.Provider) error {
	if provider == nil {
		return protocol.ErrInvalidContract
	}
	b.tlsMu.Lock()
	defer b.tlsMu.Unlock()
	if b.tlsProvider != nil || b.tlsTransfer != nil {
		return protocol.ErrInvalidContract
	}
	b.tlsProvider = provider
	return nil
}

// SetTLSRevocationChecker installs the fixed cache-only checker before Serve.
// A nil checker means that the protected policy did not enable this host effect.
func (b *Broker) SetTLSRevocationChecker(checker tlsplatform.RevocationChecker) error {
	if checker == nil {
		return protocol.ErrInvalidContract
	}
	b.tlsMu.Lock()
	defer b.tlsMu.Unlock()
	if b.tlsRevocation != nil {
		return protocol.ErrInvalidContract
	}
	b.tlsRevocation = checker
	return nil
}

func (b *Broker) TLSRevocationCheck(ctx context.Context, req protocol.TLSRevocationCheckRequest) (protocol.TLSRevocationCheckResult, error) {
	return read(ctx, b, req.Meta, protocol.OperationTLSRevocationCheck, func(ctx context.Context) (protocol.TLSRevocationCheckResult, error) {
		request := tlsplatform.RevocationRequest{Chains: req.Chains}
		if err := request.Validate(); err != nil {
			return protocol.TLSRevocationCheckResult{}, protocol.ErrInvalidContract
		}
		b.tlsMu.Lock()
		checker := b.tlsRevocation
		b.tlsMu.Unlock()
		if checker == nil {
			return protocol.TLSRevocationCheckResult{}, protocol.ErrCapabilityUnavailable
		}
		result, err := checker.CheckRevocation(ctx, request)
		if err != nil {
			return protocol.TLSRevocationCheckResult{}, err
		}
		if err := result.Validate(request); err != nil {
			return protocol.TLSRevocationCheckResult{}, protocol.ErrInvalidContract
		}
		states := make([]string, len(result.States))
		for i, state := range result.States {
			states[i] = string(state)
		}
		return protocol.TLSRevocationCheckResult{States: states}, nil
	})
}

func (b *Broker) TLSPlatformRefresh(ctx context.Context, req protocol.TLSPlatformRefreshRequest) (protocol.TLSPlatformRefreshResult, error) {
	return read(ctx, b, req.Meta, protocol.OperationTLSPlatformRefresh, func(ctx context.Context) (protocol.TLSPlatformRefreshResult, error) {
		request := tlsplatform.Request{Source: tlsplatform.Source(req.Source), IncludeCRLs: req.IncludeCRLs,
			IncludeDistrust: req.IncludeDistrust, MaxBytes: req.MaxBytes, MaxItems: req.MaxItems}
		if err := request.Validate(); err != nil {
			return protocol.TLSPlatformRefreshResult{}, err
		}
		b.tlsMu.Lock()
		defer b.tlsMu.Unlock()
		if b.tlsProvider == nil {
			return protocol.TLSPlatformRefreshResult{}, protocol.ErrCapabilityUnavailable
		}
		if req.Transfer == 0 {
			if req.Offset != 0 {
				return protocol.TLSPlatformRefreshResult{}, protocol.ErrInvalidContract
			}
			// A new transfer replaces an abandoned one. This lets R retry after a
			// timeout without retaining an unbounded or permanently wedged state.
			b.tlsTransfer = nil
			snapshot, err := b.tlsProvider.Snapshot(ctx, request)
			if err != nil {
				return protocol.TLSPlatformRefreshResult{}, err
			}
			data, err := tlsplatform.Marshal(snapshot, request)
			if err != nil {
				return protocol.TLSPlatformRefreshResult{}, err
			}
			b.tlsNext++
			if b.tlsNext == 0 {
				b.tlsNext++
			}
			b.tlsTransfer = &tlsPlatformTransfer{id: b.tlsNext, request: request, data: data}
		} else if b.tlsTransfer == nil || b.tlsTransfer.id != req.Transfer || b.tlsTransfer.request != request {
			b.tlsTransfer = nil
			return protocol.TLSPlatformRefreshResult{}, protocol.ErrInvalidContract
		}
		transfer := b.tlsTransfer
		if uint64(req.Offset) > uint64(len(transfer.data)) {
			b.tlsTransfer = nil
			return protocol.TLSPlatformRefreshResult{}, protocol.ErrInvalidContract
		}
		end := min(len(transfer.data), int(req.Offset)+tlsPlatformChunkBytes)
		if int(req.Offset) != transfer.offset {
			b.tlsTransfer = nil
			return protocol.TLSPlatformRefreshResult{}, fmt.Errorf("%w: TLS platform chunk offset", protocol.ErrInvalidContract)
		}
		result := protocol.TLSPlatformRefreshResult{Transfer: transfer.id, Offset: req.Offset,
			Total: uint32(len(transfer.data)), Final: end == len(transfer.data), Data: append([]byte(nil), transfer.data[req.Offset:end]...)}
		transfer.offset = end
		if result.Final {
			b.tlsTransfer = nil
		}
		return result, nil
	})
}

type tlsPlatformTransfer struct {
	id      uint64
	offset  int
	request tlsplatform.Request
	data    []byte
}
