package runtime

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
)

const systemOperationTimeout = 5 * time.Second

// System is the complete sysnet adapter for an isolated network Client. It
// exposes no native file, socket, process, or platform handle to R.
type System struct {
	client *Client
	dns    *lazyDNS
	out    gonnect.Network
	local  gonnect.Network

	buildMu   sync.Mutex
	mu        sync.Mutex
	closed    bool
	tuns      map[*SystemTun]struct{}
	def       *SystemTun
	providers map[*lazyDNS]struct{}
}

// System returns one complete sysnet adapter. The returned object owns Client.
func (c *Client) System() *System {
	s := &System{client: c, tuns: make(map[*SystemTun]struct{}), providers: make(map[*lazyDNS]struct{})}
	s.dns = newLazyDNS(c, s, protocol.SocketResolver, protocol.NetworkOutbound)
	outDNS := newLazyDNS(c, s, protocol.SocketNetworkResolver, protocol.NetworkOutbound)
	localDNS := newLazyDNS(c, s, protocol.SocketNetworkResolver, protocol.NetworkLocal)
	s.providers[s.dns] = struct{}{}
	s.providers[outDNS] = struct{}{}
	s.providers[localDNS] = struct{}{}
	s.out = c.Network(protocol.NetworkOutbound, newDNSResolver(outDNS))
	s.local = c.Network(protocol.NetworkLocal, newDNSResolver(localDNS))
	return s
}

func (s *System) Close() error {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	devices := make([]*SystemTun, 0, len(s.tuns))
	for device := range s.tuns {
		devices = append(devices, device)
	}
	providers := make([]*lazyDNS, 0, len(s.providers))
	for provider := range s.providers {
		providers = append(providers, provider)
	}
	s.mu.Unlock()
	var err error
	for _, device := range devices {
		err = errors.Join(err, device.Close())
	}
	for _, provider := range providers {
		err = errors.Join(err, provider.Close())
	}
	err = errors.Join(err, s.client.Close())
	return err
}

func (s *System) Capabilities() sysnet.CapabilityReport {
	var snapshot protocol.CapabilitySnapshot
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	if err := s.client.peer.Call(ctx, protocol.OperationCapabilities, protocol.ReadRequest{Meta: s.client.meta(false)}, &snapshot); err != nil {
		return unavailableCapabilities(err)
	}
	report, err := capabilityReport(snapshot)
	if err != nil {
		return unavailableCapabilities(err)
	}
	return report
}

func unavailableCapabilities(err error) sysnet.CapabilityReport {
	return sysnet.CapabilityReport{SchemaVersion: sysnet.CapabilitySchemaVersion, Operations: []sysnet.OperationCapability{{
		Key:        sysnet.OperationKey{Target: sysnet.TargetSystem, Operation: sysnet.OpCreate, Family: sysnet.FamilyNone},
		Capability: sysnet.Capability{State: sysnet.CapabilityUnavailable, Reasons: []sysnet.CapabilityReason{sysnet.ReasonProbeFailed}, Detail: err.Error()},
	}}}
}

func nativeCapability(value protocol.Capability) (sysnet.Capability, error) {
	result := sysnet.Capability{Detail: value.Detail}
	switch value.State {
	case protocol.CapabilityUnknown:
		result.State = sysnet.CapabilityUnknown
	case protocol.CapabilitySupported:
		result.State = sysnet.CapabilityAvailable
	case protocol.CapabilityUnavailable:
		result.State = sysnet.CapabilityUnavailable
	case protocol.CapabilityUnsupported:
		result.State = sysnet.CapabilityUnsupported
	default:
		return result, protocol.ErrInvalidContract
	}
	for _, reason := range value.Reasons {
		if reason == "" {
			return result, protocol.ErrInvalidContract
		}
		result.Reasons = append(result.Reasons, sysnet.CapabilityReason(reason))
	}
	for _, limitation := range value.Limitations {
		if limitation == "" {
			return result, protocol.ErrInvalidContract
		}
		result.Limitations = append(result.Limitations, sysnet.LimitationID(limitation))
	}
	return result, nil
}

