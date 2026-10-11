package network

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type readResult[T any] struct {
	value T
	err   error
}

// Serve owns the authenticated lanes until peer loss, then revokes and cleans
// the session. Bootstrap is responsible for platform endpoint authentication.
func (b *Broker) Serve(ctx context.Context, lanes transport.Lanes) error {
	if err := b.AttachData(ctx, lanes.Data); err != nil {
		lanes.Close()
		return err
	}
	peer, err := transport.New(ctx, lanes, b.session, b.Handle)
	if err != nil {
		return errors.Join(err, b.Shutdown(context.Background()))
	}
	defer func() { _ = peer.Close() }()
	for {
		select {
		case <-b.data.Done():
			return errors.Join(b.data.Err(), b.Shutdown(context.Background()))
		case <-b.stopped:
			return b.Shutdown(context.Background())
		case <-peer.Done():
			return errors.Join(peer.Err(), b.Shutdown(context.Background()))
		case event := <-b.events:
			if err := peer.Notify(event); err != nil {
				return errors.Join(err, b.Shutdown(context.Background()))
			}
		}
	}
}

func read[T any](ctx context.Context, b *Broker, meta protocol.RequestMeta, op protocol.Operation, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := b.authorize(meta, false, op); err != nil {
		return zero, err
	}
	bounded, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	select {
	case b.gate <- struct{}{}:
	case <-bounded.Done():
		return zero, bounded.Err()
	}
	if err := bounded.Err(); err != nil {
		<-b.gate
		return zero, err
	}
	b.mu.Lock()
	err := b.current(meta)
	b.mu.Unlock()
	if err != nil {
		<-b.gate
		return zero, err
	}
	done := make(chan readResult[T], 1)
	go func() {
		defer func() { <-b.gate }()
		value, err := fn(bounded)
		done <- readResult[T]{value, err}
	}()
	select {
	case result := <-done:
		// A canceled or revoked read must not publish a late native result.
		if err := bounded.Err(); err != nil {
			return zero, err
		}
		b.mu.Lock()
		err := b.current(meta)
		b.mu.Unlock()
		if err != nil {
			return zero, err
		}
		return result.value, result.err
	case <-bounded.Done():
		return zero, bounded.Err()
	}
}

func (b *Broker) Capabilities(ctx context.Context, req protocol.ReadRequest) (protocol.CapabilitySnapshot, error) {
	return read(ctx, b, req.Meta, protocol.OperationCapabilities, func(ctx context.Context) (protocol.CapabilitySnapshot, error) {
		snapshot, err := b.backend.Capabilities(ctx)
		if err != nil {
			return protocol.CapabilitySnapshot{}, err
		}
		for i := range snapshot.Operations {
			entry := &snapshot.Operations[i]
			if !b.policy.AllowsNetwork(entry.Operation) {
				entry.State = protocol.CapabilityUnavailable
				entry.AttemptAllowed = false
				entry.Reasons = append(entry.Reasons, "policy_denied")
			}
		}
		denyDefault := !b.policy.AllowsNetwork(protocol.OperationCreateDefaultTun)
		denyMatchers := !b.policy.AllowsNetwork(protocol.OperationCreateMatcher) || !b.policy.AllowsNetwork(protocol.OperationMatch)
		for i := range snapshot.Rules {
			rule := &snapshot.Rules[i]
			denied := denyMatchers
			if b.matcherRuleDenied(rule.Type) {
				denied = true
			}
			if b.matcherRuleDenied(rule.Type) || !b.policy.AllowsNetwork(protocol.OperationCheckRule) {
				rule.Validation.State = protocol.CapabilityUnavailable
				rule.Validation.AttemptAllowed = false
				rule.Validation.Reasons = append(rule.Validation.Reasons, "policy_denied")
			}
			if b.matcherRuleDenied(rule.Type) || !b.policy.AllowsNetwork(protocol.OperationCompleteRule) {
				rule.Completion.State = protocol.CapabilityUnavailable
				rule.Completion.AttemptAllowed = false
				rule.Completion.Reasons = append(rule.Completion.Reasons, "policy_denied")
			}
			if denied {
				for j := range rule.Matchers {
					entry := &rule.Matchers[j].Capability
					entry.State = protocol.CapabilityUnavailable
					entry.AttemptAllowed = false
					entry.Reasons = append(entry.Reasons, "policy_denied")
				}
			}
		}
		denyDNS := !b.policy.AllowsNetwork(protocol.OperationConfigureDNS)
		if p := b.policy.NetworkRestrictions; p != nil && p.AllowDefaultRoute != nil && !*p.AllowDefaultRoute {
			denyDefault = true
		}
		if p := b.policy.NetworkRestrictions; p != nil && p.AllowHostDNS != nil && !*p.AllowHostDNS {
			denyDNS = true
		}
		if denyDefault {
			for i := range snapshot.DefaultTunProfiles {
				entry := &snapshot.DefaultTunProfiles[i].Capability
				entry.State = protocol.CapabilityUnavailable
				entry.AttemptAllowed = false
				entry.Reasons = append(entry.Reasons, "policy_denied")
			}
			for i := range snapshot.Operations {
				entry := &snapshot.Operations[i]
				if entry.Operation == protocol.OperationCreateDefaultTun {
					entry.State = protocol.CapabilityUnavailable
					entry.AttemptAllowed = false
					entry.Reasons = append(entry.Reasons, "policy_denied")
				}
			}
		}
		if p := b.policy.NetworkRestrictions; p != nil {
			for i := range snapshot.DefaultTunProfiles {
				profile := &snapshot.DefaultTunProfiles[i]
				if profile.Strict && p.AllowStrictMode != nil && !*p.AllowStrictMode {
					profile.Capability.State = protocol.CapabilityUnavailable
					profile.Capability.AttemptAllowed = false
					profile.Capability.Reasons = append(profile.Capability.Reasons, "policy_denied")
				}
				for j := range profile.Rules {
					rule := &profile.Rules[j]
					if slices.Contains(p.DeniedRuleTypes, rule.Type) {
						rule.Capability.State = protocol.CapabilityUnavailable
						rule.Capability.AttemptAllowed = false
						rule.Capability.Reasons = append(rule.Capability.Reasons, "policy_denied")
					}
				}
			}
		}
		if denyDNS {
			for i := range snapshot.Operations {
				entry := &snapshot.Operations[i]
				if entry.Operation == protocol.OperationConfigureDNS {
					entry.State = protocol.CapabilityUnavailable
					entry.AttemptAllowed = false
					entry.Reasons = append(entry.Reasons, "policy_denied")
				}
			}
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			return protocol.CapabilitySnapshot{}, err
		}
		if len(data) > snapshotPayloadLimit(b.policy.Limits.MaxControlFrameBytes) {
			return protocol.CapabilitySnapshot{}, protocol.ErrLimitExceeded
		}
		return snapshot, nil
	})
}

