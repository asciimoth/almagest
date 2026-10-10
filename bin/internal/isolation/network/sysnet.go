package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
)

// NativeIdentity is captured by trusted native integration at creation. Its
// Verify must bind the original handle to the current native identity, not
// merely find an interface with the same name. Platform integration owns this
// check because sysnet.System does not export a portable native identity.
type NativeIdentity interface{ Verify(context.Context) error }

// CaptureIdentity is launcher-supplied code, never a runtime callback. It must
// not adopt another device when the original disappears. The backend must also
// perform identity-safe native updates/cleanup (see OwnedTun).
type CaptureIdentity func(context.Context, tun.Tun) (NativeIdentity, error)

// SystemBackend adapts an exclusively owned sysnet.System. Capture is required:
// lack of an identity guard is not permission to use name-only ownership.
// Production bootstrap must supply the qualified platform guard and backend.
type SystemBackend struct {
	resolverSource ResolverSnapshotSource
	system         sysnet.System
	capture        CaptureIdentity
	defaultOwned   *systemTun
}

func NewSystemBackend(system sysnet.System, capture CaptureIdentity) (*SystemBackend, error) {
	if system == nil || capture == nil {
		return nil, protocol.ErrInvalidContract
	}
	return &SystemBackend{system: system, capture: capture}, nil
}

func (s *SystemBackend) ConfigureDNS(ctx context.Context, owned OwnedTun, endpoint OwnedDNSEndpoint, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return &NoEffectsError{Err: err}
	}
	t, ok := owned.(*systemTun)
	if !ok || !t.defaultTun || t.backend != s {
		return &NoEffectsError{Err: protocol.ErrResourceType}
	}
	if err := t.Verify(ctx); err != nil {
		return &NoEffectsError{Err: err}
	}
	device, ok := t.device.(sysnet.DefaultTun)
	if !ok {
		return &NoEffectsError{Err: protocol.ErrCapabilityUnsupported}
	}
	var resolver gdns.Interface
	if enabled {
		if endpoint == nil || endpoint.Resolver() == nil {
			return &NoEffectsError{Err: protocol.ErrInvalidContract}
		}
		resolver = endpoint.Resolver()
	}
	if err := device.SetDNS(resolver); err != nil {
		return nativeError(err)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(protocol.ErrRecoveryRequired, err)
	}
	return nil
}

func prefixes(values []netip.Prefix) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = value.String()
	}
	return out
}

func nativeOptions(req protocol.CreateTunRequest) (sysnet.TunOpts, sysnet.DefaultTunOpts) {
	o := req.Options
	regular := sysnet.TunOpts{Name: o.Name, MTU: int(o.MTU), TunAddrs: prefixes(o.Addresses), TunRoutes: prefixes(o.Routes)}
	def := sysnet.DefaultTunOpts{Name: o.Name, MTU: int(o.MTU), TunAddrs: prefixes(o.Addresses), TunRoutes: prefixes(o.Routes), Strict: req.Strict}
	if req.DNSIP.IsValid() {
		def.DnsIP = req.DNSIP.String()
	}
	for _, route := range o.SourceRoutes {
		def.SourceRoutes = append(def.SourceRoutes, sysnet.TunSourceRoute{Source: route.Source.Addr(), Destination: route.Route})
	}
	for _, rule := range req.Rules {
		native := sysnet.Rule{Type: rule.Type, Rule: rule.Value}
		if req.RuleMode == "include" {
			def.Include = append(def.Include, native)
		} else {
			def.Exclude = append(def.Exclude, native)
		}
	}
	return regular, def
}

func nativeError(err error) error {
	var unknown net.UnknownNetworkError
	if errors.As(err, &unknown) {
		return errors.Join(protocol.ErrCapabilityUnsupported, err)
	}
	switch {
	case errors.Is(err, sysnet.ErrNotSupported):
		return errors.Join(protocol.ErrCapabilityUnsupported, err)
	case errors.Is(err, sysnet.ErrUnavailable):
		return errors.Join(protocol.ErrCapabilityUnavailable, err)
	case errors.Is(err, sysnet.ErrInvalidOptions):
		return errors.Join(protocol.ErrInvalidContract, err)
	default:
		return err
	}
}