func nativeOperation(value protocol.OperationCapability) (sysnet.OperationCapability, error) {
	op := value.NativeOperation
	if op == "" {
		switch value.Operation {
		case protocol.OperationReserveIP:
			op = string(sysnet.OpAllocateIP)
		case protocol.OperationReserveSubnet:
			op = string(sysnet.OpAllocateSubnet)
		case protocol.OperationCreateTun, protocol.OperationCreateDefaultTun:
			op = string(sysnet.OpCreate)
		case protocol.OperationSetTunMTU:
			op = string(sysnet.OpSetMTU)
		case protocol.OperationSetTunAddresses:
			op = string(sysnet.OpSetAddresses)
		case protocol.OperationSetTunRoutes:
			op = string(sysnet.OpSetRoutes)
		case protocol.OperationSetTunName:
			op = string(sysnet.OpRename)
		case protocol.OperationGetTunState:
			op = string(sysnet.OpGetAddresses)
		case protocol.OperationConfigureDNS:
			op = string(sysnet.OpDNSConfigure)
		case protocol.OperationResolverSnapshot:
			op = string(sysnet.OpDNSProvider)
		case protocol.OperationOpenSocket:
			op = string(sysnet.OpQueryUDP)
		default:
			return sysnet.OperationCapability{}, protocol.ErrInvalidContract
		}
	}
	target, family := value.Target, value.Family
	if target == "" {
		switch value.Operation {
		case protocol.OperationCreateTun:
			target = string(sysnet.TargetTun)
		case protocol.OperationCreateDefaultTun, protocol.OperationConfigureDNS:
			target = string(sysnet.TargetDefaultTun)
		default:
			target = string(sysnet.TargetSystem)
		}
	}
	if family == "" {
		family = string(sysnet.FamilyNone)
	}
	capability, err := nativeCapability(value.Capability)
	return sysnet.OperationCapability{Key: sysnet.OperationKey{Target: sysnet.Target(target), Operation: sysnet.Operation(op), Family: sysnet.AddressFamily(family)}, Capability: capability}, err
}

func capabilityReport(snapshot protocol.CapabilitySnapshot) (sysnet.CapabilityReport, error) {
	result := sysnet.CapabilityReport{SchemaVersion: snapshot.SchemaVersion, Revision: snapshot.Revision}
	for _, value := range snapshot.Operations {
		if value.Operation == protocol.OperationResolverSnapshot || value.Operation == protocol.OperationCreateDNSEndpoint {
			continue
		}
		converted, err := nativeOperation(value)
		if err != nil {
			return sysnet.CapabilityReport{}, err
		}
		result.Operations = append(result.Operations, converted)
	}
	for _, value := range snapshot.Rules {
		validation, err := nativeCapability(value.Validation)
		if err != nil {
			return sysnet.CapabilityReport{}, err
		}
		completion, err := nativeCapability(value.Completion)
		if err != nil {
			return sysnet.CapabilityReport{}, err
		}
		rule := sysnet.RuleCapability{Type: value.Type, Description: value.Description, ValueKind: sysnet.RuleValueKind(value.ValueKind), SemanticsID: value.SemanticsID, Validation: validation, Completion: completion}
		for _, matcher := range value.Matchers {
			capability, err := nativeCapability(matcher.Capability)
			if err != nil {
				return sysnet.CapabilityReport{}, err
			}
			rule.Matchers = append(rule.Matchers, sysnet.MatcherProfile{Key: sysnet.MatcherProfileKey{Family: sysnet.AddressFamily(matcher.Family), Transport: sysnet.Transport(matcher.Protocol)}, Quality: sysnet.MatchQuality(matcher.Quality), Capability: capability})
		}
		result.Rules = append(result.Rules, rule)
	}
	for _, value := range snapshot.DefaultTunProfiles {
		capability, err := nativeCapability(value.Capability)
		if err != nil {
			return sysnet.CapabilityReport{}, err
		}
		profile := sysnet.DefaultTunProfile{Key: sysnet.RoutingProfileKey{Family: sysnet.AddressFamily(value.Family), Mode: sysnet.RoutingMode(value.Mode), Strict: value.Strict}, Capability: capability}
		for _, binding := range value.Rules {
			capability, err := nativeCapability(binding.Capability)
			if err != nil {
				return sysnet.CapabilityReport{}, err
			}
			profile.Rules = append(profile.Rules, sysnet.RuleBinding{Type: binding.Type, Capability: capability})
		}
		result.DefaultTunProfiles = append(result.DefaultTunProfiles, profile)
	}
	for _, value := range snapshot.Ownership {
		capability, err := nativeCapability(value.Capability)
		if err != nil {
			return sysnet.CapabilityReport{}, err
		}
		owner := sysnet.OwnerCapability{Key: sysnet.MatcherProfileKey{Family: sysnet.AddressFamily(value.Family), Transport: sysnet.Transport(value.Protocol)}, Quality: sysnet.MatchQuality(value.Quality), Capability: capability}
		for _, field := range value.Fields {
			capability, err := nativeCapability(field.Capability)
			if err != nil {
				return sysnet.CapabilityReport{}, err
			}
			owner.Fields = append(owner.Fields, sysnet.OwnerFieldCapability{Field: sysnet.OwnerField(field.Field), Capability: capability})
		}
		result.Ownership = append(result.Ownership, owner)
	}
	if err := sysnet.ValidateCapabilityReport(result); err != nil {
		return sysnet.CapabilityReport{}, errors.Join(protocol.ErrInvalidContract, err)
	}
	return result, nil
}

