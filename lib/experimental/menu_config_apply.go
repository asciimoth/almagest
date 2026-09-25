package experimentalnodes

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

func isXTorConfigBlock(block string) bool {
	switch block {
	case xTorSettingsBlockID, xTorPoliciesBlockID, xTorNetworksBlockID,
		xTorBridgesBlockID, xTorServicesBlockID, xTorClientAuthBlockID:
		return true
	default:
		return false
	}
}

func (n *xTorDriverNode) applyConfigForm(block string, values map[string]any) error {
	next := cloneXTorDriverConfig(n.config)
	var err error
	switch block {
	case xTorSettingsBlockID:
		err = applyXTorSettings(&next, values)
	case xTorPoliciesBlockID:
		err = applyXTorPolicies(&next, values)
	case xTorNetworksBlockID:
		err = applyXTorNetworks(&next, values)
	case xTorBridgesBlockID:
		err = applyXTorBridges(&next, values)
	case xTorServicesBlockID:
		err = applyXTorServices(&next, values)
	case xTorClientAuthBlockID:
		err = applyXTorClientAuthorizations(&next, values)
	default:
		return nil
	}
	if err == nil {
		next.normalize()
		err = validateXTorDriverConfig(next)
	}
	if err == nil {
		err = validateXTorHostCompatibility(next, n.host)
	}
	if err == nil && xTorConfigGeneratesSecrets(next) && !nodekit.CanPersistNodeConfig(n.Runtime) {
		err = errors.New("generated secrets require durable node configuration persistence; configure explicit secret selectors")
	}
	if err != nil {
		n.formErrorBlock = block
		n.formError = err.Error()
		n.sendMenuSnapshot(true)
		_ = n.updateLabel()
		return nil
	}

	changed := !xTorConfigEqual(n.config, next)
	n.formError = ""
	n.formErrorBlock = ""
	if !changed {
		n.sendMenuSnapshot(true)
		return n.updateLabel()
	}

	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.ready = false
	n.activeSources = nil
	n.config = next
	if err = n.rebuildPorts(); err != nil {
		return err
	}
	n.scheduleRebuild("menu configuration changed")
	n.sendMenuSnapshot(true)
	return nil
}

func applyXTorSettings(config *XTorDriverConfig, values map[string]any) error {
	if err := xTorCheckKeys(values,
		xTorFieldEnabled, xTorFieldMaxProxyConnections); err != nil {
		return err
	}
	var err error
	if config.Enabled, err = xTorBool(values, xTorFieldEnabled); err != nil {
		return err
	}
	config.Driver.MaxProxyConnections, err = xTorInt(values, xTorFieldMaxProxyConnections, 0, 65536)
	return err
}

func applyXTorPolicies(config *XTorDriverConfig, values map[string]any) error {
	if err := xTorCheckKeys(values,
		xTorFieldOutboundFailures, xTorFieldBandwidthRate, xTorFieldBandwidthBurst, xTorFieldConnectionPadding,
		xTorFieldCircuitPadding, xTorFieldReachableORPorts, xTorFieldMaxPendingCircuits,
		xTorFieldNumCPUs, xTorFieldClientIP, xTorFieldOnionTraffic, xTorFieldEntryNodes,
		xTorFieldExitNodes, xTorFieldExcludeNodes, xTorFieldExcludeExitNodes, xTorFieldStrictNodes); err != nil {
		return err
	}
	d := &config.Driver
	var err error
	if d.OutboundFailures, err = xTorChoice(values, xTorFieldOutboundFailures, "default", "retry", "latch"); err != nil {
		return err
	}
	if d.BandwidthRate, err = xTorUint(values, xTorFieldBandwidthRate); err != nil {
		return err
	}
	if d.BandwidthBurst, err = xTorUint(values, xTorFieldBandwidthBurst); err != nil {
		return err
	}
	if d.ConnectionPadding, err = xTorChoice(values, xTorFieldConnectionPadding, "default", "negotiated", "force", "reduced", "disabled"); err != nil {
		return err
	}
	if d.CircuitPadding, err = xTorChoice(values, xTorFieldCircuitPadding, "default", "standard", "reduced", "disabled"); err != nil {
		return err
	}
	if d.ReachableORPorts, err = xTorPorts(values, xTorFieldReachableORPorts, false); err != nil {
		return err
	}
	var integer int
	if integer, err = xTorInt(values, xTorFieldMaxPendingCircuits, 0, 1024); err != nil {
		return err
	}
	d.MaxPendingCircuits = uint16(integer)
	if integer, err = xTorInt(values, xTorFieldNumCPUs, 0, 65535); err != nil {
		return err
	}
	d.NumCPUs = uint16(integer)
	if d.ClientIP, err = xTorChoice(values, xTorFieldClientIP, "default", "ipv4", "dual", "prefer-ipv6", "ipv6"); err != nil {
		return err
	}
	if d.OnionTraffic, err = xTorChoice(values, xTorFieldOnionTraffic, "default", "allow", "only", "reject"); err != nil {
		return err
	}
	if d.EntryNodes, err = xTorStringList(values, xTorFieldEntryNodes); err != nil {
		return err
	}
	if d.ExitNodes, err = xTorStringList(values, xTorFieldExitNodes); err != nil {
		return err
	}
	if d.ExcludeNodes, err = xTorStringList(values, xTorFieldExcludeNodes); err != nil {
		return err
	}
	if d.ExcludeExitNodes, err = xTorStringList(values, xTorFieldExcludeExitNodes); err != nil {
		return err
	}
	d.StrictNodes, err = xTorBool(values, xTorFieldStrictNodes)
	return err
}