func (s *SystemBackend) Check(ctx context.Context, req protocol.CreateTunRequest) (protocol.ValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return protocol.ValidationResult{}, err
	}
	regular, def := nativeOptions(req)
	var report sysnet.ValidationReport
	if req.Default {
		report = s.system.CheckDefaultTunOpts(def)
	} else {
		report = s.system.CheckTunOpts(regular)
	}
	// Unknown, advisory probe results do not prohibit a valid attempt.
	var blocking sysnet.ValidationReport
	for _, issue := range report.Issues {
		issueErr := (sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{issue}}).Err()
		if errors.Is(issueErr, sysnet.ErrCapabilityUnknown) && !errors.Is(issueErr, sysnet.ErrInvalidOptions) {
			continue
		}
		blocking.Issues = append(blocking.Issues, issue)
	}
	if err := blocking.Err(); err != nil {
		return protocol.ValidationResult{}, nativeError(err)
	}
	if err := s.checkName(ctx, req.Options.Name, req.Default); err != nil {
		return protocol.ValidationResult{}, err
	}
	return protocol.ValidationResult{}, nil
}

func (s *SystemBackend) checkName(ctx context.Context, name string, def bool) error {
	if name == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range interfaces {
		match := iface.Name == name || (runtime.GOOS == "windows" && strings.EqualFold(iface.Name, name))
		if !match {
			continue
		}
		if def && s.defaultOwned != nil && s.defaultOwned.Verify(ctx) == nil {
			ownedName, err := s.defaultOwned.device.Name()
			if err == nil && ownedName == iface.Name {
				return nil
			}
		}
		return protocol.ErrInvalidContract
	}
	return nil
}

func (s *SystemBackend) Create(ctx context.Context, req protocol.CreateTunRequest) (OwnedTun, error) {
	if err := ctx.Err(); err != nil {
		return nil, &NoEffectsError{Err: err}
	}
	// Recheck the collision after preflight. Native constructors must also
	// enforce exclusive creation, because a foreign process can race this.
	if err := s.checkName(ctx, req.Options.Name, false); err != nil {
		return nil, &NoEffectsError{Err: err}
	}
	regular, def := nativeOptions(req)
	var device tun.Tun
	var err error
	if req.Default {
		device, err = s.system.BuildDefaultTun(def)
	} else {
		device, err = s.system.BuildTun(regular)
	}
	if device == nil {
		return nil, err
	}
	owned := &systemTun{backend: s, device: device, defaultTun: req.Default}
	if err != nil {
		return owned, errors.Join(protocol.ErrRecoveryRequired, err)
	}
	// Capture ownership even if cancellation raced native creation. This is
	// trusted accounting work needed to compensate a late successful create.
	captureCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	identity, err := s.capture(captureCtx, device)
	if err != nil || identity == nil {
		return owned, errors.Join(protocol.ErrRecoveryRequired, err)
	}
	owned.identity = identity
	if req.Default {
		s.defaultOwned = owned
	}
	return owned, nil
}

type systemTun struct {
	reserveData      func(int) (func(), error)
	releaseData      func()
	pendingPackets   [][]byte
	pendingReadError error
	backend          *SystemBackend
	device           tun.Tun
	identity         NativeIdentity
	defaultTun       bool
	expected         *protocol.ResourceState
}

func (t *systemTun) Verify(ctx context.Context) error {
	if t.identity == nil {
		return protocol.ErrRecoveryRequired
	}
	if err := t.identity.Verify(ctx); err != nil {
		return err
	}
	if t.expected != nil {
		state, err := t.readState(ctx)
		if err != nil {
			return err
		}
		if state.Name != t.expected.Name || state.MTU != t.expected.MTU ||
			!slices.Equal(canonicalPrefixes(state.Addresses, false), canonicalPrefixes(t.expected.Addresses, false)) ||
			!slices.Equal(canonicalPrefixes(state.Routes, true), canonicalPrefixes(t.expected.Routes, true)) {
			return protocol.ErrRecoveryRequired
		}
	}
	return nil
}

func (t *systemTun) Events() <-chan tun.Event { return t.device.Events() }

func (t *systemTun) Close(ctx context.Context) error {
	if err := t.Verify(ctx); err != nil {
		return err
	}
	return t.device.Close()
}

func parsePrefixes(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil {
				return nil, err
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, prefix)
	}
	return out, nil
}

func (t *systemTun) State(ctx context.Context) (protocol.ResourceState, error) {
	if err := t.Verify(ctx); err != nil {
		return protocol.ResourceState{}, err
	}
	state, err := t.readState(ctx)
	if err == nil && t.expected == nil {
		saved := cloneState(state)
		t.expected = &saved
	}
	return state, err
}

