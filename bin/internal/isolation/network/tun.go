package network

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/gonnect/tun"
)

func (b *Broker) validateOptions(req protocol.CreateTunRequest) error {
	o := req.Options
	if len(o.Name) > int(b.policy.Limits.MaxStringBytes) || !utf8.ValidString(o.Name) || strings.ContainsAny(o.Name, "\x00\r\n") {
		return protocol.ErrInvalidContract
	}
	if o.MTU > math.MaxInt32 {
		return protocol.ErrInvalidContract
	}
	if len(o.Addresses)+len(o.Routes)+len(o.SourceRoutes)+len(req.Rules) > int(b.policy.Limits.MaxArrayItems) {
		return protocol.ErrLimitExceeded
	}
	for _, set := range [][]netip.Prefix{o.Addresses, o.Routes} {
		for _, prefix := range set {
			if !prefix.IsValid() {
				return protocol.ErrInvalidContract
			}
		}
	}
	for _, route := range o.SourceRoutes {
		if !route.Source.IsValid() || !route.Route.IsValid() || route.Source.Addr().BitLen() != route.Route.Addr().BitLen() {
			return protocol.ErrInvalidContract
		}
	}
	if !req.Default && (req.Strict || req.DNSIP.IsValid() || len(o.SourceRoutes) > 0 || len(req.Rules) > 0 || req.RuleMode != "") {
		return protocol.ErrInvalidContract
	}
	switch req.RuleMode {
	case "", "full":
		if len(req.Rules) > 0 {
			return protocol.ErrInvalidContract
		}
	case "include", "exclude":
	default:
		return protocol.ErrInvalidContract
	}
	for _, rule := range req.Rules {
		if rule.Type == "" || len(rule.Type)+len(rule.Value) > int(b.policy.Limits.MaxStringBytes) || !utf8.ValidString(rule.Type+rule.Value) || strings.ContainsRune(rule.Type+rule.Value, 0) {
			return protocol.ErrInvalidContract
		}
	}
	if p := b.policy.NetworkRestrictions; p != nil && p.AllowDefaultRoute != nil && !*p.AllowDefaultRoute {
		if req.Default {
			return protocol.ErrPolicyDenied
		}
		for _, route := range o.Routes {
			if route.Bits() == 0 {
				return protocol.ErrPolicyDenied
			}
		}
	}
	if p := b.policy.NetworkRestrictions; p != nil {
		if req.Strict && p.AllowStrictMode != nil && !*p.AllowStrictMode {
			return protocol.ErrPolicyDenied
		}
		for _, rule := range req.Rules {
			if slices.Contains(p.DeniedRuleTypes, rule.Type) {
				return protocol.ErrPolicyDenied
			}
		}
	}
	return nil
}

func canonical(req protocol.CreateTunRequest) protocol.CreateTunRequest {
	req.Meta = protocol.RequestMeta{}
	if req.RuleMode == "full" || len(req.Rules) == 0 {
		req.RuleMode = ""
	}
	req.Options.Addresses = canonicalPrefixes(req.Options.Addresses, false)
	req.Options.Routes = canonicalPrefixes(req.Options.Routes, true)
	req.Options.SourceRoutes = slices.Clone(req.Options.SourceRoutes)
	req.Rules = slices.Clone(req.Rules)
	return req
}

