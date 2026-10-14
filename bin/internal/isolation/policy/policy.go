// Package policy defines normalized daemon-isolation authority. Policy is
// constructed from launch authority and protected settings before restrictions
// are installed. Runtime configuration is never a policy source.
package policy

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

var (
	ErrInvalidPolicy = errors.New("invalid isolation policy")
	ErrNodeCommand   = errors.New("node command is not authorized")
	ErrCallback      = errors.New("backend callback is not authorized")
)

// LaunchAuthority identifies the authority already available to the daemon.
// It is descriptive: policy cannot add credentials or elevate the process.
type LaunchAuthority struct {
	Backend         string
	CredentialClass string
	ProtectedDigest [32]byte
}

// Policy contains mandatory validation limits and optional restrictions.
// NetworkRestrictions nil means that all typed operations supported under the
// launch credentials and protected backend settings remain permitted.
type Policy struct {
	Version             uint32
	Launch              LaunchAuthority
	Limits              Limits
	NetworkRestrictions *NetworkRestrictions
	Commands            CommandAuthority
}

// Limits are availability and parser ceilings, not feature allowlists.
type Limits struct {
	MaxControlFrameBytes  uint32
	MaxControlDepth       uint16
	MaxConcurrentRequests uint16
	MaxArrayItems         uint32
	MaxStringBytes        uint32
	MaxConfigBytes        uint32
	MaxConfigChunkBytes   uint32
	MaxLiveResources      uint32
	MaxOperationReceipts  uint32
}

// NetworkRestrictions can only reduce the default launch authority. These
// fields are not yet connected to protected-config schemas.
type NetworkRestrictions struct {
	DeniedOperations  []protocol.Operation
	DeniedRuleTypes   []string
	MaxTuns           uint32
	MaxSockets        uint32
	MaxListeners      uint32
	AllowDefaultRoute *bool
	AllowHostDNS      *bool
	AllowStrictMode   *bool
}

// CommandAuthority is derived from protected command settings. Node commands
// and broker callbacks use disjoint identifiers and request types.
type CommandAuthority struct {
	NodeCommands []string
	Callbacks    []string
}

func Default(launch LaunchAuthority, commands CommandAuthority) Policy {
	return Policy{
		Version: protocol.Version,
		Launch:  launch,
		Limits: Limits{
			MaxControlFrameBytes:  protocol.MaxControlFrameBytes,
			MaxControlDepth:       protocol.MaxControlDepth,
			MaxConcurrentRequests: protocol.MaxConcurrentRequests,
			MaxArrayItems:         protocol.MaxAddressOrRouteSetItems,
			MaxStringBytes:        protocol.MaxDiagnosticMessageBytes,
			MaxConfigBytes:        protocol.MaxConfigBytes,
			MaxConfigChunkBytes:   protocol.MaxConfigChunkBytes,
			MaxLiveResources:      protocol.MaxLiveResources,
			MaxOperationReceipts:  protocol.MaxOperationReceipts,
		},
		Commands: normalizeCommands(commands),
	}
}

