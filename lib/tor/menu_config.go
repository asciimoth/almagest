package tornodes

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	torFieldEnabled              = "enabled"
	torFieldMaxProxyConnections  = "maxProxyConnections"
	torFieldOutboundFailures     = "outboundFailures"
	torFieldBandwidthRate        = "bandwidthRate"
	torFieldBandwidthBurst       = "bandwidthBurst"
	torFieldConnectionPadding    = "connectionPadding"
	torFieldCircuitPadding       = "circuitPadding"
	torFieldReachableORPorts     = "reachableORPorts"
	torFieldMaxPendingCircuits   = "maxPendingCircuits"
	torFieldNumCPUs              = "numCPUs"
	torFieldClientIP             = "clientIP"
	torFieldOnionTraffic         = "onionTraffic"
	torFieldEntryNodes           = "entryNodes"
	torFieldExitNodes            = "exitNodes"
	torFieldExcludeNodes         = "excludeNodes"
	torFieldExcludeExitNodes     = "excludeExitNodes"
	torFieldStrictNodes          = "strictNodes"
	torFieldNetworks             = "networks"
	torFieldUseBridges           = "useBridges"
	torFieldEnableObfs4          = "enableObfs4"
	torFieldBridges              = "bridges"
	torFieldServices             = "services"
	torFieldClientAuthorizations = "clientAuthorizations"
)

func (n *torDriverNode) configBlocks() []formular.Block {
	return []formular.Block{
		n.settingsBlock(),
		n.policiesBlock(),
		n.networksBlock(),
		n.bridgesBlock(),
		n.servicesBlock(),
		n.clientAuthorizationBlock(),
	}
}

func (n *torDriverNode) settingsBlock() formular.Block {
	d := n.config.Driver
	return torFormBlock(torSettingsBlockID, 30, false, false, []formular.Item{
		n.torCheckbox(torSettingsBlockID, torFieldEnabled, "Enabled", n.config.Enabled),
		n.torInt(torSettingsBlockID, torFieldMaxProxyConnections, "Maximum proxy connections", d.MaxProxyConnections, 0, 65536),
	})
}

func (n *torDriverNode) policiesBlock() formular.Block {
	d := n.config.Driver
	return torFormBlock(torPoliciesBlockID, 40, true, true, []formular.Item{
		n.torRadio(torPoliciesBlockID, torFieldOutboundFailures, "Outbound failure policy", torMenuDefault(d.OutboundFailures), []string{"default", "retry", "latch"}),
		n.torUintText(torPoliciesBlockID, torFieldBandwidthRate, "Bandwidth rate (bytes/s)", d.BandwidthRate),
		n.torUintText(torPoliciesBlockID, torFieldBandwidthBurst, "Bandwidth burst (bytes)", d.BandwidthBurst),
		n.torRadio(torPoliciesBlockID, torFieldConnectionPadding, "Connection padding", torMenuDefault(d.ConnectionPadding), []string{"default", "negotiated", "force", "reduced", "disabled"}),
		n.torRadio(torPoliciesBlockID, torFieldCircuitPadding, "Circuit padding", torMenuDefault(d.CircuitPadding), []string{"default", "standard", "reduced", "disabled"}),
		n.torText(torPoliciesBlockID, torFieldReachableORPorts, "Reachable relay ports", torPortsText(d.ReachableORPorts)),
		n.torInt(torPoliciesBlockID, torFieldMaxPendingCircuits, "Maximum pending circuits", int(d.MaxPendingCircuits), 0, 1024),
		n.torInt(torPoliciesBlockID, torFieldNumCPUs, "CPU count", int(d.NumCPUs), 0, 65535),
		n.torRadio(torPoliciesBlockID, torFieldClientIP, "Client IP policy", torMenuDefault(d.ClientIP), []string{"default", "ipv4", "dual", "prefer-ipv6", "ipv6"}),
		n.torRadio(torPoliciesBlockID, torFieldOnionTraffic, "Onion traffic", torMenuDefault(d.OnionTraffic), []string{"default", "allow", "only", "reject"}),
		n.torMultiline(torPoliciesBlockID, torFieldEntryNodes, "Entry relay fingerprints", strings.Join(d.EntryNodes, "\n")),
		n.torMultiline(torPoliciesBlockID, torFieldExitNodes, "Exit relay fingerprints", strings.Join(d.ExitNodes, "\n")),
		n.torMultiline(torPoliciesBlockID, torFieldExcludeNodes, "Excluded relay fingerprints", strings.Join(d.ExcludeNodes, "\n")),
		n.torMultiline(torPoliciesBlockID, torFieldExcludeExitNodes, "Excluded exit fingerprints", strings.Join(d.ExcludeExitNodes, "\n")),
		n.torCheckbox(torPoliciesBlockID, torFieldStrictNodes, "Strict relay selection", d.StrictNodes),
	})
}

