package experimentalnodes

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	xTorFieldEnabled              = "enabled"
	xTorFieldMaxProxyConnections  = "maxProxyConnections"
	xTorFieldOutboundFailures     = "outboundFailures"
	xTorFieldBandwidthRate        = "bandwidthRate"
	xTorFieldBandwidthBurst       = "bandwidthBurst"
	xTorFieldConnectionPadding    = "connectionPadding"
	xTorFieldCircuitPadding       = "circuitPadding"
	xTorFieldReachableORPorts     = "reachableORPorts"
	xTorFieldMaxPendingCircuits   = "maxPendingCircuits"
	xTorFieldNumCPUs              = "numCPUs"
	xTorFieldClientIP             = "clientIP"
	xTorFieldOnionTraffic         = "onionTraffic"
	xTorFieldEntryNodes           = "entryNodes"
	xTorFieldExitNodes            = "exitNodes"
	xTorFieldExcludeNodes         = "excludeNodes"
	xTorFieldExcludeExitNodes     = "excludeExitNodes"
	xTorFieldStrictNodes          = "strictNodes"
	xTorFieldNetworks             = "networks"
	xTorFieldUseBridges           = "useBridges"
	xTorFieldEnableObfs4          = "enableObfs4"
	xTorFieldBridges              = "bridges"
	xTorFieldServices             = "services"
	xTorFieldClientAuthorizations = "clientAuthorizations"
)

func (n *xTorDriverNode) configBlocks() []formular.Block {
	return []formular.Block{
		n.settingsBlock(),
		n.policiesBlock(),
		n.networksBlock(),
		n.bridgesBlock(),
		n.servicesBlock(),
		n.clientAuthorizationBlock(),
	}
}

func (n *xTorDriverNode) settingsBlock() formular.Block {
	d := n.config.Driver
	return xTorFormBlock(xTorSettingsBlockID, 30, false, false, []formular.Item{
		n.xTorCheckbox(xTorSettingsBlockID, xTorFieldEnabled, "Enabled", n.config.Enabled,
			"Starts Tor and publishes configured outputs."),
		n.xTorInt(xTorSettingsBlockID, xTorFieldMaxProxyConnections, "Maximum proxy connections", d.MaxProxyConnections, 0, 65536,
			"Zero uses the tor-driver default."),
	})
}

func (n *xTorDriverNode) policiesBlock() formular.Block {
	d := n.config.Driver
	return xTorFormBlock(xTorPoliciesBlockID, 40, true, true, []formular.Item{
		n.xTorRadio(xTorPoliciesBlockID, xTorFieldOutboundFailures, "Outbound failure policy", xTorMenuDefault(d.OutboundFailures),
			[]string{"default", "retry", "latch"}, "Retry failures or latch the outbound path until it is replaced."),
		n.xTorUintText(xTorPoliciesBlockID, xTorFieldBandwidthRate, "Bandwidth rate (bytes/s)", d.BandwidthRate,
			"Set rate and burst together. Zero disables the limit."),
		n.xTorUintText(xTorPoliciesBlockID, xTorFieldBandwidthBurst, "Bandwidth burst (bytes)", d.BandwidthBurst,
			"Must be at least the bandwidth rate."),
		n.xTorRadio(xTorPoliciesBlockID, xTorFieldConnectionPadding, "Connection padding", xTorMenuDefault(d.ConnectionPadding),
			[]string{"default", "negotiated", "force", "reduced", "disabled"}, "Controls Tor connection padding."),
		n.xTorRadio(xTorPoliciesBlockID, xTorFieldCircuitPadding, "Circuit padding", xTorMenuDefault(d.CircuitPadding),
			[]string{"default", "standard", "reduced", "disabled"}, "Controls Tor circuit padding."),
		n.xTorText(xTorPoliciesBlockID, xTorFieldReachableORPorts, "Reachable relay ports", xTorPortsText(d.ReachableORPorts),
			"Comma- or space-separated TCP ports. Empty permits all relay ports."),
		n.xTorInt(xTorPoliciesBlockID, xTorFieldMaxPendingCircuits, "Maximum pending circuits", int(d.MaxPendingCircuits), 0, 1024,
			"Zero uses the Tor default."),
		n.xTorInt(xTorPoliciesBlockID, xTorFieldNumCPUs, "CPU count", int(d.NumCPUs), 0, 65535,
			"Zero lets Tor detect the CPU count."),
		n.xTorRadio(xTorPoliciesBlockID, xTorFieldClientIP, "Client IP policy", xTorMenuDefault(d.ClientIP),
			[]string{"default", "ipv4", "dual", "prefer-ipv6", "ipv6"}, "Selects the IP families Tor can use."),
		n.xTorRadio(xTorPoliciesBlockID, xTorFieldOnionTraffic, "Onion traffic", xTorMenuDefault(d.OnionTraffic),
			[]string{"default", "allow", "only", "reject"}, "Controls access to onion services."),
		n.xTorMultiline(xTorPoliciesBlockID, xTorFieldEntryNodes, "Entry relay fingerprints", strings.Join(d.EntryNodes, "\n"),
			"One 40-character relay fingerprint per line."),
		n.xTorMultiline(xTorPoliciesBlockID, xTorFieldExitNodes, "Exit relay fingerprints", strings.Join(d.ExitNodes, "\n"),
			"One 40-character relay fingerprint per line."),
		n.xTorMultiline(xTorPoliciesBlockID, xTorFieldExcludeNodes, "Excluded relay fingerprints", strings.Join(d.ExcludeNodes, "\n"),
			"One 40-character relay fingerprint per line."),
		n.xTorMultiline(xTorPoliciesBlockID, xTorFieldExcludeExitNodes, "Excluded exit fingerprints", strings.Join(d.ExcludeExitNodes, "\n"),
			"One 40-character relay fingerprint per line."),
		n.xTorCheckbox(xTorPoliciesBlockID, xTorFieldStrictNodes, "Strict relay selection", d.StrictNodes,
			"Do not use relays outside the configured lists."),
	})
}