func (p Policy) Validate() error {
	if p.Version != protocol.Version {
		return fmt.Errorf("%w: version %d", ErrInvalidPolicy, p.Version)
	}
	if strings.TrimSpace(p.Launch.Backend) == "" || strings.TrimSpace(p.Launch.CredentialClass) == "" {
		return fmt.Errorf("%w: launch authority is incomplete", ErrInvalidPolicy)
	}
	if p.Limits.MaxControlFrameBytes == 0 || p.Limits.MaxControlFrameBytes > protocol.MaxControlFrameBytes ||
		p.Limits.MaxControlDepth == 0 || p.Limits.MaxControlDepth > protocol.MaxControlDepth ||
		p.Limits.MaxConcurrentRequests == 0 || p.Limits.MaxConcurrentRequests > protocol.MaxConcurrentRequests ||
		p.Limits.MaxArrayItems == 0 || p.Limits.MaxArrayItems > protocol.MaxAddressOrRouteSetItems ||
		p.Limits.MaxStringBytes == 0 || p.Limits.MaxStringBytes > protocol.MaxDiagnosticMessageBytes ||
		p.Limits.MaxConfigBytes == 0 || p.Limits.MaxConfigBytes > protocol.MaxConfigBytes ||
		p.Limits.MaxConfigChunkBytes == 0 || p.Limits.MaxConfigChunkBytes > protocol.MaxConfigChunkBytes ||
		p.Limits.MaxLiveResources == 0 || p.Limits.MaxLiveResources > protocol.MaxLiveResources ||
		p.Limits.MaxOperationReceipts == 0 || p.Limits.MaxOperationReceipts > protocol.MaxOperationReceipts {
		return fmt.Errorf("%w: limit is zero or exceeds a compiled ceiling", ErrInvalidPolicy)
	}
	if err := validateUniqueNames("node command", p.Commands.NodeCommands); err != nil {
		return err
	}
	if err := validateUniqueNames("callback", p.Commands.Callbacks); err != nil {
		return err
	}
	if p.NetworkRestrictions != nil {
		seen := make(map[protocol.Operation]struct{}, len(p.NetworkRestrictions.DeniedOperations))
		for _, operation := range p.NetworkRestrictions.DeniedOperations {
			if !operation.Valid() {
				return fmt.Errorf("%w: unknown denied operation %q", ErrInvalidPolicy, operation)
			}
			if _, exists := seen[operation]; exists {
				return fmt.Errorf("%w: duplicate denied operation %q", ErrInvalidPolicy, operation)
			}
			seen[operation] = struct{}{}
		}
		if err := validateUniqueNames("denied network rule type", p.NetworkRestrictions.DeniedRuleTypes); err != nil {
			return err
		}
	}
	return nil
}

// AllowsNetwork reports only optional policy restriction. The broker must also
// validate the typed operation, ownership, limits, and effective capability.
func (p Policy) AllowsNetwork(operation protocol.Operation) bool {
	if !operation.Valid() {
		return false
	}
	if p.NetworkRestrictions == nil {
		return true
	}
	return !slices.Contains(p.NetworkRestrictions.DeniedOperations, operation)
}

func (p Policy) AuthorizeNodeCommand(commandID string) error {
	if !slices.Contains(p.Commands.NodeCommands, commandID) {
		return ErrNodeCommand
	}
	return nil
}

func (p Policy) AuthorizeCallback(callbackID string) error {
	if !slices.Contains(p.Commands.Callbacks, callbackID) {
		return ErrCallback
	}
	return nil
}

// Digest returns a stable consistency digest of normalized policy. It is not a
// credential and does not prove that a runtime enforces the policy.
func (p Policy) Digest() ([32]byte, error) {
	if err := p.Validate(); err != nil {
		return [32]byte{}, err
	}
	normalized := p
	normalized.Commands = normalizeCommands(p.Commands)
	if normalized.NetworkRestrictions != nil {
		copyRestrictions := *normalized.NetworkRestrictions
		copyRestrictions.DeniedOperations = slices.Clone(copyRestrictions.DeniedOperations)
		slices.Sort(copyRestrictions.DeniedOperations)
		copyRestrictions.DeniedRuleTypes = slices.Clone(copyRestrictions.DeniedRuleTypes)
		slices.Sort(copyRestrictions.DeniedRuleTypes)
		normalized.NetworkRestrictions = &copyRestrictions
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func normalizeCommands(commands CommandAuthority) CommandAuthority {
	commands.NodeCommands = slices.Clone(commands.NodeCommands)
	commands.Callbacks = slices.Clone(commands.Callbacks)
	slices.Sort(commands.NodeCommands)
	slices.Sort(commands.Callbacks)
	return commands
}

func validateUniqueNames(kind string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: empty %s ID", ErrInvalidPolicy, kind)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("%w: duplicate %s ID %q", ErrInvalidPolicy, kind, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