func applyXTorNetworks(config *XTorDriverConfig, values map[string]any) error {
	if err := xTorCheckKeys(values, xTorFieldNetworks); err != nil {
		return err
	}
	elements, err := xTorArray(values, xTorFieldNetworks)
	if err != nil {
		return err
	}
	result := make([]XTorDriverNetworkConfig, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		if err = xTorCheckElement(element, "network", seen, "name", "circuits", "isolateDestinationAddress", "isolateDestinationPort"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		var item XTorDriverNetworkConfig
		if item.Name, err = xTorString(element.Values, "name"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		if item.Circuits, err = xTorChoice(element.Values, "circuits", "default", "session", "connection"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		if item.IsolateDestinationAddress, err = xTorBool(element.Values, "isolateDestinationAddress"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		if item.IsolateDestinationPort, err = xTorBool(element.Values, "isolateDestinationPort"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		result = append(result, item)
	}
	config.Networks = result
	return nil
}

func applyXTorBridges(config *XTorDriverConfig, values map[string]any) error {
	if err := xTorCheckKeys(values, xTorFieldUseBridges, xTorFieldEnableObfs4, xTorFieldBridges); err != nil {
		return err
	}
	var err error
	if config.Driver.UseBridges, err = xTorBool(values, xTorFieldUseBridges); err != nil {
		return err
	}
	if config.Driver.EnableObfs4, err = xTorBool(values, xTorFieldEnableObfs4); err != nil {
		return err
	}
	elements, err := xTorArray(values, xTorFieldBridges)
	if err != nil {
		return err
	}
	result := make([]XTorDriverBridge, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		var allowed []string
		switch element.Template {
		case "plain":
			allowed = []string{"address", "fingerprint"}
		case "obfs4":
			allowed = []string{"address", "fingerprint", "obfs4Certificate", "iat"}
		default:
			return fmt.Errorf("bridge %d: template %q is not supported", i+1, element.Template)
		}
		if err = xTorCheckElement(element, element.Template, seen, allowed...); err != nil {
			return fmt.Errorf("bridge %d: %w", i+1, err)
		}
		item := XTorDriverBridge{Transport: element.Template}
		if item.Address, err = xTorString(element.Values, "address"); err != nil {
			return fmt.Errorf("bridge %d: %w", i+1, err)
		}
		if item.Fingerprint, err = xTorString(element.Values, "fingerprint"); err != nil {
			return fmt.Errorf("bridge %d: %w", i+1, err)
		}
		if element.Template == "obfs4" {
			if item.Obfs4Certificate, err = xTorString(element.Values, "obfs4Certificate"); err != nil {
				return fmt.Errorf("bridge %d: %w", i+1, err)
			}
			if item.IAT, err = xTorChoice(element.Values, "iat", "default", "disabled", "enabled", "paranoid"); err != nil {
				return fmt.Errorf("bridge %d: %w", i+1, err)
			}
		}
		result = append(result, item)
	}
	config.Driver.Bridges = result
	return nil
}

func applyXTorServices(config *XTorDriverConfig, values map[string]any) error {
	if err := xTorCheckKeys(values, xTorFieldServices); err != nil {
		return err
	}
	elements, err := xTorArray(values, xTorFieldServices)
	if err != nil {
		return err
	}
	result := make([]XTorDriverServiceConfig, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		if err = xTorCheckElement(element, "service", seen, "name", "ports", "maxStreams", "maxStreamsPolicy", "keySecret", "authorizedClients"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		var item XTorDriverServiceConfig
		if item.Name, err = xTorString(element.Values, "name"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		if item.Ports, err = xTorPorts(element.Values, "ports", true); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		var maximum int
		if maximum, err = xTorInt(element.Values, "maxStreams", 0, 65535); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		item.MaxStreams = uint16(maximum)
		if item.MaxStreamsPolicy, err = xTorChoice(element.Values, "maxStreamsPolicy", "default", "keep", "close"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		if item.KeySecret, err = xTorString(element.Values, "keySecret"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		if item.AuthorizedClients, err = xTorStringList(element.Values, "authorizedClients"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		result = append(result, item)
	}
	config.Services = result
	return nil
}

func applyXTorClientAuthorizations(config *XTorDriverConfig, values map[string]any) error {
	if err := xTorCheckKeys(values, xTorFieldClientAuthorizations); err != nil {
		return err
	}
	elements, err := xTorArray(values, xTorFieldClientAuthorizations)
	if err != nil {
		return err
	}
	result := make([]XTorDriverClientAuthConfig, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		if err = xTorCheckElement(element, "authorization", seen, "address", "name", "privateKeySecret"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		var item XTorDriverClientAuthConfig
		if item.Address, err = xTorString(element.Values, "address"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		if item.Name, err = xTorString(element.Values, "name"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		if item.PrivateKeySecret, err = xTorString(element.Values, "privateKeySecret"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		result = append(result, item)
	}
	config.ClientAuthorizations = result
	return nil
}

func xTorCheckKeys(values map[string]any, expected ...string) error {
	allowed := make(map[string]struct{}, len(expected))
	for _, key := range expected {
		allowed[key] = struct{}{}
		if _, ok := values[key]; !ok {
			return fmt.Errorf("field %q is missing", key)
		}
	}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("field %q is not supported", key)
		}
	}
	return nil
}

func xTorCheckElement(element formular.ArrayElementValue, template string, seen map[string]struct{}, fields ...string) error {
	if element.Template != template {
		return fmt.Errorf("template %q is not supported", element.Template)
	}
	if _, ok := seen[element.ID]; ok {
		return fmt.Errorf("element ID %q is duplicated", element.ID)
	}
	seen[element.ID] = struct{}{}
	return xTorCheckKeys(element.Values, fields...)
}

func xTorString(values map[string]any, field string) (string, error) {
	value, ok := formular.StringValue(values[field])
	if !ok {
		return "", fmt.Errorf("field %q must be text", field)
	}
	return strings.TrimSpace(value), nil
}

func xTorBool(values map[string]any, field string) (bool, error) {
	value, ok := formular.BoolValue(values[field])
	if !ok {
		return false, fmt.Errorf("field %q must be true or false", field)
	}
	return value, nil
}

func xTorInt(values map[string]any, field string, minimum, maximum int) (int, error) {
	value, ok := formular.IntValue(values[field])
	if !ok {
		return 0, fmt.Errorf("field %q must be a whole number", field)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("field %q must be between %d and %d", field, minimum, maximum)
	}
	return value, nil
}

func xTorUint(values map[string]any, field string) (uint64, error) {
	value, err := xTorString(values, field)
	if err != nil {
		return 0, err
	}
	result, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q must be an unsigned decimal number", field)
	}
	return result, nil
}

func xTorChoice(values map[string]any, field string, choices ...string) (string, error) {
	value, err := xTorString(values, field)
	if err != nil {
		return "", err
	}
	for _, choice := range choices {
		if value == choice {
			if value == "default" {
				return "", nil
			}
			return value, nil
		}
	}
	return "", fmt.Errorf("field %q has unsupported value %q", field, value)
}

func xTorPorts(values map[string]any, field string, required bool) ([]uint16, error) {
	value, err := xTorString(values, field)
	if err != nil {
		return nil, err
	}
	parts := xTorSplitList(value)
	if required && len(parts) == 0 {
		return nil, fmt.Errorf("field %q requires at least one port", field)
	}
	ports := make([]uint16, 0, len(parts))
	seen := make(map[uint16]struct{}, len(parts))
	for _, part := range parts {
		parsed, parseErr := strconv.ParseUint(part, 10, 16)
		if parseErr != nil || parsed == 0 {
			return nil, fmt.Errorf("field %q contains invalid TCP port %q", field, part)
		}
		port := uint16(parsed)
		if _, ok := seen[port]; ok {
			return nil, fmt.Errorf("field %q contains duplicate port %d", field, port)
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	return ports, nil
}

func xTorStringList(values map[string]any, field string) ([]string, error) {
	value, err := xTorString(values, field)
	if err != nil {
		return nil, err
	}
	return xTorSplitList(value), nil
}

func xTorSplitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func xTorArray(values map[string]any, field string) ([]formular.ArrayElementValue, error) {
	raw, ok := values[field]
	if !ok {
		return nil, fmt.Errorf("field %q is missing", field)
	}
	elements, ok := formular.ArrayElementValuesFromAny(raw)
	if !ok {
		return nil, fmt.Errorf("field %q must be a list", field)
	}
	for _, element := range elements {
		if strings.TrimSpace(element.ID) == "" {
			return nil, errors.New("list element ID is empty")
		}
	}
	return elements, nil
}