func validationFailure(path string, err error) sysnet.ValidationReport {
	state, cause := sysnet.CapabilityUnavailable, err
	switch {
	case errors.Is(err, protocol.ErrInvalidContract):
		state, cause = sysnet.CapabilityAvailable, errors.Join(sysnet.ErrInvalidOptions, err)
	case errors.Is(err, protocol.ErrCapabilityUnsupported):
		state, cause = sysnet.CapabilityUnsupported, errors.Join(sysnet.ErrNotSupported, err)
	case errors.Is(err, protocol.ErrCapabilityUnknown):
		state, cause = sysnet.CapabilityUnknown, errors.Join(sysnet.ErrCapabilityUnknown, err)
	}
	return sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{{Path: path, State: state, Err: cause}}}
}

func tunOptions(value sysnet.TunOpts) (protocol.TunOptions, error) {
	if value.MTU < 0 || uint64(value.MTU) > math.MaxUint32 {
		return protocol.TunOptions{}, protocol.ErrInvalidContract
	}
	addresses, err := parseNativePrefixes(value.TunAddrs)
	if err != nil {
		return protocol.TunOptions{}, err
	}
	routes, err := parseNativePrefixes(value.TunRoutes)
	if err != nil {
		return protocol.TunOptions{}, err
	}
	return protocol.TunOptions{Name: value.Name, MTU: uint32(value.MTU), Addresses: addresses, Routes: routes}, nil
}

func defaultTunRequest(value sysnet.DefaultTunOpts) (protocol.CreateTunRequest, error) {
	regular, err := tunOptions(sysnet.TunOpts{Name: value.Name, MTU: value.MTU, TunAddrs: value.TunAddrs, TunRoutes: value.TunRoutes})
	if err != nil {
		return protocol.CreateTunRequest{}, err
	}
	request := protocol.CreateTunRequest{Default: true, Options: regular, Strict: value.Strict}
	if value.DnsIP != "" {
		request.DNSIP, err = netip.ParseAddr(value.DnsIP)
		if err != nil {
			return protocol.CreateTunRequest{}, protocol.ErrInvalidContract
		}
	}
	for _, value := range value.SourceRoutes {
		if !value.Source.IsValid() || !value.Destination.IsValid() {
			return protocol.CreateTunRequest{}, protocol.ErrInvalidContract
		}
		request.Options.SourceRoutes = append(request.Options.SourceRoutes, protocol.SourceRoute{Source: netip.PrefixFrom(value.Source, value.Source.BitLen()), Route: value.Destination})
	}
	if len(value.Include) > 0 && len(value.Exclude) > 0 {
		return protocol.CreateTunRequest{}, protocol.ErrInvalidContract
	}
	selected := value.Exclude
	if len(value.Include) > 0 {
		request.RuleMode, selected = "include", value.Include
	} else if len(value.Exclude) > 0 {
		request.RuleMode = "exclude"
	}
	for _, rule := range selected {
		request.Rules = append(request.Rules, protocol.Rule{Type: rule.Type, Value: rule.Rule})
	}
	return request, nil
}