func (n *xTorDriverNode) networksBlock() formular.Block {
	template := xTorNetworkTemplate()
	return xTorFormBlock(xTorNetworksBlockID, 50, false, false, []formular.Item{
		n.xTorArray(xTorNetworksBlockID, xTorFieldNetworks, "Tor Networks", []formular.ArrayTemplate{template}, xTorNetworkElements(template, n.config.Networks),
			"Each entry creates one Network output. Per-connection circuits cannot use destination isolation."),
	})
}

func xTorNetworkTemplate() formular.ArrayTemplate {
	addressIsolation := formular.CheckboxField("isolateDestinationAddress", "Isolate destination address", false,
		formular.Help("Use a different circuit when the destination address changes."))
	portIsolation := formular.CheckboxField("isolateDestinationPort", "Isolate destination port", false,
		formular.Help("Use a different circuit when the destination port changes."))
	condition := formular.FieldNotEquals("circuits", "connection")
	formular.VisibleWhen(condition)(&addressIsolation)
	formular.VisibleWhen(condition)(&portIsolation)
	return formular.ArrayTemplate{Name: "network", Label: "Network", Items: []formular.Item{
		formular.TextField("name", "Name", "", formular.Required, formular.Help("Used in the graph port name.")),
		formular.RadioField("circuits", "Circuit isolation", "default", xTorChoices("default", "session", "connection"),
			formular.Help("Session reuses circuits. Connection creates a new circuit for every connection.")),
		addressIsolation,
		portIsolation,
	}}
}

func xTorNetworkElements(template formular.ArrayTemplate, values []XTorDriverNetworkConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, xTorElementID("network", i), map[string]any{
			"name": value.Name, "circuits": xTorMenuDefault(value.Circuits),
			"isolateDestinationAddress": value.IsolateDestinationAddress,
			"isolateDestinationPort":    value.IsolateDestinationPort,
		}))
	}
	return out
}

func (n *xTorDriverNode) bridgesBlock() formular.Block {
	templates := []formular.ArrayTemplate{xTorPlainBridgeTemplate(), xTorObfs4BridgeTemplate()}
	return xTorFormBlock(xTorBridgesBlockID, 60, true, true, []formular.Item{
		n.xTorCheckbox(xTorBridgesBlockID, xTorFieldUseBridges, "Use bridges", n.config.Driver.UseBridges,
			"Routes Tor bootstrap and relay traffic through the configured bridges."),
		n.xTorCheckbox(xTorBridgesBlockID, xTorFieldEnableObfs4, "Enable obfs4", n.config.Driver.EnableObfs4,
			"Enables the protected obfs4 or lyrebird transport executable."),
		n.xTorArray(xTorBridgesBlockID, xTorFieldBridges, "Bridge lines", templates, xTorBridgeElements(templates, n.config.Driver.Bridges),
			"Add plain or obfs4 bridges. Enable bridge use before applying a non-empty list."),
	})
}

func xTorPlainBridgeTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{Name: "plain", Label: "Plain bridge", Items: []formular.Item{
		formular.TextField("address", "Address", "", formular.Required, formular.Help("Numeric bridge IP address and port.")),
		formular.TextField("fingerprint", "Fingerprint", "", formular.Help("Optional 40-character relay fingerprint.")),
	}}
}

func xTorObfs4BridgeTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{Name: "obfs4", Label: "obfs4 bridge", Items: []formular.Item{
		formular.TextField("address", "Address", "", formular.Required, formular.Help("Numeric bridge IP address and port.")),
		formular.TextField("fingerprint", "Fingerprint", "", formular.Required, formular.Help("40-character relay fingerprint.")),
		formular.TextField("obfs4Certificate", "obfs4 certificate", "", formular.Required,
			formular.Help("Base64 obfs4 certificate from the bridge line.")),
		formular.RadioField("iat", "Inter-arrival timing", "default", xTorChoices("default", "disabled", "enabled", "paranoid")),
	}}
}

func xTorBridgeElements(templates []formular.ArrayTemplate, values []XTorDriverBridge) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		template := templates[0]
		if value.Transport == "obfs4" {
			template = templates[1]
		}
		out = append(out, formular.InstantiateArrayTemplate(template, xTorElementID("bridge", i), map[string]any{
			"address": value.Address, "fingerprint": value.Fingerprint,
			"obfs4Certificate": value.Obfs4Certificate, "iat": xTorMenuDefault(value.IAT),
		}))
	}
	return out
}

func (n *xTorDriverNode) servicesBlock() formular.Block {
	template := xTorServiceTemplate()
	return xTorFormBlock(xTorServicesBlockID, 70, false, false, []formular.Item{
		n.xTorArray(xTorServicesBlockID, xTorFieldServices, "Onion services", []formular.ArrayTemplate{template}, xTorServiceElements(template, n.config.Services),
			"Each entry creates one persistent v3 onion service and one listen-only Network output."),
	})
}

func xTorServiceTemplate() formular.ArrayTemplate {
	key := formular.TextField("keySecret", "Private key secret", "", formular.Selector(nodekit.SecretNameSelector),
		formular.Help("Empty creates a new node-scoped private key secret."))
	maxStreams := formular.IntField("maxStreams", "Maximum streams", 0, formular.Help("Zero uses the Tor default."))
	minimum, maximum := float64(0), float64(65535)
	maxStreams.Min, maxStreams.Max = &minimum, &maximum
	return formular.ArrayTemplate{Name: "service", Label: "Onion service", Items: []formular.Item{
		formular.TextField("name", "Name", "", formular.Required, formular.Help("Used in the graph port name.")),
		formular.TextField("ports", "Virtual ports", "80", formular.Required,
			formular.Help("Comma- or space-separated onion service ports.")),
		maxStreams,
		formular.RadioField("maxStreamsPolicy", "Stream limit policy", "default", xTorChoices("default", "keep", "close")),
		key,
		formular.MultilineTextField("authorizedClients", "Authorized client public keys", "",
			formular.Help("One v3 client authorization public key per line.")),
	}}
}

func xTorServiceElements(template formular.ArrayTemplate, values []XTorDriverServiceConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, xTorElementID("service", i), map[string]any{
			"name": value.Name, "ports": xTorPortsText(value.Ports), "maxStreams": int(value.MaxStreams),
			"maxStreamsPolicy": xTorMenuDefault(value.MaxStreamsPolicy), "keySecret": value.KeySecret,
			"authorizedClients": strings.Join(value.AuthorizedClients, "\n"),
		}))
	}
	return out
}

