package tunnodes

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
)

func capabilityAllowsAttempt(capability sysnet.Capability) bool {
	return capability.State == sysnet.CapabilityAvailable ||
		capability.State == sysnet.CapabilityUnknown
}

func validationErrorForAttempt(report sysnet.ValidationReport) error {
	var errs []error
	for _, issue := range report.Issues {
		err := (sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{issue}}).Err()
		if errors.Is(err, sysnet.ErrCapabilityUnknown) &&
			!errors.Is(err, sysnet.ErrInvalidOptions) {
			continue
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func optionAddressFamily(addrs, routes []string, defaultFamily sysnet.AddressFamily) sysnet.AddressFamily {
	has4, has6 := false, false
	for _, values := range [][]string{addrs, routes} {
		for _, value := range values {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil {
				continue
			}
			if prefix.Addr().Is4() {
				has4 = true
			} else {
				has6 = true
			}
		}
	}
	switch {
	case has4 && has6:
		return sysnet.FamilyDual
	case has4:
		return sysnet.FamilyIPv4
	case has6:
		return sysnet.FamilyIPv6
	default:
		return defaultFamily
	}
}

func tunOptsFamily(opts sysnet.TunOpts) sysnet.AddressFamily {
	return optionAddressFamily(opts.TunAddrs, opts.TunRoutes, sysnet.FamilyNone)
}

func defaultTunOptsFamily(opts sysnet.DefaultTunOpts) sysnet.AddressFamily {
	return optionAddressFamily(opts.TunAddrs, opts.TunRoutes, sysnet.FamilyIPv4)
}

func systemCanCreate(report sysnet.CapabilityReport, target sysnet.Target) bool {
	for _, operation := range report.Operations {
		if operation.Key.Target == target &&
			operation.Key.Operation == sysnet.OpCreate &&
			capabilityAllowsAttempt(operation.Capability) {
			return true
		}
	}
	return false
}

func systemOperation(
	report sysnet.CapabilityReport,
	target sysnet.Target,
	operation sysnet.Operation,
	family sysnet.AddressFamily,
) sysnet.Capability {
	return report.Operation(sysnet.OperationKey{
		Target: target, Operation: operation, Family: family,
	})
}

func tunOperationAllowsAttempt(
	system sysnet.System,
	t tun.Tun,
	target sysnet.Target,
	operation sysnet.Operation,
	family sysnet.AddressFamily,
) bool {
	if system == nil || t == nil {
		return false
	}
	report, err := system.CapabilitiesForTun(t)
	if err != nil {
		return false
	}
	return capabilityAllowsAttempt(report.Operation(sysnet.OperationKey{
		Target: target, Operation: operation, Family: family,
	}))
}

func capabilityText(capability sysnet.Capability) string {
	state := "availability is not established"
	switch capability.State {
	case sysnet.CapabilityUnsupported:
		state = "not supported"
	case sysnet.CapabilityUnavailable:
		state = "not available on this system"
	case sysnet.CapabilityAvailable:
		return ""
	}
	parts := make([]string, 0, len(capability.Reasons))
	for _, reason := range capability.Reasons {
		parts = append(parts, strings.ReplaceAll(string(reason), "_", " "))
	}
	if capability.Detail != "" {
		parts = append(parts, capability.Detail)
	}
	if len(parts) == 0 {
		return state
	}
	return fmt.Sprintf("%s: %s", state, strings.Join(parts, ", "))
}

func applyCapabilityToMenuItem(item formular.Item, capability sysnet.Capability) formular.Item {
	if capability.State == sysnet.CapabilityUnsupported ||
		capability.State == sysnet.CapabilityUnavailable {
		if item.Field != nil {
			item.Readonly = true
		}
	}
	if help := capabilityText(capability); help != "" {
		item.Help = help
	}
	return item
}

func availableRuleCapabilities(
	report sysnet.CapabilityReport,
	profile sysnet.DefaultTunProfile,
) []sysnet.RuleCapability {
	result := make([]sysnet.RuleCapability, 0, len(profile.Rules))
	for _, binding := range profile.Rules {
		if !capabilityAllowsAttempt(binding.Capability) {
			continue
		}
		rule := report.Rule(binding.Type)
		if capabilityAllowsAttempt(rule.Validation) {
			result = append(result, rule)
		}
	}
	slices.SortFunc(result, func(a, b sysnet.RuleCapability) int {
		return strings.Compare(a.Type, b.Type)
	})
	return result
}