func canonicalPrefixes(in []netip.Prefix, masked bool) []netip.Prefix {
	out := slices.Clone(in)
	if masked {
		for i := range out {
			out[i] = out[i].Masked()
		}
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int { return strings.Compare(a.String(), b.String()) })
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b *Broker) CreateTun(ctx context.Context, req protocol.CreateTunRequest) (protocol.ResourceResult, error) {
	op := protocol.OperationCreateTun
	if req.Default {
		op = protocol.OperationCreateDefaultTun
	}
	input := req
	input.Meta.RequestID = 0
	return b.mutate(ctx, req.Meta, op, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		if err := b.validateOptions(req); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		if _, err := b.backend.Check(ctx, req); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		b.mu.Lock()
		if b.recovery {
			b.mu.Unlock()
			return rejected(req.Meta.OperationID, protocol.ErrRecoveryRequired), protocol.ErrRecoveryRequired
		}
		var previous *resource
		live, tunCount := 0, 0
		for _, r := range b.resources {
			if r.state.Closed {
				continue
			}
			live++
			if r.ref.Type == protocol.ResourceTun || r.ref.Type == protocol.ResourceDefaultTun {
				tunCount++
			}
			if req.Default && r.ref.Type == protocol.ResourceDefaultTun {
				previous = r
			}
		}
		recorded := len(b.resources)
		b.mu.Unlock()
		if previous != nil && reflect.DeepEqual(previous.options, canonical(req)) {
			if err := previous.native.Verify(ctx); err != nil {
				return b.failed(previous, req.Meta.OperationID, err)
			}
			return b.result(previous, protocol.OutcomeCommitted), nil
		}
		limit := b.policy.Limits.MaxLiveResources
		if p := b.policy.NetworkRestrictions; p != nil && p.MaxTuns > 0 && p.MaxTuns < limit {
			if previous != nil {
				tunCount--
			}
			if tunCount >= int(p.MaxTuns) {
				return rejected(req.Meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
			}
		}
		if previous != nil {
			live--
		}
		if live >= int(limit) || recorded >= protocol.MaxOperationReceipts {
			return rejected(req.Meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
		}
		if err := ctx.Err(); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		// Allocate the accounting record before any native effect, including
		// default replacement. Random IDs are never derived from native names.
		id, err := protocol.NewResourceID()
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		b.mu.Lock()
		_, collision := b.resources[id]
		b.mu.Unlock()
		if !id.Valid() || collision {
			return rejected(req.Meta.OperationID, protocol.ErrInvalidContract), protocol.ErrInvalidContract
		}
		kind := protocol.ResourceTun
		if req.Default {
			kind = protocol.ResourceDefaultTun
		}
		r := &resource{ref: protocol.ResourceRef{ID: id, SessionID: b.session.ID, Type: kind, Generation: 1, GraphEpoch: req.Meta.GraphEpoch}, options: canonical(req)}
		if b.journal != nil {
			r.journal = "resource-" + id.String()
			if err := b.journal.Put(supervision.Effect{ID: r.journal, SessionID: b.session.ID, ResourceType: kind, State: supervision.EffectPending}); err != nil {
				return rejected(req.Meta.OperationID, err), err
			}
		}
		if previous != nil {
			if err := b.closeNative(ctx, previous); err != nil {
				if journalErr := b.resolveJournal(r); journalErr != nil {
					err = errors.Join(err, journalErr)
				}
				return b.failed(previous, req.Meta.OperationID, err)
			}
		}
		b.mu.Lock()
		b.resources[id] = r
		b.mu.Unlock()
		native, createErr := b.backend.Create(ctx, req)
		r.native = native
		var noEffects *NoEffectsError
		if native == nil && errors.As(createErr, &noEffects) {
			if err := b.resolveJournal(r); err != nil {
				return b.failed(r, req.Meta.OperationID, err)
			}
			b.mu.Lock()
			r.state.Closed = true
			b.mu.Unlock()
			outcome := protocol.OutcomeRejected
			if previous != nil {
				outcome = protocol.OutcomeCompensated
			}
			return b.result(r, outcome), createErr
		}
		if createErr != nil || native == nil || ctx.Err() != nil {
			cause := errors.Join(createErr, ctx.Err())
			if native != nil && !errors.Is(createErr, protocol.ErrRecoveryRequired) {
				cleanup, cancel := context.WithTimeout(context.Background(), b.timeout)
				closeErr := b.closeNative(cleanup, r)
				cancel()
				if closeErr == nil {
					return b.result(r, protocol.OutcomeCompensated), cause
				}
				cause = errors.Join(cause, closeErr)
			}
			return b.failed(r, req.Meta.OperationID, cause)
		}
		if err := b.ownJournal(r); err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		if err := native.Verify(ctx); err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		state, err := native.State(ctx)
		if err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		if err = b.validateState(state); err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		b.mu.Lock()
		if ctx.Err() != nil || b.closed || b.epoch != req.Meta.GraphEpoch {
			b.mu.Unlock()
			cleanup, cancel := context.WithTimeout(context.Background(), b.timeout)
			defer cancel()
			if err := b.closeNative(cleanup, r); err != nil {
				return b.failed(r, req.Meta.OperationID, err)
			}
			return b.result(r, protocol.OutcomeCompensated), context.Canceled
		}
		r.state = cloneState(state)
		r.state.Revision = 0
		watch, stop := context.WithCancel(context.Background())
		r.stop = stop
		b.eventLocked(r, false)
		b.mu.Unlock()
		go b.watch(watch, r, native.Events())
		return b.result(r, protocol.OutcomeCommitted), nil
	})
}

func (b *Broker) validateState(state protocol.ResourceState) error {
	if len(state.Name) > int(b.policy.Limits.MaxStringBytes) || len(state.Addresses)+len(state.Routes) > int(b.policy.Limits.MaxArrayItems) || len(state.Warnings) > protocol.MaxResourceWarnings {
		return protocol.ErrLimitExceeded
	}
	for _, w := range state.Warnings {
		if len(w) > protocol.MaxResourceWarningBytes {
			return protocol.ErrLimitExceeded
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > int(b.policy.Limits.MaxControlFrameBytes)/2 {
		return protocol.ErrLimitExceeded
	}
	return nil
}

func (b *Broker) result(r *resource, outcome protocol.Outcome) protocol.ResourceResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return protocol.ResourceResult{MutationResult: protocol.MutationResult{Outcome: outcome, CurrentResource: r.ref}, Resource: r.ref, State: b.stateForRuntime(r.state)}
}

func (b *Broker) failed(r *resource, id string, err error) (protocol.ResourceResult, error) {
	if journalErr := b.uncertainJournal(r); journalErr != nil {
		err = errors.Join(err, journalErr)
	}
	b.mu.Lock()
	b.recovery = true
	r.state.RecoveryRequired = true
	r.state.Up = false
	if r.stop != nil {
		r.stop()
	}
	b.eventLocked(r, false)
	b.mu.Unlock()
	result := b.result(r, protocol.OutcomeUncertain)
	result.OperationID = id
	result.Code = "recovery"
	return result, errors.Join(protocol.ErrRecoveryRequired, err)
}

type recoverableNative interface{ RecoveryIdentity() string }

func (b *Broker) ownJournal(r *resource) error {
	if b.journal == nil || r.journal == "" {
		return nil
	}
	identity, ok := r.native.(recoverableNative)
	if !ok || identity.RecoveryIdentity() == "" {
		return supervision.ErrRecoveryUncertain
	}
	return b.journal.Put(supervision.Effect{ID: r.journal, SessionID: b.session.ID, ResourceType: r.ref.Type,
		Identity: identity.RecoveryIdentity(), State: supervision.EffectOwned})
}

func (b *Broker) uncertainJournal(r *resource) error {
	if b.journal == nil || r == nil || r.journal == "" {
		return nil
	}
	identity, ok := r.native.(recoverableNative)
	if !ok || identity.RecoveryIdentity() == "" {
		// Keep the pending record. It explicitly requires inspection and must not
		// be converted into a name-based ownership claim.
		return nil
	}
	return b.journal.Put(supervision.Effect{ID: r.journal, SessionID: b.session.ID, ResourceType: r.ref.Type,
		Identity: identity.RecoveryIdentity(), State: supervision.EffectUncertain})
}

func (b *Broker) resolveJournal(r *resource) error {
	if b.journal == nil || r == nil || r.journal == "" {
		return nil
	}
	return b.journal.Resolve(r.journal)
}

func (b *Broker) watch(ctx context.Context, r *resource, events <-chan tun.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			b.mu.Lock()
			if ctx.Err() != nil || b.closed || r.state.Closed || r.state.RecoveryRequired || r.ref.GraphEpoch != b.epoch {
				b.mu.Unlock()
				return
			}
			if !ok {
				// Event-channel close is not proof that host cleanup succeeded.
				r.state.Up = false
				r.state.RecoveryRequired = true
				b.recovery = true
			} else {
				if event&tun.EventDown != 0 {
					r.state.Up = false
				}
				if event&tun.EventUp != 0 {
					r.state.Up = true
				}
			}
			b.eventLocked(r, event&tun.EventMTUUpdate != 0)
			b.mu.Unlock()
			if !ok {
				return
			}
		}
	}
}

