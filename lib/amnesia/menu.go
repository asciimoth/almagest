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
		nodekit.ReadonlyTextField("mtu", "Profile MTU ceiling", strconv.Itoa(n.profileStatus.profileMTU)),
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
	connection := nodekit.ConfigSecretNameField(
		fieldConnectionSecret, "Connection secret", n.config.connectionSecret,
		statusErr, n.formError,
	)
	country := nodekit.ConfigTextField(
		fieldServerCountryCode, "Server country", n.config.serverCountryCode,
		statusErr, n.formError, false,
	)
	gatewayURL := nodekit.ConfigTextField(
		fieldGatewayURL, "Gateway URL", n.config.gatewayURL,
		statusErr, n.formError, false,
	)
	gatewayKey := nodekit.ConfigSecretNameField(
		fieldGatewayPublicKeySecret, "Gateway public key secret", n.config.gatewayPublicKeySecret,
		statusErr, n.formError,
	)
	connectEvent := nodekit.ConfigCheckbox(fieldConnectEvent, "Send connect event", n.config.connectEvent)
	reconnect := formular.Item{
		Type: formular.ItemButton, ID: "reconnect", Label: "Reconnect",
		Inactive: n.pending || !n.config.enabled,
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