func parseNativePrefixes(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return nil, protocol.ErrInvalidContract
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		result = append(result, prefix)
	}
	return result, nil
}

func (s *System) check(op protocol.Operation, request any) sysnet.ValidationReport {
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	var result protocol.ValidationResult
	if err := s.client.peer.Call(ctx, op, request, &result); err != nil {
		return validationFailure("Options", err)
	}
	return ruleValidationReport(result)
}

func (s *System) CheckTunOpts(value sysnet.TunOpts) sysnet.ValidationReport {
	options, err := tunOptions(value)
	if err != nil {
		return validationFailure("Options", err)
	}
	return s.check(protocol.OperationCheckTun, protocol.CheckTunRequest{Meta: s.client.meta(false), Options: options})
}

func (s *System) CheckDefaultTunOpts(value sysnet.DefaultTunOpts) sysnet.ValidationReport {
	request, err := defaultTunRequest(value)
	if err != nil {
		return validationFailure("Options", err)
	}
	return s.check(protocol.OperationCheckDefaultTun, protocol.CheckDefaultTunRequest{Meta: s.client.meta(false), Options: request.Options, Strict: request.Strict, Rules: request.Rules, RuleMode: request.RuleMode, DNSIP: request.DNSIP})
}

func (s *System) CheckRule(rule sysnet.Rule, use sysnet.RuleContext) sysnet.ValidationReport {
	return s.client.CheckRule(rule, use)
}
func (s *System) CompleteRule(rule sysnet.Rule, use sysnet.RuleContext) ([]string, error) {
	return s.client.CompleteRule(rule, use)
}
func (s *System) AllocIP() subnet.IPAllocator {
	return s.client.Allocator()
}
func (s *System) AllocSubnet() subnet.SubnetAllocator {
	return s.client.Allocator()
}
func (s *System) OutDNS() gdns.Interface {
	provider := newLazyDNS(s.client, s, protocol.SocketResolver, protocol.NetworkOutbound)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = provider.Close()
		return provider
	}
	s.providers[provider] = struct{}{}
	s.mu.Unlock()
	return provider
}
func (s *System) OutNet() gonnect.Network   { return s.out }
func (s *System) LocalNet() gonnect.Network { return s.local }
func (s *System) BuildMatcher(rule sysnet.Rule) (sysnet.Matcher, error) {
	return s.client.BuildMatcher(rule)
}