func (b *Broker) closeNative(ctx context.Context, r *resource) error {
	b.mu.Lock()
	if r.state.Closed {
		b.mu.Unlock()
		return nil
	}
	if r.stop != nil {
		r.stop()
	}
	r.state.Up = false
	r.closing = true
	var children []*resource
	for id, offer := range b.offers {
		if id == r.ref.ID || offer.parent == r.ref.ID {
			offer.timer.Stop()
			delete(b.offers, id)
			if id != r.ref.ID {
				children = append(children, b.resources[id])
			}
		}
	}
	b.mu.Unlock()
	if r.ref.Type == protocol.ResourceMatcher {
		return b.closeMatcher(ctx, r)
	}
	if r.ref.Type == protocol.ResourceDNS {
		if r.native == nil {
			return protocol.ErrRecoveryRequired
		}
		if err := r.native.Verify(ctx); err != nil {
			return errors.Join(protocol.ErrRecoveryRequired, err)
		}
		backend, ok := b.backend.(DNSBackend)
		if !ok {
			return protocol.ErrRecoveryRequired
		}
		b.mu.Lock()
		var owners []*resource
		for _, candidate := range b.resources {
			if candidate.dns == r.ref && !candidate.state.Closed {
				owners = append(owners, candidate)
			}
		}
		b.mu.Unlock()
		for _, owner := range owners {
			if err := backend.ConfigureDNS(ctx, owner.native, nil, false); err != nil {
				return errors.Join(protocol.ErrRecoveryRequired, err)
			}
			b.mu.Lock()
			owner.dns = protocol.ResourceRef{}
			if owner.ref.Generation == math.MaxUint64 {
				b.recovery = true
				b.mu.Unlock()
				return protocol.ErrLimitExceeded
			}
			owner.ref.Generation++
			b.eventLocked(owner, false)
			b.mu.Unlock()
		}
	}
	for _, child := range children {
		if err := b.closeNative(ctx, child); err != nil {
			return err
		}
	}
	if r.native == nil {
		return protocol.ErrRecoveryRequired
	}
	if r.ref.Type != protocol.ResourceDNS {
		if err := r.native.Verify(ctx); err != nil {
			return errors.Join(protocol.ErrRecoveryRequired, err)
		}
	}
	if err := r.native.Close(ctx); err != nil {
		return errors.Join(protocol.ErrRecoveryRequired, err)
	}
	if err := b.resolveJournal(r); err != nil {
		return errors.Join(protocol.ErrRecoveryRequired, err)
	}
	if err := b.waitData(ctx, r.ref.ID); err != nil {
		return err
	}
	if t, ok := r.native.(*systemTun); ok {
		t.clearPackets()
	}
	b.mu.Lock()
	r.state.Closed = true
	r.state.RecoveryRequired = false
	r.native = nil
	r.options = protocol.CreateTunRequest{}
	r.dns = protocol.ResourceRef{}
	r.stop = nil
	b.eventLocked(r, false)
	b.mu.Unlock()
	return nil
}

