package network

import (
	"context"
	"errors"
	"math"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// CreateDNSEndpoint creates a graph-owned relay on N. The request has no
// parameters other than its authority metadata, so R cannot select a host
// provider or native DNS mechanism.
func (b *Broker) CreateDNSEndpoint(ctx context.Context, req protocol.CreateDNSEndpointRequest) (protocol.ResourceResult, error) {
	input := req
	input.Meta.RequestID = 0
	return b.mutate(ctx, req.Meta, protocol.OperationCreateDNSEndpoint, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		b.mu.Lock()
		limited := len(b.resources) >= int(b.policy.Limits.MaxLiveResources)
		b.mu.Unlock()
		if limited {
			return rejected(req.Meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
		}
		if err := ctx.Err(); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		endpoint := NewApplicationDNSEndpoint()
		ref, err := b.registerDNSEndpoint(ctx, req.Meta.GraphEpoch, endpoint)
		if err != nil {
			_ = endpoint.Close(context.Background())
			return rejected(req.Meta.OperationID, err), err
		}
		b.mu.Lock()
		r := b.resources[ref.ID]
		b.mu.Unlock()
		if r == nil {
			_ = endpoint.Close(context.Background())
			return rejected(req.Meta.OperationID, protocol.ErrRecoveryRequired), protocol.ErrRecoveryRequired
		}
		return b.result(r, protocol.OutcomeCommitted), nil
	})
}

// RegisterDNSEndpoint records a bootstrap-created DNS relay. This is not a
// runtime RPC: isolated bootstrap creates the bounded relay and gives its
// opaque reference to R. Thus R cannot select a provider or native mechanism.
func (b *Broker) RegisterDNSEndpoint(ctx context.Context, graphEpoch uint64, endpoint OwnedDNSEndpoint) (protocol.ResourceRef, error) {
	return b.registerDNSEndpoint(ctx, graphEpoch, endpoint)
}

func (b *Broker) registerDNSEndpoint(ctx context.Context, graphEpoch uint64, endpoint OwnedDNSEndpoint) (protocol.ResourceRef, error) {
	if endpoint == nil || endpoint.Resolver() == nil || graphEpoch == 0 {
		return protocol.ResourceRef{}, protocol.ErrInvalidContract
	}
	if err := endpoint.Verify(ctx); err != nil {
		return protocol.ResourceRef{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || graphEpoch != b.epoch {
		return protocol.ResourceRef{}, protocol.ErrStaleGraphEpoch
	}
	if len(b.resources) >= int(b.policy.Limits.MaxLiveResources) {
		return protocol.ResourceRef{}, protocol.ErrLimitExceeded
	}
	id, err := protocol.NewResourceID()
	if err != nil {
		return protocol.ResourceRef{}, err
	}
	if b.resources[id] != nil {
		return protocol.ResourceRef{}, protocol.ErrInvalidContract
	}
	ref := protocol.ResourceRef{ID: id, SessionID: b.session.ID, Type: protocol.ResourceDNS, Generation: 1, GraphEpoch: graphEpoch}
	b.resources[id] = &resource{ref: ref, native: endpoint, state: protocol.ResourceState{Up: true, Revision: 1}}
	return ref, nil
}

func (b *Broker) validateDNSRequest(req protocol.ConfigureDNSRequest) error {
	if req.Tun.Type != protocol.ResourceDefaultTun {
		return protocol.ErrResourceType
	}
	if req.Enabled {
		if req.Endpoint.Type != protocol.ResourceDNS || !req.Endpoint.ID.Valid() {
			return protocol.ErrResourceType
		}
	} else if req.Endpoint != (protocol.ResourceRef{}) {
		return protocol.ErrInvalidContract
	}
	if p := b.policy.NetworkRestrictions; p != nil && p.AllowHostDNS != nil && !*p.AllowHostDNS {
		return protocol.ErrPolicyDenied
	}
	return nil
}

// ConfigureDNS attaches or detaches the session DNS relay from an owned
// default TUN. Native provider selection stays inside the backend.
func (b *Broker) ConfigureDNS(ctx context.Context, req protocol.ConfigureDNSRequest) (protocol.MutationResult, error) {
	input := req
	input.Meta.RequestID = 0
	result, err := b.mutate(ctx, req.Meta, protocol.OperationConfigureDNS, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		if err := b.validateDNSRequest(req); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		backend, ok := b.backend.(DNSBackend)
		if !ok {
			return rejected(req.Meta.OperationID, protocol.ErrCapabilityUnsupported), protocol.ErrCapabilityUnsupported
		}
		b.mu.Lock()
		tunResource, lookupErr := b.lookup(req.Tun)
		var endpointResource *resource
		if lookupErr == nil && (tunResource.state.Closed || tunResource.state.RecoveryRequired) {
			lookupErr = protocol.ErrCapabilityUnavailable
		}
		if lookupErr == nil && req.Enabled {
			endpointResource, lookupErr = b.lookup(req.Endpoint)
			if lookupErr == nil && (endpointResource.state.Closed || endpointResource.state.RecoveryRequired) {
				lookupErr = protocol.ErrCapabilityUnavailable
			}
		}
		b.mu.Unlock()
		if lookupErr != nil {
			return rejected(req.Meta.OperationID, lookupErr), lookupErr
		}
		var endpoint OwnedDNSEndpoint
		if req.Enabled {
			endpoint, ok = endpointResource.native.(OwnedDNSEndpoint)
			if !ok || endpoint.Resolver() == nil {
				return rejected(req.Meta.OperationID, protocol.ErrResourceType), protocol.ErrResourceType
			}
		}
		if req.Enabled && tunResource.dns == req.Endpoint || !req.Enabled && tunResource.dns == (protocol.ResourceRef{}) {
			return b.result(tunResource, protocol.OutcomeCommitted), nil
		}
		if tunResource.ref.Generation == math.MaxUint64 {
			return rejected(req.Meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
		}
		if err := tunResource.native.Verify(ctx); err != nil {
			return b.failed(tunResource, req.Meta.OperationID, err)
		}
		if endpoint != nil {
			if err := endpoint.Verify(ctx); err != nil {
				return b.failed(endpointResource, req.Meta.OperationID, err)
			}
		}
		if err := backend.ConfigureDNS(ctx, tunResource.native, endpoint, req.Enabled); err != nil {
			var noEffects *NoEffectsError
			if errors.As(err, &noEffects) {
				return rejected(req.Meta.OperationID, err), err
			}
			return b.failed(tunResource, req.Meta.OperationID, err)
		}
		if err := ctx.Err(); err != nil {
			return b.failed(tunResource, req.Meta.OperationID, err)
		}
		b.mu.Lock()
		if b.closed || tunResource.ref.GraphEpoch != b.epoch {
			b.mu.Unlock()
			return b.failed(tunResource, req.Meta.OperationID, protocol.ErrStaleGraphEpoch)
		}
		if req.Enabled {
			tunResource.dns = req.Endpoint
		} else {
			tunResource.dns = protocol.ResourceRef{}
		}
		tunResource.ref.Generation++
		b.eventLocked(tunResource, false)
		b.mu.Unlock()
		return b.result(tunResource, protocol.OutcomeCommitted), nil
	})
	return result.MutationResult, err
}