func (n *xTorDriverNode) clientAuthorizationBlock() formular.Block {
	template := xTorClientAuthorizationTemplate()
	return xTorFormBlock(xTorClientAuthBlockID, 80, true, true, []formular.Item{
		n.xTorArray(xTorClientAuthBlockID, xTorFieldClientAuthorizations, "Client authorization", []formular.ArrayTemplate{template},
			xTorClientAuthorizationElements(template, n.config.ClientAuthorizations),
			"Adds private authorization keys for connecting to protected v3 onion services."),
	})
}

func xTorClientAuthorizationTemplate() formular.ArrayTemplate {
	secret := formular.TextField("privateKeySecret", "Private key secret", "", formular.Selector(nodekit.SecretNameSelector),
		formular.Help("Empty creates a new node-scoped X25519 private key secret."))
	return formular.ArrayTemplate{Name: "authorization", Label: "Authorization", Items: []formular.Item{
		formular.TextField("address", "Onion address", "", formular.Required),
		formular.TextField("name", "Key name", "", formular.Help("Optional Tor authorization name.")),
		secret,
	}}
}

func xTorClientAuthorizationElements(template formular.ArrayTemplate, values []XTorDriverClientAuthConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, xTorElementID("authorization", i), map[string]any{
			"address": value.Address, "name": value.Name,
			"privateKeySecret": value.PrivateKeySecret,
		}))
	}
	return out
}

func xTorFormBlock(id string, order int, collapsible, collapsed bool, items []formular.Item) formular.Block {
	return formular.Block{
		ID: id, Order: order, Generation: 1, Form: true,
		Collapsible: collapsible, Collapsed: collapsed, Items: items,
	}
}

func (n *xTorDriverNode) xTorText(block, id, label, value, help string) formular.Item {
	return formular.TextField(id, label, value, n.xTorFieldOptions(block, help)...)
}

func (n *xTorDriverNode) xTorUintText(block, id, label string, value uint64, help string) formular.Item {
	return n.xTorText(block, id, label, strconv.FormatUint(value, 10), help)
}

func (n *xTorDriverNode) xTorMultiline(block, id, label, value, help string) formular.Item {
	return formular.MultilineTextField(id, label, value, n.xTorFieldOptions(block, help)...)
}

func (n *xTorDriverNode) xTorCheckbox(block, id, label string, value bool, help string) formular.Item {
	return formular.CheckboxField(id, label, value, n.xTorFieldOptions(block, help)...)
}

func (n *xTorDriverNode) xTorRadio(block, id, label, value string, choices []string, help string) formular.Item {
	return formular.RadioField(id, label, value, xTorChoices(choices...), n.xTorFieldOptions(block, help)...)
}

func (n *xTorDriverNode) xTorInt(block, id, label string, value, minimum, maximum int, help string) formular.Item {
	item := formular.IntField(id, label, value, n.xTorFieldOptions(block, help)...)
	minValue, maxValue := float64(minimum), float64(maximum)
	item.Min, item.Max = &minValue, &maxValue
	return item
}

func (n *xTorDriverNode) xTorArray(block, id, label string, templates []formular.ArrayTemplate, elements []formular.ArrayElement, help string) formular.Item {
	field := &formular.Field{Kind: formular.FieldArray, Templates: templates, Elements: elements}
	if n.formErrorBlock == block && n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Help: help, Field: field}
}

func (n *xTorDriverNode) xTorFieldOptions(block, help string) []formular.FieldOption {
	options := make([]formular.FieldOption, 0, 3)
	if help != "" {
		options = append(options, formular.Help(help))
	}
	if n.formErrorBlock == block && n.formError != "" {
		options = append(options, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return options
}

func xTorChoices(values ...string) []formular.Choice {
	out := make([]formular.Choice, len(values))
	for i, value := range values {
		out[i] = formular.Choice{Value: value}
	}
	return out
}

func xTorMenuDefault(value string) string {
	if value == "" {
		return "default"
	}
	return value
}

func xTorPortsText[T ~uint16](ports []T) string {
	values := make([]string, len(ports))
	for i, port := range ports {
		values[i] = strconv.FormatUint(uint64(port), 10)
	}
	return strings.Join(values, ", ")
}

func xTorElementID(kind string, index int) string {
	return fmt.Sprintf("%s-%d", kind, index+1)
}