func (t *systemTun) readState(ctx context.Context) (protocol.ResourceState, error) {
	var state protocol.ResourceState
	name, mtu, err := t.interfaceState(ctx)
	if err != nil {
		return state, err
	}
	state.Name = name
	if mtu < 0 {
		return state, protocol.ErrInvalidContract
	}
	state.MTU = uint32(mtu)
	addrs, err := t.backend.system.GetTunAddrs(t.device)
	if err != nil && !errors.Is(err, sysnet.ErrNotSupported) {
		return state, nativeError(err)
	}
	state.Addresses, err = parsePrefixes(addrs)
	if err != nil {
		return state, err
	}
	routes, err := t.backend.system.GetTunRoutes(t.device)
	if err != nil && !errors.Is(err, sysnet.ErrNotSupported) {
		return state, nativeError(err)
	}
	state.Routes, err = parsePrefixes(routes)
	if err != nil {
		return state, err
	}
	var warnings []sysnet.Warning
	if device, ok := t.device.(sysnet.DefaultTun); t.defaultTun && ok {
		warnings = t.backend.system.DefaultTunWarnings(device)
	} else {
		warnings = t.backend.system.TunWarnings(t.device)
	}
	for _, warning := range warnings {
		state.Warnings = append(state.Warnings, string(warning))
	}
	report, err := t.backend.system.CapabilitiesForTun(t.device)
	if err != nil {
		return state, nativeError(err)
	}
	state.Capabilities = operations(report.Operations)
	state.CapabilityRevision = report.InstanceRevision
	return state, nil
}

func (t *systemTun) interfaceState(ctx context.Context) (string, int, error) {
	if reader, ok := t.identity.(interface {
		InterfaceState(context.Context) (string, int, error)
	}); ok {
		return reader.InterfaceState(ctx)
	}
	name, err := t.device.Name()
	if err != nil {
		return "", 0, err
	}
	mtu, err := t.device.MTU()
	return name, mtu, err
}

func (t *systemTun) Capabilities(ctx context.Context) (protocol.CapabilitySnapshot, error) {
	if err := t.Verify(ctx); err != nil {
		return protocol.CapabilitySnapshot{}, err
	}
	report, err := t.backend.system.CapabilitiesForTun(t.device)
	return protocol.CapabilitySnapshot{SchemaVersion: protocol.Version, Revision: report.InstanceRevision, Operations: operations(report.Operations)}, nativeError(err)
}

func (t *systemTun) Update(ctx context.Context, req protocol.UpdateTunRequest) error {
	if err := t.Verify(ctx); err != nil {
		return err
	}
	report, err := t.Capabilities(ctx)
	if err != nil {
		return &NoEffectsError{Err: err}
	}
	if err := canUpdate(report, req.Kind); err != nil {
		return &NoEffectsError{Err: err}
	}
	switch req.Kind {
	case protocol.TunUpdateMTU:
		err = t.backend.system.SetTunMTU(t.device, int(req.MTU))
	case protocol.TunUpdateAddresses:
		err = t.backend.system.SetTunAddrs(t.device, prefixes(req.Addresses))
	case protocol.TunUpdateRoutes:
		err = t.backend.system.SetTunRoutes(t.device, prefixes(req.Routes))
	case protocol.TunUpdateName:
		// A rename can only target an unused name. Native rename must enforce
		// that same rule atomically if another process wins this race.
		if current, nameErr := t.device.Name(); nameErr == nil && current != req.Name {
			if iface, _ := net.InterfaceByName(req.Name); iface != nil {
				return &NoEffectsError{Err: protocol.ErrInvalidContract}
			}
		}
		err = t.backend.system.SetTunName(t.device, req.Name)
	default:
		return protocol.ErrUnsupportedOperation
	}
	if err != nil {
		return nativeError(err)
	}
	state, err := t.readState(ctx)
	if err != nil {
		return err
	}
	saved := cloneState(state)
	t.expected = &saved
	return nil
}

func capability(c sysnet.Capability) protocol.Capability {
	out := protocol.Capability{State: protocol.CapabilityUnknown, Detail: c.Detail, AttemptAllowed: c.State == sysnet.CapabilityUnknown || c.State == sysnet.CapabilityAvailable}
	switch c.State {
	case sysnet.CapabilityAvailable:
		out.State = protocol.CapabilitySupported
	case sysnet.CapabilityUnavailable:
		out.State = protocol.CapabilityUnavailable
	case sysnet.CapabilityUnsupported:
		out.State = protocol.CapabilityUnsupported
	}
	for _, r := range c.Reasons {
		out.Reasons = append(out.Reasons, string(r))
	}
	for _, l := range c.Limitations {
		out.Limitations = append(out.Limitations, string(l))
	}
	return out
}