func (n *torDriverNode) networksBlock() formular.Block {
	template := torNetworkTemplate()
	return torFormBlock(torNetworksBlockID, 50, false, false, []formular.Item{
		n.torArray(torNetworksBlockID, torFieldNetworks, "Tor Networks", []formular.ArrayTemplate{template}, torNetworkElements(template, n.config.Networks)),
	})
}

func torNetworkTemplate() formular.ArrayTemplate {
	addressIsolation := formular.CheckboxField("isolateDestinationAddress", "Isolate destination address", false)
	portIsolation := formular.CheckboxField("isolateDestinationPort", "Isolate destination port", false)
	condition := formular.FieldNotEquals("circuits", "connection")
	formular.VisibleWhen(condition)(&addressIsolation)
	formular.VisibleWhen(condition)(&portIsolation)
	return formular.ArrayTemplate{Name: "network", Label: "Network", Items: []formular.Item{
		formular.TextField("name", "Name", "", formular.Required),
		formular.RadioField("circuits", "Circuit isolation", "default", torChoices("default", "session", "connection")),
		addressIsolation,
		portIsolation,
	}}
}

func torNetworkElements(template formular.ArrayTemplate, values []TorDriverNetworkConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, torElementID("network", i), map[string]any{
			"name": value.Name, "circuits": torMenuDefault(value.Circuits),
			"isolateDestinationAddress": value.IsolateDestinationAddress,
			"isolateDestinationPort":    value.IsolateDestinationPort,
		}))
	}
	return out
}

func (n *torDriverNode) bridgesBlock() formular.Block {
	templates := []formular.ArrayTemplate{torPlainBridgeTemplate(), torObfs4BridgeTemplate()}
	return torFormBlock(torBridgesBlockID, 60, true, true, []formular.Item{
		n.torCheckbox(torBridgesBlockID, torFieldUseBridges, "Use bridges", n.config.Driver.UseBridges),
		n.torCheckbox(torBridgesBlockID, torFieldEnableObfs4, "Enable obfs4", n.config.Driver.EnableObfs4),
		n.torArray(torBridgesBlockID, torFieldBridges, "Bridge lines", templates, torBridgeElements(templates, n.config.Driver.Bridges)),
	})
}

func torPlainBridgeTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{Name: "plain", Label: "Plain bridge", Items: []formular.Item{
		formular.TextField("address", "Address", "", formular.Required),
		formular.TextField("fingerprint", "Fingerprint", ""),
	}}
}

func torObfs4BridgeTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{Name: "obfs4", Label: "obfs4 bridge", Items: []formular.Item{
		formular.TextField("address", "Address", "", formular.Required),
		formular.TextField("fingerprint", "Fingerprint", "", formular.Required),
		formular.TextField("obfs4Certificate", "obfs4 certificate", "", formular.Required),
		formular.RadioField("iat", "Inter-arrival timing", "default", torChoices("default", "disabled", "enabled", "paranoid")),
	}}
}

func torBridgeElements(templates []formular.ArrayTemplate, values []TorDriverBridge) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		template := templates[0]
		if value.Transport == "obfs4" {
			template = templates[1]
		}
		out = append(out, formular.InstantiateArrayTemplate(template, torElementID("bridge", i), map[string]any{
			"address": value.Address, "fingerprint": value.Fingerprint,
			"obfs4Certificate": value.Obfs4Certificate, "iat": torMenuDefault(value.IAT),
		}))
	}
	return out
}

func (n *torDriverNode) servicesBlock() formular.Block {
	template := torServiceTemplate()
	return torFormBlock(torServicesBlockID, 70, false, false, []formular.Item{
		n.torArray(torServicesBlockID, torFieldServices, "Onion services", []formular.ArrayTemplate{template}, torServiceElements(template, n.config.Services)),
	})
}

func torServiceTemplate() formular.ArrayTemplate {
	key := formular.TextField("keySecret", "Private key secret", "", formular.Selector(nodekit.SecretNameSelector))
	maxStreams := formular.IntField("maxStreams", "Maximum streams", 0)
	minimum, maximum := float64(0), float64(65535)
	maxStreams.Min, maxStreams.Max = &minimum, &maximum
	return formular.ArrayTemplate{Name: "service", Label: "Onion service", Items: []formular.Item{
		formular.TextField("name", "Name", "", formular.Required),
		formular.TextField("ports", "Virtual ports", "80", formular.Required),
		maxStreams,
		formular.RadioField("maxStreamsPolicy", "Stream limit policy", "default", torChoices("default", "keep", "close")),
		key,
		formular.MultilineTextField("authorizedClients", "Authorized client public keys", ""),
	}}
}