func (b *Broker) CheckTun(ctx context.Context, req protocol.CheckTunRequest) (protocol.ValidationResult, error) {
	return b.check(ctx, req.Meta, protocol.OperationCheckTun, protocol.CreateTunRequest{Options: req.Options})
}

func (b *Broker) CheckDefaultTun(ctx context.Context, req protocol.CheckDefaultTunRequest) (protocol.ValidationResult, error) {
	return b.check(ctx, req.Meta, protocol.OperationCheckDefaultTun, protocol.CreateTunRequest{Default: true, Options: req.Options, Strict: req.Strict, Rules: req.Rules, RuleMode: req.RuleMode, DNSIP: req.DNSIP})
}

func (b *Broker) check(ctx context.Context, meta protocol.RequestMeta, op protocol.Operation, req protocol.CreateTunRequest) (protocol.ValidationResult, error) {
	return read(ctx, b, meta, op, func(ctx context.Context) (protocol.ValidationResult, error) {
		if err := b.validateOptions(req); err != nil {
			return protocol.ValidationResult{}, err
		}
		return b.backend.Check(ctx, req)
	})
}

func (b *Broker) GetResourceState(ctx context.Context, req protocol.ResourceRequest) (protocol.ResourceState, error) {
	if req.Resource.Type == protocol.ResourceMatcher || req.Resource.Type == protocol.ResourceAllocation {
		return protocol.ResourceState{}, protocol.ErrResourceType
	}
	return read(ctx, b, req.Meta, protocol.OperationGetTunState, func(ctx context.Context) (protocol.ResourceState, error) {
		b.mu.Lock()
		r, err := b.lookup(req.Resource)
		if err != nil {
			b.mu.Unlock()
			return protocol.ResourceState{}, err
		}
		if r.state.Closed || r.state.RecoveryRequired {
			state := b.stateForRuntime(r.state)
			b.mu.Unlock()
			return state, nil
		}
		b.mu.Unlock()
		if err := r.native.Verify(ctx); err != nil {
			_, failure := b.failed(r, "", err)
			return protocol.ResourceState{}, failure
		}
		state, err := r.native.State(ctx)
		if err != nil {
			return protocol.ResourceState{}, err
		}
		if err = b.validateState(state); err != nil {
			return protocol.ResourceState{}, err
		}
		b.mu.Lock()
		state.Revision, state.Up = r.state.Revision, r.state.Up
		b.mu.Unlock()
		return b.stateForRuntime(state), nil
	})
}