func (s *System) BuildTun(value sysnet.TunOpts) (tun.Tun, error) {
	options, err := tunOptions(value)
	if err != nil {
		return nil, systemError(err)
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, sysnet.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	device, err := s.client.CreateTun(ctx, protocol.CreateTunRequest{Options: options})
	if err != nil {
		return nil, systemError(err)
	}
	return s.track(device, false), nil
}

func (s *System) BuildDefaultTun(value sysnet.DefaultTunOpts) (sysnet.DefaultTun, error) {
	request, err := defaultTunRequest(value)
	if err != nil {
		return nil, systemError(err)
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, sysnet.ErrUnavailable
	}
	stable := s.def
	s.mu.Unlock()
	if stable != nil && stable.beginReplacement() {
		stable.mu.Lock()
		service := stable.service
		stable.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
		device, createErr := s.client.CreateTun(ctx, request)
		cancel()
		if createErr == nil {
			previous := stable.install(device, service)
			if previous != nil && previous != service {
				_ = previous.Close()
			}
		}
		stable.endReplacement()
		if createErr != nil {
			return nil, systemError(createErr)
		}
		return stable, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	device, err := s.client.CreateTun(ctx, request)
	if err != nil {
		return nil, systemError(err)
	}
	stable = s.track(device, true)
	s.mu.Lock()
	s.def = stable
	s.mu.Unlock()
	return stable, nil
}

func (s *System) track(source *Tun, defaultTun bool) *SystemTun {
	generation := uint64(1)
	if nativeGeneration := source.sourceGeneration(); nativeGeneration != 0 {
		generation = nativeGeneration
	}
	device := &SystemTun{system: s, current: source, sourceGen: generation, defaultTun: defaultTun, events: make(chan tun.Event, 128)}
	s.mu.Lock()
	s.tuns[device] = struct{}{}
	s.mu.Unlock()
	go device.forward(source)
	return device
}

func (s *System) owned(value tun.Tun) (*SystemTun, error) {
	device, ok := value.(*SystemTun)
	if !ok || device.system != s || device.source() == nil {
		return nil, sysnet.ErrUnknownTun
	}
	return device, nil
}

func (s *System) state(value tun.Tun) (protocol.ResourceState, error) {
	device, err := s.owned(value)
	if err != nil {
		return protocol.ResourceState{}, err
	}
	state, err := device.readState()
	return state, systemTunError(err)
}

func (s *System) CapabilitiesForTun(value tun.Tun) (sysnet.TunCapabilityReport, error) {
	state, err := s.state(value)
	if err != nil {
		return sysnet.TunCapabilityReport{}, err
	}
	report := sysnet.TunCapabilityReport{SystemRevision: state.SystemCapabilityRevision, InstanceRevision: state.CapabilityRevision}
	for _, value := range state.Capabilities {
		converted, err := nativeOperation(value)
		if err != nil {
			return sysnet.TunCapabilityReport{}, systemError(err)
		}
		report.Operations = append(report.Operations, converted)
	}
	if err := sysnet.ValidateTunCapabilityReport(report); err != nil {
		return sysnet.TunCapabilityReport{}, errors.Join(sysnet.ErrUnavailable, err)
	}
	return report, nil
}

func (s *System) warnings(value tun.Tun, defaultTun bool) []sysnet.Warning {
	device, err := s.owned(value)
	if err != nil || device.defaultTun != defaultTun {
		return nil
	}
	state, err := s.state(value)
	if err != nil || state.Closed || state.RecoveryRequired {
		return nil
	}
	result := make([]sysnet.Warning, len(state.Warnings))
	for i, warning := range state.Warnings {
		result[i] = sysnet.Warning(warning)
	}
	return result
}
func (s *System) DefaultTunWarnings(value sysnet.DefaultTun) []sysnet.Warning {
	return s.warnings(value, true)
}
func (s *System) TunWarnings(value tun.Tun) []sysnet.Warning { return s.warnings(value, false) }

func (s *System) update(value tun.Tun, request protocol.UpdateTunRequest) error {
	device, err := s.owned(value)
	if err != nil {
		return err
	}
	return device.update(request)
}
func (s *System) SetTunMTU(value tun.Tun, mtu int) error {
	if mtu < 0 || uint64(mtu) > math.MaxUint32 {
		return sysnet.ErrInvalidOptions
	}
	return s.update(value, protocol.UpdateTunRequest{Kind: protocol.TunUpdateMTU, MTU: uint32(mtu)})
}
func (s *System) SetTunAddrs(value tun.Tun, addresses []string) error {
	parsed, err := parseNativePrefixes(addresses)
	if err != nil {
		return sysnet.ErrInvalidOptions
	}
	return s.update(value, protocol.UpdateTunRequest{Kind: protocol.TunUpdateAddresses, Addresses: parsed})
}
func (s *System) AddTunAddr(value tun.Tun, address string) error {
	device, err := s.owned(value)
	if err != nil {
		return err
	}
	prefixes, err := parseNativePrefixes([]string{address})
	if err != nil {
		return sysnet.ErrInvalidOptions
	}
	return device.appendPrefix(protocol.TunUpdateAddresses, prefixes[0])
}
func (s *System) GetTunAddrs(value tun.Tun) ([]string, error) {
	state, err := s.state(value)
	if err != nil {
		return nil, err
	}
	return prefixStrings(state.Addresses), nil
}
func (s *System) SetTunRoutes(value tun.Tun, routes []string) error {
	parsed, err := parseNativePrefixes(routes)
	if err != nil {
		return sysnet.ErrInvalidOptions
	}
	return s.update(value, protocol.UpdateTunRequest{Kind: protocol.TunUpdateRoutes, Routes: parsed})
}
func (s *System) AddTunRoute(value tun.Tun, route string) error {
	device, err := s.owned(value)
	if err != nil {
		return err
	}
	prefixes, err := parseNativePrefixes([]string{route})
	if err != nil {
		return sysnet.ErrInvalidOptions
	}
	return device.appendPrefix(protocol.TunUpdateRoutes, prefixes[0])
}
func (s *System) GetTunRoutes(value tun.Tun) ([]string, error) {
	state, err := s.state(value)
	if err != nil {
		return nil, err
	}
	return prefixStrings(state.Routes), nil
}
func (s *System) SetTunName(value tun.Tun, name string) error {
	return s.update(value, protocol.UpdateTunRequest{Kind: protocol.TunUpdateName, Name: name})
}

func prefixStrings(values []netip.Prefix) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.String()
	}
	return result
}