func torServiceElements(template formular.ArrayTemplate, values []TorDriverServiceConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, torElementID("service", i), map[string]any{
			"name": value.Name, "ports": torPortsText(value.Ports), "maxStreams": int(value.MaxStreams),
			"maxStreamsPolicy": torMenuDefault(value.MaxStreamsPolicy), "keySecret": value.KeySecret,
			"authorizedClients": strings.Join(value.AuthorizedClients, "\n"),
		}))
	}
	return out
}

func (n *torDriverNode) clientAuthorizationBlock() formular.Block {
	template := torClientAuthorizationTemplate()
	return torFormBlock(torClientAuthBlockID, 80, true, true, []formular.Item{
		n.torArray(torClientAuthBlockID, torFieldClientAuthorizations, "Client authorization", []formular.ArrayTemplate{template}, torClientAuthorizationElements(template, n.config.ClientAuthorizations)),
	})
}

func torClientAuthorizationTemplate() formular.ArrayTemplate {
	secret := formular.TextField("privateKeySecret", "Private key secret", "", formular.Selector(nodekit.SecretNameSelector))
	return formular.ArrayTemplate{Name: "authorization", Label: "Authorization", Items: []formular.Item{
		formular.TextField("address", "Onion address", "", formular.Required),
		formular.TextField("name", "Key name", ""),
		secret,
	}}
}

func torClientAuthorizationElements(template formular.ArrayTemplate, values []TorDriverClientAuthConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, torElementID("authorization", i), map[string]any{
			"address": value.Address, "name": value.Name,
			"privateKeySecret": value.PrivateKeySecret,
		}))
	}
	return out
}

func torFormBlock(id string, order int, collapsible, collapsed bool, items []formular.Item) formular.Block {
	return formular.Block{
		ID: id, Order: order, Generation: 1, Form: true,
		Collapsible: collapsible, Collapsed: collapsed, Items: items,
	}
}

func (n *torDriverNode) torText(block, id, label, value string) formular.Item {
	return formular.TextField(id, label, value, n.torFieldOptions(block)...)
}

func (n *torDriverNode) torUintText(block, id, label string, value uint64) formular.Item {
	return n.torText(block, id, label, strconv.FormatUint(value, 10))
}

func (n *torDriverNode) torMultiline(block, id, label, value string) formular.Item {
	return formular.MultilineTextField(id, label, value, n.torFieldOptions(block)...)
}

func (n *torDriverNode) torCheckbox(block, id, label string, value bool) formular.Item {
	return formular.CheckboxField(id, label, value, n.torFieldOptions(block)...)
}

func (n *torDriverNode) torRadio(block, id, label, value string, choices []string) formular.Item {
	return formular.RadioField(id, label, value, torChoices(choices...), n.torFieldOptions(block)...)
}

func (n *torDriverNode) torInt(block, id, label string, value, minimum, maximum int) formular.Item {
	item := formular.IntField(id, label, value, n.torFieldOptions(block)...)
	minValue, maxValue := float64(minimum), float64(maximum)
	item.Min, item.Max = &minValue, &maxValue
	return item
}

func (n *torDriverNode) torArray(block, id, label string, templates []formular.ArrayTemplate, elements []formular.ArrayElement) formular.Item {
	field := &formular.Field{Kind: formular.FieldArray, Templates: templates, Elements: elements}
	if n.formErrorBlock == block && n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func (n *torDriverNode) torFieldOptions(block string) []formular.FieldOption {
	options := make([]formular.FieldOption, 0, 2)
	if n.formErrorBlock == block && n.formError != "" {
		options = append(options, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return options
}

func torChoices(values ...string) []formular.Choice {
	out := make([]formular.Choice, len(values))
	for i, value := range values {
		out[i] = formular.Choice{Value: value}
	}
	return out
}

func torMenuDefault(value string) string {
	if value == "" {
		return "default"
	}
	return value
}

func torPortsText[T ~uint16](ports []T) string {
	values := make([]string, len(ports))
	for i, port := range ports {
		values[i] = strconv.FormatUint(uint64(port), 10)
	}
	return strings.Join(values, ", ")
}

func torElementID(kind string, index int) string {
	return fmt.Sprintf("%s-%d", kind, index+1)
}
