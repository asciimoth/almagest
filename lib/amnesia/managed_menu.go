package amnesianodes

import (
	"strconv"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

func (n *managedAmnesiaNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.statusBlock(), n.configBlock())
}

func (n *managedAmnesiaNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.statusBlock(), n.configBlock())
}

func (n *managedAmnesiaNode) sendStatusBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.statusBlock())
}

func (n *managedAmnesiaNode) sendConfigBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *managedAmnesiaNode) statusBlock() formular.Block {
	errText := n.formError
	if errText == "" {
		errText = n.runtimeError
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: []formular.Item{
		nodekit.ReadonlyTextField("state", "State", n.stateText()),
		nodekit.ReadonlyTextField("inputFormat", "Input format", n.inputFormat.String()),
		nodekit.ReadonlyTextField("profile", "Profile", n.profileStatus.name),
		nodekit.ReadonlyTextField("country", "Server country", n.profileStatus.serverCountry),
		nodekit.ReadonlyStringListField("addresses", "Tunnel addresses", n.profileStatus.addresses),
		nodekit.ReadonlyStringListField("dns", "DNS servers", n.profileStatus.dns),
		nodekit.ReadonlyTextField("installationUUID", "UUID", n.installationUUID),
		nodekit.ReadonlyTextField("rxBytes", "RX bytes", strconv.FormatUint(n.runtimeStats.RxBytes, 10)),
		nodekit.ReadonlyTextField("txBytes", "TX bytes", strconv.FormatUint(n.runtimeStats.TxBytes, 10)),
		nodekit.ReadonlyTextField("error", "Error", errText),
	}}
}

func (n *managedAmnesiaNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	enabled := nodekit.ConfigCheckbox(fieldEnabled, "Enabled", n.config.enabled)
	enabled.Help = "Create and keep an Amnezia connection while both ports are attached."
	connection := nodekit.ConfigSecretNameField(
		fieldConnectionSecret, "Connection secret", n.config.connectionSecret,
		statusErr, n.formError,
	)
	connection.Help = "Secret name containing an Amnezia vpn:// URL or native WireGuard/AmneziaWG configuration."
	country := nodekit.ConfigTextField(
		fieldServerCountryCode, "Server country", n.config.serverCountryCode,
		statusErr, n.formError, false,
	)
	country.Help = "Optional ISO 3166-1 alpha-2 country code for service profile selection. If the tunnel cannot complete a handshake, try another country first because an endpoint can be temporarily unavailable."
	gatewayURL := nodekit.ConfigTextField(
		fieldGatewayURL, "Gateway URL", n.config.gatewayURL,
		statusErr, n.formError, false,
	)
	gatewayURL.Help = "Optional API-v2 gateway URL for a custom Amnezia service."
	gatewayKey := nodekit.ConfigSecretNameField(
		fieldGatewayPublicKeySecret, "Gateway public key secret", n.config.gatewayPublicKeySecret,
		statusErr, n.formError,
	)
	gatewayKey.Help = "Optional secret name containing the custom gateway RSA public key in PEM format."
	return formular.Block{ID: "config", Order: 20, Generation: 1, Form: true, Items: []formular.Item{
		enabled,
		connection,
		country,
		nodekit.TLSConfigSelectorItem(&n.Base, n.config.tlsConfigName, n.tlsConfigErr),
		gatewayURL,
		gatewayKey,
	}}
}