func systemError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, protocol.ErrInvalidContract):
		return errors.Join(sysnet.ErrInvalidOptions, err)
	case errors.Is(err, protocol.ErrCapabilityUnsupported):
		return errors.Join(sysnet.ErrNotSupported, err)
	case errors.Is(err, protocol.ErrCapabilityUnavailable), errors.Is(err, protocol.ErrPolicyDenied):
		return errors.Join(sysnet.ErrUnavailable, err)
	default:
		return err
	}
}

func systemTunError(err error) error {
	if errors.Is(err, protocol.ErrForeignResource) || errors.Is(err, protocol.ErrResourceType) || errors.Is(err, protocol.ErrStaleGeneration) || errors.Is(err, protocol.ErrStaleGraphEpoch) {
		return errors.Join(sysnet.ErrUnknownTun, err)
	}
	return systemError(err)
}

// SystemTun keeps one public TUN identity while broker generations change.
type SystemTun struct {
	system     *System
	defaultTun bool
	events     chan tun.Event

	mutationMu sync.Mutex
	mu         sync.Mutex
	current    *Tun
	service    *DNSService
	sourceGen  uint64
	closed     bool
	replacing  bool
	replaced   chan struct{}
	forwardWG  sync.WaitGroup
	eventsDone bool
	closeOnce  sync.Once
	closeErr   error
}

func (t *SystemTun) source() *Tun {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	return t.current
}
func (t *SystemTun) beginReplacement() bool {
	t.mutationMu.Lock()
	t.mu.Lock()
	if t.closed || t.current == nil {
		t.mu.Unlock()
		t.mutationMu.Unlock()
		return false
	}
	t.replacing = true
	t.replaced = make(chan struct{})
	t.mu.Unlock()
	return true
}
func (t *SystemTun) endReplacement() {
	t.mu.Lock()
	t.replacing = false
	close(t.replaced)
	t.mu.Unlock()
	t.mutationMu.Unlock()
}
func (t *SystemTun) install(source *Tun, service *DNSService) *DNSService {
	t.mu.Lock()
	oldSource, oldService := t.current, t.service
	t.current, t.service = source, service
	if source != nil {
		if nativeGeneration := source.sourceGeneration(); nativeGeneration != 0 {
			t.sourceGen = nativeGeneration
		} else if oldSource != nil && oldSource.ref.ID != source.ref.ID {
			t.sourceGen++
		}
	}
	t.mu.Unlock()
	if source != oldSource {
		t.forwardWG.Go(func() { t.forward(source) })
	}
	return oldService
}
func (t *SystemTun) forward(source *Tun) {
	for event := range source.Events() {
		t.mu.Lock()
		active := !t.closed && t.current == source
		t.mu.Unlock()
		if !active {
			continue
		}
		select {
		case t.events <- event:
		default:
			go func() { _ = t.system.client.Close() }()
			return
		}
	}
	t.mu.Lock()
	if t.replacing && t.current == source {
		done := t.replaced
		t.mu.Unlock()
		<-done
		t.mu.Lock()
	}
	if !t.closed && t.current == source {
		t.closed = true
		close(t.events)
		t.eventsDone = true
	}
	t.mu.Unlock()
}

