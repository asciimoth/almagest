package amnesianodes

import (
	"strconv"
	"time"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

func (n *amnesiaNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.statusBlock(), n.configBlock())
}

func (n *amnesiaNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.statusBlock(), n.configBlock())
}

func (n *amnesiaNode) sendStatusBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.statusBlock())
}

func (n *amnesiaNode) sendConfigBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *amnesiaNode) statusBlock() formular.Block {
	errText := n.formError
	if errText == "" {
		errText = n.runtimeError
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: []formular.Item{
		nodekit.ReadonlyTextField("state", "State", n.stateText()),
		nodekit.ReadonlyTextField("tunnel", "Tunnel", n.tunnelStatusText()),
		nodekit.ReadonlyTextField("wireGuard", "WireGuard", attachmentText(n.runtimeWGReady)),
		nodekit.ReadonlyTextField("apiNetwork", "API network", attachmentText(nodekit.NetworkIsReady(n.network))),
		nodekit.ReadonlyBoolField("apiNetworkRequired", "API network required", n.apiNetworkRequired),
		nodekit.ReadonlyTextField("inputFormat", "Input format", n.inputFormat.String()),
		nodekit.ReadonlyTextField("profile", "Profile", n.profileStatus.name),
		nodekit.ReadonlyTextField("country", "Server country", n.profileStatus.serverCountry),
		nodekit.ReadonlyTextField("endpoint", "Peer endpoint", n.profileStatus.endpoint),
		nodekit.ReadonlyTextField("peerPublicKey", "Peer public key", n.profileStatus.publicKey),
		nodekit.ReadonlyStringListField("addresses", "Tunnel addresses", n.profileStatus.addresses),
		nodekit.ReadonlyStringListField("dns", "DNS servers", n.profileStatus.dns),
		nodekit.ReadonlyStringListField("allowedIPs", "Allowed IPs", n.profileStatus.allowedIPs),
		nodekit.ReadonlyTextField("peerTransport", "Peer UDP transport", "WireGuard default"),
		nodekit.ReadonlyBoolField("peerActive", "Peer active", n.peerStatus.Active),
		nodekit.ReadonlyBoolField("peerConnected", "Peer connected", n.peerStatus.Connected),
		nodekit.ReadonlyTextField("rxBytes", "RX bytes", strconv.FormatUint(n.peerStatus.RxBytes, 10)),
		nodekit.ReadonlyTextField("txBytes", "TX bytes", strconv.FormatUint(n.peerStatus.TxBytes, 10)),
		nodekit.ReadonlyTextField("latestHandshake", "Latest handshake", amnesiaTimeText(n.peerStatus.LastHandshakeTime)),
		nodekit.ReadonlyTextField("installationUUID", "Installation UUID", n.installationUUID),
		nodekit.ReadonlyBoolField("uuidPersistent", "Installation UUID persisted", n.uuidPersistent),
		nodekit.ReadonlyTextField("error", "Error", errText),
	}}
}

func (n *amnesiaNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	enabled := nodekit.ConfigCheckbox(fieldEnabled, "Enabled", n.config.enabled)
	enabled.Help = "Import the selected connection and keep its peer on the attached WireGuard node. Disable this option to remove the peer and stop Amnezia API activity."
	connection := nodekit.ConfigTextField(
		fieldConnectionSecret, "Connection secret", n.config.connectionSecret,
		statusErr, n.formError, false,
	)
	connection.Help = "Secret name containing an Amnezia vpn:// URL or native WireGuard/AmneziaWG configuration."
	country := nodekit.ConfigTextField(
		fieldServerCountryCode, "Server country", n.config.serverCountryCode,
		statusErr, n.formError, false,
	)
	country.Help = "Optional ISO 3166-1 alpha-2 country code for service profile selection."
	gatewayURL := nodekit.ConfigTextField(
		fieldGatewayURL, "Gateway URL", n.config.gatewayURL,
		statusErr, n.formError, false,
	)
	gatewayURL.Help = "Optional API-v2 gateway URL for a custom Amnezia service. HTTPS uses the selected TLS config."
	gatewayKey := nodekit.ConfigTextField(
		fieldGatewayPublicKeySecret, "Gateway public key secret", n.config.gatewayPublicKeySecret,
		statusErr, n.formError, false,
	)
	gatewayKey.Help = "Optional secret name containing the custom gateway RSA public key in PEM format."
	connectEvent := nodekit.ConfigCheckbox(fieldConnectEvent, "Send connect event", n.config.connectEvent)
	connectEvent.Help = "For service keys, tell the gateway that this profile request starts a connection. This option has no effect on native WireGuard or AmneziaWG profiles."
	reconnect := formular.Item{
		Type: formular.ItemButton, ID: "reconnect", Label: "Reconnect",
		Inactive: n.pending || !n.config.enabled,
		Help:     "Import the connection again and replace this node's tracked peer. This does not rotate the attached WireGuard node's private key.",
	}
	return formular.Block{ID: "config", Order: 20, Generation: 1, Form: true, Items: []formular.Item{
		enabled,
		connection,
		country,
		connectEvent,
		nodekit.TLSConfigSelectorItem(&n.Base, n.config.tlsConfigName, n.tlsConfigErr),
		gatewayURL,
		gatewayKey,
		reconnect,
	}}
}

func (n *amnesiaNode) tunnelStatusText() string {
	switch {
	case n.peerStatus.Connected:
		return "connected"
	case n.peerStatus.Active:
		return "handshaking"
	case n.runtimeReady:
		return "configured"
	default:
		return "inactive"
	}
}

func attachmentText(attached bool) string {
	if attached {
		return "attached"
	}
	return "detached"
}

func amnesiaTimeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
