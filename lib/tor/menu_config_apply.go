package tornodes

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

func isTorConfigBlock(block string) bool {
	switch block {
	case torSettingsBlockID, torPoliciesBlockID, torNetworksBlockID,
		torBridgesBlockID, torServicesBlockID, torClientAuthBlockID:
		return true
	default:
		return false
	}
}

func (n *torDriverNode) applyConfigForm(block string, values map[string]any) error {
	next := cloneTorDriverConfig(n.config)
	var err error
	switch block {
	case torSettingsBlockID:
		err = applyTorSettings(&next, values)
	case torPoliciesBlockID:
		err = applyTorPolicies(&next, values)
	case torNetworksBlockID:
		err = applyTorNetworks(&next, values)
	case torBridgesBlockID:
		err = applyTorBridges(&next, values)
	case torServicesBlockID:
		err = applyTorServices(&next, values)
	case torClientAuthBlockID:
		err = applyTorClientAuthorizations(&next, values)
	default:
		return nil
	}
	if err == nil {
		next.normalize()
		err = validateTorDriverConfig(next)
	}
	if err == nil && torConfigGeneratesSecrets(next) && !nodekit.CanPersistNodeConfig(n.Runtime) {
		err = errors.New("generated secrets require durable node configuration persistence; configure explicit secret selectors")
	}
	if err != nil {
		n.formErrorBlock = block
		n.formError = err.Error()
		n.sendMenuSnapshot(true)
		_ = n.updateLabel()
		return nil
	}

	changed := !torConfigEqual(n.config, next)
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

func applyTorSettings(config *TorDriverConfig, values map[string]any) error {
	if err := torCheckKeys(values,
		torFieldEnabled, torFieldMaxProxyConnections); err != nil {
		return err
	}
	var err error
	if config.Enabled, err = torBool(values, torFieldEnabled); err != nil {
		return err
	}
	config.Driver.MaxProxyConnections, err = torInt(values, torFieldMaxProxyConnections, 0, 65536)
	return err
}

func applyTorPolicies(config *TorDriverConfig, values map[string]any) error {
	if err := torCheckKeys(values,
		torFieldOutboundFailures, torFieldBandwidthRate, torFieldBandwidthBurst, torFieldConnectionPadding,
		torFieldCircuitPadding, torFieldReachableORPorts, torFieldMaxPendingCircuits,
		torFieldNumCPUs, torFieldClientIP, torFieldOnionTraffic, torFieldEntryNodes,
		torFieldExitNodes, torFieldExcludeNodes, torFieldExcludeExitNodes, torFieldStrictNodes); err != nil {
		return err
	}
	d := &config.Driver
	var err error
	if d.OutboundFailures, err = torChoice(values, torFieldOutboundFailures, "default", "retry", "latch"); err != nil {
		return err
	}
	if d.BandwidthRate, err = torUint(values, torFieldBandwidthRate); err != nil {
		return err
	}
	if d.BandwidthBurst, err = torUint(values, torFieldBandwidthBurst); err != nil {
		return err
	}
	if d.ConnectionPadding, err = torChoice(values, torFieldConnectionPadding, "default", "negotiated", "force", "reduced", "disabled"); err != nil {
		return err
	}
	if d.CircuitPadding, err = torChoice(values, torFieldCircuitPadding, "default", "standard", "reduced", "disabled"); err != nil {
		return err
	}
	if d.ReachableORPorts, err = torPorts(values, torFieldReachableORPorts, false); err != nil {
		return err
	}
	var integer int
	if integer, err = torInt(values, torFieldMaxPendingCircuits, 0, 1024); err != nil {
		return err
	}
	d.MaxPendingCircuits = uint16(integer)
	if integer, err = torInt(values, torFieldNumCPUs, 0, 65535); err != nil {
		return err
	}
	d.NumCPUs = uint16(integer)
	if d.ClientIP, err = torChoice(values, torFieldClientIP, "default", "ipv4", "dual", "prefer-ipv6", "ipv6"); err != nil {
		return err
	}
	if d.OnionTraffic, err = torChoice(values, torFieldOnionTraffic, "default", "allow", "only", "reject"); err != nil {
		return err
	}
	if d.EntryNodes, err = torStringList(values, torFieldEntryNodes); err != nil {
		return err
	}
	if d.ExitNodes, err = torStringList(values, torFieldExitNodes); err != nil {
		return err
	}
	if d.ExcludeNodes, err = torStringList(values, torFieldExcludeNodes); err != nil {
		return err
	}
	if d.ExcludeExitNodes, err = torStringList(values, torFieldExcludeExitNodes); err != nil {
		return err
	}
	d.StrictNodes, err = torBool(values, torFieldStrictNodes)
	return err
}

func applyTorNetworks(config *TorDriverConfig, values map[string]any) error {
	if err := torCheckKeys(values, torFieldNetworks); err != nil {
		return err
	}
	elements, err := torArray(values, torFieldNetworks)
	if err != nil {
		return err
	}
	result := make([]TorDriverNetworkConfig, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		if err = torCheckElement(element, "network", seen, "name", "circuits", "isolateDestinationAddress", "isolateDestinationPort"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		var item TorDriverNetworkConfig
		if item.Name, err = torString(element.Values, "name"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		if item.Circuits, err = torChoice(element.Values, "circuits", "default", "session", "connection"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		if item.IsolateDestinationAddress, err = torBool(element.Values, "isolateDestinationAddress"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		if item.IsolateDestinationPort, err = torBool(element.Values, "isolateDestinationPort"); err != nil {
			return fmt.Errorf("network %d: %w", i+1, err)
		}
		result = append(result, item)
	}
	config.Networks = result
	return nil
}

func applyTorBridges(config *TorDriverConfig, values map[string]any) error {
	if err := torCheckKeys(values, torFieldUseBridges, torFieldEnableObfs4, torFieldBridges); err != nil {
		return err
	}
	var err error
	if config.Driver.UseBridges, err = torBool(values, torFieldUseBridges); err != nil {
		return err
	}
	if config.Driver.EnableObfs4, err = torBool(values, torFieldEnableObfs4); err != nil {
		return err
	}
	elements, err := torArray(values, torFieldBridges)
	if err != nil {
		return err
	}
	result := make([]TorDriverBridge, 0, len(elements))
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
		if err = torCheckElement(element, element.Template, seen, allowed...); err != nil {
			return fmt.Errorf("bridge %d: %w", i+1, err)
		}
		item := TorDriverBridge{Transport: element.Template}
		if item.Address, err = torString(element.Values, "address"); err != nil {
			return fmt.Errorf("bridge %d: %w", i+1, err)
		}
		if item.Fingerprint, err = torString(element.Values, "fingerprint"); err != nil {
			return fmt.Errorf("bridge %d: %w", i+1, err)
		}
		if element.Template == "obfs4" {
			if item.Obfs4Certificate, err = torString(element.Values, "obfs4Certificate"); err != nil {
				return fmt.Errorf("bridge %d: %w", i+1, err)
			}
			if item.IAT, err = torChoice(element.Values, "iat", "default", "disabled", "enabled", "paranoid"); err != nil {
				return fmt.Errorf("bridge %d: %w", i+1, err)
			}
		}
		result = append(result, item)
	}
	config.Driver.Bridges = result
	return nil
}

func applyTorServices(config *TorDriverConfig, values map[string]any) error {
	if err := torCheckKeys(values, torFieldServices); err != nil {
		return err
	}
	elements, err := torArray(values, torFieldServices)
	if err != nil {
		return err
	}
	result := make([]TorDriverServiceConfig, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		if err = torCheckElement(element, "service", seen, "name", "ports", "maxStreams", "maxStreamsPolicy", "keySecret", "authorizedClients"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		var item TorDriverServiceConfig
		if item.Name, err = torString(element.Values, "name"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		if item.Ports, err = torPorts(element.Values, "ports", true); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		var maximum int
		if maximum, err = torInt(element.Values, "maxStreams", 0, 65535); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		item.MaxStreams = uint16(maximum)
		if item.MaxStreamsPolicy, err = torChoice(element.Values, "maxStreamsPolicy", "default", "keep", "close"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		if item.KeySecret, err = torString(element.Values, "keySecret"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		if item.AuthorizedClients, err = torStringList(element.Values, "authorizedClients"); err != nil {
			return fmt.Errorf("onion service %d: %w", i+1, err)
		}
		result = append(result, item)
	}
	config.Services = result
	return nil
}

func applyTorClientAuthorizations(config *TorDriverConfig, values map[string]any) error {
	if err := torCheckKeys(values, torFieldClientAuthorizations); err != nil {
		return err
	}
	elements, err := torArray(values, torFieldClientAuthorizations)
	if err != nil {
		return err
	}
	result := make([]TorDriverClientAuthConfig, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for i, element := range elements {
		if err = torCheckElement(element, "authorization", seen, "address", "name", "privateKeySecret"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		var item TorDriverClientAuthConfig
		if item.Address, err = torString(element.Values, "address"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		if item.Name, err = torString(element.Values, "name"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		if item.PrivateKeySecret, err = torString(element.Values, "privateKeySecret"); err != nil {
			return fmt.Errorf("client authorization %d: %w", i+1, err)
		}
		result = append(result, item)
	}
	config.ClientAuthorizations = result
	return nil
}

func torCheckKeys(values map[string]any, expected ...string) error {
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

func torCheckElement(element formular.ArrayElementValue, template string, seen map[string]struct{}, fields ...string) error {
	if element.Template != template {
		return fmt.Errorf("template %q is not supported", element.Template)
	}
	if _, ok := seen[element.ID]; ok {
		return fmt.Errorf("element ID %q is duplicated", element.ID)
	}
	seen[element.ID] = struct{}{}
	return torCheckKeys(element.Values, fields...)
}

func torString(values map[string]any, field string) (string, error) {
	value, ok := formular.StringValue(values[field])
	if !ok {
		return "", fmt.Errorf("field %q must be text", field)
	}
	return strings.TrimSpace(value), nil
}

func torBool(values map[string]any, field string) (bool, error) {
	value, ok := formular.BoolValue(values[field])
	if !ok {
		return false, fmt.Errorf("field %q must be true or false", field)
	}
	return value, nil
}

func torInt(values map[string]any, field string, minimum, maximum int) (int, error) {
	value, ok := formular.IntValue(values[field])
	if !ok {
		return 0, fmt.Errorf("field %q must be a whole number", field)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("field %q must be between %d and %d", field, minimum, maximum)
	}
	return value, nil
}

func torUint(values map[string]any, field string) (uint64, error) {
	value, err := torString(values, field)
	if err != nil {
		return 0, err
	}
	result, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q must be an unsigned decimal number", field)
	}
	return result, nil
}

func torChoice(values map[string]any, field string, choices ...string) (string, error) {
	value, err := torString(values, field)
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

func torPorts(values map[string]any, field string, required bool) ([]uint16, error) {
	value, err := torString(values, field)
	if err != nil {
		return nil, err
	}
	parts := torSplitList(value)
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

func torStringList(values map[string]any, field string) ([]string, error) {
	value, err := torString(values, field)
	if err != nil {
		return nil, err
	}
	return torSplitList(value), nil
}

func torSplitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func torArray(values map[string]any, field string) ([]formular.ArrayElementValue, error) {
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