func (t *SystemTun) update(request protocol.UpdateTunRequest) error {
	if !t.beginReplacement() {
		return sysnet.ErrUnknownTun
	}
	defer t.endReplacement()
	old := t.sourceDuringReplacement()
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	next, err := t.system.client.updateTun(ctx, old, request)
	if err != nil {
		return systemTunError(err)
	}
	t.install(next, t.service)
	return nil
}

func (t *SystemTun) readState() (protocol.ResourceState, error) {
	t.mutationMu.Lock()
	defer t.mutationMu.Unlock()
	source := t.sourceDuringReplacement()
	if source == nil {
		return protocol.ResourceState{}, sysnet.ErrUnknownTun
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	return t.system.client.tunState(ctx, source)
}

func (t *SystemTun) appendPrefix(kind protocol.TunUpdateKind, prefix netip.Prefix) error {
	if !t.beginReplacement() {
		return sysnet.ErrUnknownTun
	}
	defer t.endReplacement()
	old := t.sourceDuringReplacement()
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	state, err := t.system.client.tunState(ctx, old)
	if err != nil {
		return systemTunError(err)
	}
	request := protocol.UpdateTunRequest{Kind: kind}
	if kind == protocol.TunUpdateAddresses {
		request.Addresses = append(state.Addresses, prefix)
	} else {
		request.Routes = append(state.Routes, prefix)
	}
	next, err := t.system.client.updateTun(ctx, old, request)
	if err != nil {
		return systemTunError(err)
	}
	t.install(next, t.service)
	return nil
}
func (t *SystemTun) sourceDuringReplacement() *Tun {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

// replacementAfter waits for an in-progress generation change. Broker events
// can close the old RPC TUN before the update reply installs its replacement.
// Keep this race inside the stable SystemTun identity.
func (t *SystemTun) replacementAfter(source *Tun) *Tun {
	t.mu.Lock()
	if t.replacing && t.current == source {
		done := t.replaced
		t.mu.Unlock()
		<-done
		t.mu.Lock()
	}
	defer t.mu.Unlock()
	if t.closed || t.current == source || t.current == nil || source == nil || t.current.ref.ID != source.ref.ID {
		return nil
	}
	return t.current
}

func (t *SystemTun) Read(buffers [][]byte, sizes []int, offset int) (int, error) {
	for source := t.source(); source != nil; {
		n, err := source.Read(buffers, sizes, offset)
		if n != 0 || !errors.Is(err, net.ErrClosed) {
			return n, err
		}
		next := t.replacementAfter(source)
		if next == nil {
			return 0, net.ErrClosed
		}
		// Conn.Read can return promptly when a generation event closes it while
		// its bounded RPC worker is still unwinding. Wait for that one reader
		// before the new generation uses the same broker resource ID.
		source.waitReadIdle()
		source = next
	}
	return 0, net.ErrClosed
}
func (t *SystemTun) Write(buffers [][]byte, offset int) (int, error) {
	if source := t.source(); source != nil {
		return source.Write(buffers, offset)
	}
	return 0, net.ErrClosed
}
func (t *SystemTun) MTU() (int, error) {
	if source := t.source(); source != nil {
		return source.MTU()
	}
	return 0, net.ErrClosed
}
func (t *SystemTun) Name() (string, error) {
	if source := t.source(); source != nil {
		return source.Name()
	}
	return "", net.ErrClosed
}
func (t *SystemTun) Events() <-chan tun.Event { return t.events }
func (*SystemTun) File() *os.File             { return nil }
func (*SystemTun) IsNative() bool             { return true }
func (t *SystemTun) BatchSize() int {
	if source := t.source(); source != nil {
		return source.BatchSize()
	}
	return 1
}
func (*SystemTun) MWO() int { return 0 }
func (*SystemTun) MRO() int { return 0 }
func (t *SystemTun) SourceGeneration() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sourceGen
}

func (t *SystemTun) Close() error {
	t.closeOnce.Do(func() {
		t.mutationMu.Lock()
		defer t.mutationMu.Unlock()
		t.mu.Lock()
		t.closed = true
		source, service := t.current, t.service
		t.current, t.service = nil, nil
		t.mu.Unlock()
		if service != nil {
			t.closeErr = errors.Join(t.closeErr, service.Close())
		}
		if source != nil {
			t.closeErr = errors.Join(t.closeErr, source.Close())
		}
		t.forwardWG.Wait()
		t.mu.Lock()
		if !t.eventsDone {
			close(t.events)
			t.eventsDone = true
		}
		t.mu.Unlock()
		t.system.mu.Lock()
		delete(t.system.tuns, t)
		if t.system.def == t {
			t.system.def = nil
		}
		t.system.mu.Unlock()
	})
	return t.closeErr
}

func (t *SystemTun) SetDNS(upstream gdns.Interface) error {
	if !t.defaultTun {
		return sysnet.ErrUnknownTun
	}
	if !t.beginReplacement() {
		return sysnet.ErrUnknownTun
	}
	defer t.endReplacement()
	old := t.sourceDuringReplacement()
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	var service *DNSService
	var endpoint protocol.ResourceRef
	var err error
	if upstream != nil {
		service, endpoint, err = t.system.client.createDNSEndpoint(ctx, upstream)
		if err != nil {
			return systemError(err)
		}
	}
	next, err := t.system.client.ConfigureDNS(ctx, old, endpoint, upstream != nil)
	if err != nil {
		if service != nil {
			_ = service.Close()
		}
		return systemTunError(err)
	}
	previous := t.install(next, service)
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

type lazyDNS struct {
	client   *Client
	owner    *System
	kind     protocol.SocketKind
	purpose  protocol.NetworkPurpose
	requests chan gdns.Request
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	provider gdns.Interface
	closed   bool
}

func newLazyDNS(client *Client, owner *System, kind protocol.SocketKind, purpose protocol.NetworkPurpose) *lazyDNS {
	ctx, cancel := context.WithCancel(context.Background())
	d := &lazyDNS{client: client, owner: owner, kind: kind, purpose: purpose, requests: make(chan gdns.Request), ctx: ctx, cancel: cancel}
	for range 4 {
		d.wg.Go(d.run)
	}
	return d
}
func (d *lazyDNS) Requests() chan<- gdns.Request { return d.requests }
func (d *lazyDNS) get(ctx context.Context) (gdns.Interface, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, net.ErrClosed
	}
	if d.provider == nil {
		var provider gdns.Interface
		var err error
		if d.kind == protocol.SocketNetworkResolver {
			provider, err = d.client.NetworkDNS(ctx, d.purpose)
		} else {
			provider, err = d.client.OutDNS(ctx)
		}
		if err != nil {
			return nil, err
		}
		d.provider = provider
	}
	return d.provider, nil
}
func (d *lazyDNS) invalidate(provider gdns.Interface) {
	d.mu.Lock()
	if d.provider == provider {
		d.provider = nil
		_ = provider.Close()
	}
	d.mu.Unlock()
}
func (d *lazyDNS) run() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case request := <-d.requests:
			ctx := request.Context
			if ctx == nil {
				ctx = context.Background()
			}
			query, cancel := context.WithTimeout(ctx, systemOperationTimeout)
			provider, err := d.get(query)
			var response *gdns.Message
			if err == nil {
				response, err = gdns.Query(query, provider, request.Message)
			}
			cancel()
			if err != nil && provider != nil {
				d.invalidate(provider)
			}
			select {
			case request.Reply <- gdns.Response{Message: response, Err: err}:
			case <-ctx.Done():
			case <-d.ctx.Done():
			}
		}
	}
}
func (d *lazyDNS) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	provider := d.provider
	d.provider = nil
	d.mu.Unlock()
	d.cancel()
	d.wg.Wait()
	if d.owner != nil {
		d.owner.mu.Lock()
		delete(d.owner.providers, d)
		d.owner.mu.Unlock()
	}
	if provider != nil {
		return provider.Close()
	}
	return nil
}

var _ sysnet.System = (*System)(nil)
var _ sysnet.DefaultTun = (*SystemTun)(nil)