func updateOperation(kind protocol.TunUpdateKind) protocol.Operation {
	switch kind {
	case protocol.TunUpdateMTU:
		return protocol.OperationSetTunMTU
	case protocol.TunUpdateAddresses:
		return protocol.OperationSetTunAddresses
	case protocol.TunUpdateRoutes:
		return protocol.OperationSetTunRoutes
	case protocol.TunUpdateName:
		return protocol.OperationSetTunName
	default:
		return ""
	}
}

func (b *Broker) UpdateTun(ctx context.Context, req protocol.UpdateTunRequest) (protocol.MutationResult, error) {
	if req.Resource.Type != protocol.ResourceTun && req.Resource.Type != protocol.ResourceDefaultTun {
		return protocol.MutationResult{}, protocol.ErrResourceType
	}
	input := req
	input.Meta.RequestID = 0
	result, err := b.mutate(ctx, req.Meta, updateOperation(req.Kind), input, func(ctx context.Context) (protocol.ResourceResult, error) {
		b.mu.Lock()
		r, err := b.lookup(req.Resource)
		if err == nil && (r.state.Closed || r.state.RecoveryRequired) {
			err = protocol.ErrCapabilityUnavailable
		}
		b.mu.Unlock()
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		opts := r.options
		if (req.Kind != protocol.TunUpdateMTU && req.MTU != 0) ||
			(req.Kind != protocol.TunUpdateName && req.Name != "") ||
			(req.Kind != protocol.TunUpdateAddresses && len(req.Addresses) > 0) ||
			(req.Kind != protocol.TunUpdateRoutes && len(req.Routes) > 0) {
			return rejected(req.Meta.OperationID, protocol.ErrInvalidContract), protocol.ErrInvalidContract
		}
		switch req.Kind {
		case protocol.TunUpdateMTU:
			if req.Name != "" || len(req.Addresses)+len(req.Routes) > 0 {
				return rejected(req.Meta.OperationID, protocol.ErrInvalidContract), protocol.ErrInvalidContract
			}
			opts.Options.MTU = req.MTU
		case protocol.TunUpdateAddresses:
			opts.Options.Addresses = req.Addresses
		case protocol.TunUpdateRoutes:
			opts.Options.Routes = req.Routes
		case protocol.TunUpdateName:
			opts.Options.Name = req.Name
		default:
			return rejected(req.Meta.OperationID, protocol.ErrUnsupportedOperation), protocol.ErrUnsupportedOperation
		}
		if err := b.validateOptions(opts); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		if err := r.native.Verify(ctx); err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		report, err := r.native.Capabilities(ctx)
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		if err := canUpdate(report, req.Kind); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		if reflect.DeepEqual(canonical(opts), r.options) {
			return b.result(r, protocol.OutcomeCommitted), nil
		}
		if r.ref.Generation == math.MaxUint64 {
			return rejected(req.Meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
		}
		if err := ctx.Err(); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		if err := r.native.Update(ctx, req); err != nil {
			var noEffects *NoEffectsError
			if errors.As(err, &noEffects) {
				return rejected(req.Meta.OperationID, err), err
			}
			return b.failed(r, req.Meta.OperationID, err)
		}
		state, err := r.native.State(ctx)
		if err == nil {
			err = b.validateState(state)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		b.mu.Lock()
		if b.closed || r.ref.GraphEpoch != b.epoch {
			b.mu.Unlock()
			return b.failed(r, req.Meta.OperationID, protocol.ErrStaleGraphEpoch)
		}
		state.Revision, state.Up = r.state.Revision, r.state.Up
		r.state = cloneState(state)
		r.options = canonical(opts)
		r.ref.Generation++
		b.eventLocked(r, req.Kind == protocol.TunUpdateMTU)
		b.mu.Unlock()
		return b.result(r, protocol.OutcomeCommitted), nil
	})
	return result.MutationResult, err
}

func canUpdate(report protocol.CapabilitySnapshot, kind protocol.TunUpdateKind) error {
	name := ""
	switch kind {
	case protocol.TunUpdateMTU:
		name = "set_mtu"
	case protocol.TunUpdateAddresses:
		name = "set_addresses"
	case protocol.TunUpdateRoutes:
		name = "set_routes"
	case protocol.TunUpdateName:
		name = "rename"
	default:
		return protocol.ErrUnsupportedOperation
	}
	result := protocol.ErrCapabilityUnsupported
	for _, entry := range report.Operations {
		if entry.Operation != updateOperation(kind) || (entry.NativeOperation != "" && entry.NativeOperation != name) {
			continue
		}
		if entry.CanAttempt() {
			return nil
		}
		if entry.State == protocol.CapabilityUnavailable {
			result = protocol.ErrCapabilityUnavailable
		}
	}
	return result
}

func (b *Broker) CloseResource(ctx context.Context, req protocol.ResourceRequest) (protocol.MutationResult, error) {
	input := req
	input.Meta.RequestID = 0
	result, err := b.mutate(ctx, req.Meta, protocol.OperationCloseResource, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		b.mu.Lock()
		r, err := b.lookup(req.Resource)
		b.mu.Unlock()
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		if err := b.closeNative(ctx, r); err != nil {
			return b.failed(r, req.Meta.OperationID, err)
		}
		return b.result(r, protocol.OutcomeCommitted), nil
	})
	return result.MutationResult, err
}

func (b *Broker) ReleaseGraphEpoch(ctx context.Context, req protocol.ReleaseGraphEpochRequest) (protocol.MutationResult, error) {
	input := req
	input.Meta.RequestID = 0
	result, err := b.mutate(ctx, req.Meta, protocol.OperationReleaseGraphEpoch, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		b.mu.Lock()
		if req.GraphEpoch != b.epoch || b.epoch == math.MaxUint64 {
			b.mu.Unlock()
			return rejected(req.Meta.OperationID, protocol.ErrStaleGraphEpoch), protocol.ErrStaleGraphEpoch
		}
		b.epoch++
		for _, r := range b.resources {
			if r.stop != nil {
				r.stop()
			}
			r.state.Up = false
		}
		b.mu.Unlock()
		if err := b.cleanup(ctx); err != nil {
			return uncertain(req.Meta.OperationID), err
		}
		return protocol.ResourceResult{MutationResult: protocol.MutationResult{Outcome: protocol.OutcomeCommitted}}, nil
	})
	return result.MutationResult, err
}