// Handle is the closed checkpoint-18 dispatch table for transport.New. Later
// checkpoint operations fail explicitly; they never fall back to native calls
// in the runtime. Mutation replies contain receipts even when the operation
// fails; errors from native work do not discard the recorded outcome.
func (b *Broker) Handle(ctx context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
	switch op {
	case protocol.OperationRuntimeReady:
		var req protocol.ReadRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.MarkRuntimeReady(ctx, req)
	case protocol.OperationChannel:
		var req protocol.ChannelRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.Channel(ctx, req)
	case protocol.OperationOpenSocket:
		var req protocol.OpenSocketRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.OpenSocket(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationListInterfaces:
		var req protocol.InterfaceRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.ListNetworkInterfaces(ctx, req)
	case protocol.OperationResolverSnapshot:
		var req protocol.ReadRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.ResolverSnapshot(ctx, req)
	case protocol.OperationCapabilities, protocol.OperationStatus:
		var req protocol.ReadRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		if op == protocol.OperationStatus {
			return b.Status(ctx, req)
		}
		return b.Capabilities(ctx, req)
	case protocol.OperationCheckTun:
		var req protocol.CheckTunRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.CheckTun(ctx, req)
	case protocol.OperationCheckDefaultTun:
		var req protocol.CheckDefaultTunRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.CheckDefaultTun(ctx, req)
	case protocol.OperationCheckRule:
		var req protocol.CheckRuleRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.CheckRule(ctx, req)
	case protocol.OperationCompleteRule:
		var req protocol.CompleteRuleRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.CompleteRule(ctx, req)
	case protocol.OperationReserveIP, protocol.OperationReserveSubnet:
		var req protocol.ReserveAddressRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		if allocationOperation(req.Kind) != op {
			return nil, protocol.ErrInvalidContract
		}
		result, err := b.ReserveAddress(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationReleaseAllocation:
		var req protocol.ResourceRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.ReleaseAllocation(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationCreateTun, protocol.OperationCreateDefaultTun:
		var req protocol.CreateTunRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		if req.Default != (op == protocol.OperationCreateDefaultTun) {
			return nil, protocol.ErrInvalidContract
		}
		result, err := b.CreateTun(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationCreateMatcher:
		var req protocol.CreateMatcherRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.CreateMatcher(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationMatch:
		var req protocol.MatchRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		return b.Match(ctx, req)
	case protocol.OperationConfigureDNS:
		var req protocol.ConfigureDNSRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.ConfigureDNS(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationCreateDNSEndpoint:
		var req protocol.CreateDNSEndpointRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.CreateDNSEndpoint(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationSetTunMTU, protocol.OperationSetTunAddresses, protocol.OperationSetTunRoutes, protocol.OperationSetTunName:
		var req protocol.UpdateTunRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		if updateOperation(req.Kind) != op {
			return nil, protocol.ErrInvalidContract
		}
		result, err := b.UpdateTun(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationGetTunState, protocol.OperationCloseResource:
		var req protocol.ResourceRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		if op == protocol.OperationGetTunState {
			return b.GetResourceState(ctx, req)
		}
		result, err := b.CloseResource(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationReleaseGraphEpoch:
		var req protocol.ReleaseGraphEpochRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.ReleaseGraphEpoch(ctx, req)
		return mutationReply(result, err)
	case protocol.OperationGetOperationResult:
		var req protocol.OperationResultRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		result, err := b.GetResult(ctx, req)
		return mutationReply(result, err)
	default:
		return nil, protocol.ErrUnsupportedOperation
	}
}

func mutationReply[T any](result T, err error) (any, error) {
	// Mutation failure is in the receipt, not a transport error that discards
	// its body. No native error strings cross the channel.
	code := ""
	if err != nil {
		code = transport.ErrorCode(err)
		for _, entry := range []struct {
			code string
			err  error
		}{
			{"recovery", protocol.ErrRecoveryRequired}, {"invalid", protocol.ErrInvalidContract},
			{"unsupported", protocol.ErrCapabilityUnsupported}, {"unavailable", protocol.ErrCapabilityUnavailable},
			{"denied", protocol.ErrPolicyDenied}, {"limit", protocol.ErrLimitExceeded},
			{"foreign", protocol.ErrForeignResource}, {"type", protocol.ErrResourceType},
			{"generation", protocol.ErrStaleGeneration}, {"epoch", protocol.ErrStaleGraphEpoch},
			{"conflict", protocol.ErrOperationConflict}, {"unknown", protocol.ErrOperationUnknown},
			{"canceled", context.Canceled}, {"deadline", context.DeadlineExceeded},
		} {
			if errors.Is(err, entry.err) {
				code = entry.code
				break
			}
		}
	}
	switch value := any(result).(type) {
	case protocol.ResourceResult:
		if value.OperationID == "" {
			return nil, err
		}
		value.Code = code
		return value, nil
	case protocol.MutationResult:
		value.Code = code
		return value, nil
	default:
		return nil, protocol.ErrInvalidContract
	}
}

func (b *Broker) boundSnapshot(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > snapshotPayloadLimit(b.policy.Limits.MaxControlFrameBytes) {
		return protocol.ErrLimitExceeded
	}
	return nil
}

func snapshotPayloadLimit(frameBytes uint32) int {
	// Control replies encode the snapshot as raw JSON inside a small fixed
	// envelope. Keep 4 KiB for that envelope and future fixed metadata without
	// discarding complete native capability reports that exceed half a frame.
	if frameBytes <= 4096 {
		return int(frameBytes) / 2
	}
	return int(frameBytes) - 4096
}