func operation(key sysnet.OperationKey) protocol.Operation {
	switch key.Operation {
	case sysnet.OpDialTCP, sysnet.OpDialUDP, sysnet.OpPacketDialUDP, sysnet.OpListenTCP, sysnet.OpListenUDP, sysnet.OpListenPacketUDP, sysnet.OpMulticastUDP:
		return protocol.OperationOpenSocket
	case sysnet.OpInterfaces:
		return protocol.OperationListInterfaces
	case sysnet.OpCreate, sysnet.OpCreateNamed:
		if key.Target == sysnet.TargetDefaultTun {
			return protocol.OperationCreateDefaultTun
		}
		if key.Target == sysnet.TargetTun {
			return protocol.OperationCreateTun
		}
	case sysnet.OpSetMTU:
		return protocol.OperationSetTunMTU
	case sysnet.OpSetAddresses, sysnet.OpAddAddress:
		return protocol.OperationSetTunAddresses
	case sysnet.OpSetRoutes, sysnet.OpAddRoute:
		return protocol.OperationSetTunRoutes
	case sysnet.OpRename:
		return protocol.OperationSetTunName
	case sysnet.OpGetAddresses, sysnet.OpGetRoutes:
		return protocol.OperationGetTunState
	case sysnet.OpReconfigureInPlace, sysnet.OpSourceRoutes:
		return protocol.OperationCreateDefaultTun
	}
	return ""
}

func operations(in []sysnet.OperationCapability) []protocol.OperationCapability {
	out := make([]protocol.OperationCapability, 0, len(in))
	for _, entry := range in {
		op := operation(entry.Key)
		if op == "" {
			continue
		}
		out = append(out, protocol.OperationCapability{Operation: op, NativeOperation: string(entry.Key.Operation), Target: string(entry.Key.Target), Family: string(entry.Key.Family), Capability: capability(entry.Capability)})
	}
	return out
}

func (s *SystemBackend) Capabilities(ctx context.Context) (protocol.CapabilitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return protocol.CapabilitySnapshot{}, err
	}
	r := s.system.Capabilities()
	out := protocol.CapabilitySnapshot{SchemaVersion: r.SchemaVersion, Revision: r.Revision, Operations: operations(r.Operations)}
	resolverCapability := protocol.Capability{State: protocol.CapabilityUnsupported, Reasons: []string{"upstream_snapshot_unavailable"}}
	if s.resolverSource != nil {
		resolverCapability = protocol.Capability{State: protocol.CapabilityUnknown, AttemptAllowed: true}
	}
	out.Operations = append(out.Operations, protocol.OperationCapability{Operation: protocol.OperationResolverSnapshot, Target: "out_dns", Capability: resolverCapability})
	out.Operations = append(out.Operations, protocol.OperationCapability{Operation: protocol.OperationOpenSocket, Target: "out_dns", Capability: protocol.Capability{State: protocol.CapabilityUnknown, AttemptAllowed: true}})
	out.Operations = append(out.Operations, protocol.OperationCapability{Operation: protocol.OperationConfigureDNS, Target: "default_tun", Capability: protocol.Capability{State: protocol.CapabilityUnknown, AttemptAllowed: true}})
	for _, profile := range r.DefaultTunProfiles {
		p := protocol.DefaultTunProfile{Family: string(profile.Key.Family), Mode: string(profile.Key.Mode), Strict: profile.Key.Strict, Capability: capability(profile.Capability)}
		for _, rule := range profile.Rules {
			p.Rules = append(p.Rules, protocol.RuleBinding{Type: rule.Type, Capability: capability(rule.Capability)})
		}
		out.DefaultTunProfiles = append(out.DefaultTunProfiles, p)
	}
	for _, rule := range r.Rules {
		converted := protocol.RuleCapability{Type: rule.Type, Description: rule.Description, ValueKind: string(rule.ValueKind), SemanticsID: rule.SemanticsID, Validation: capability(rule.Validation), Completion: capability(rule.Completion)}
		for _, matcher := range rule.Matchers {
			converted.Matchers = append(converted.Matchers, protocol.MatcherCapability{Family: string(matcher.Key.Family), Protocol: string(matcher.Key.Transport), Quality: string(matcher.Quality), Capability: capability(matcher.Capability)})
		}
		out.Rules = append(out.Rules, converted)
	}
	return out, nil
}