func (b *Broker) cleanup(ctx context.Context) error {
	b.mu.Lock()
	resources := make([]*resource, 0, len(b.resources))
	for _, r := range b.resources {
		resources = append(resources, r)
	}
	b.mu.Unlock()
	var result error
	for _, r := range resources {
		if err := b.closeNative(ctx, r); err != nil {
			_, failure := b.failed(r, "", err)
			result = errors.Join(result, failure)
		}
		if ctx.Err() != nil {
			return errors.Join(protocol.ErrRecoveryRequired, result, ctx.Err())
		}
	}
	return result
}

// Shutdown immediately rejects new work and cancels the current native call.
// The single cleanup worker waits for that call; callers wait at most five
// seconds. A hung backend leaves recovery visible, with its objects retained.
func (b *Broker) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	if b.data != nil {
		_ = b.data.Close()
	}
	b.closed = true
	b.stopOnce.Do(func() { close(b.stopped) })
	if b.cancel != nil {
		b.cancel()
	}
	for _, r := range b.resources {
		if r.stop != nil {
			r.stop()
		}
	}
	b.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	select {
	case b.gate <- struct{}{}:
	case <-bounded.Done():
		b.mu.Lock()
		b.recovery = true
		b.mu.Unlock()
		return errors.Join(protocol.ErrRecoveryRequired, bounded.Err())
	}
	done := make(chan error, 1)
	go func() { defer func() { <-b.gate }(); done <- b.cleanup(bounded) }()
	select {
	case err := <-done:
		return err
	case <-bounded.Done():
		b.mu.Lock()
		b.recovery = true
		b.mu.Unlock()
		return errors.Join(protocol.ErrRecoveryRequired, bounded.Err())
	}
}
