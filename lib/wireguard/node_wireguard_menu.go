package wireguardnodes

import (
	"maps"
	"strconv"
	"time"

	"github.com/asciimoth/formular"
)

func (n *wireGuardNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.menuBlocks()...)
}
func (n *wireGuardNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.menuBlocks()...)
}
func (n *wireGuardNode) menuBlocks() []formular.Block {
	return []formular.Block{n.statusBlock(), n.configBlock(), n.peersBlock()}
}
func (n *wireGuardNode) sendStatusBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.statusBlock())
}
func (n *wireGuardNode) statusBlock() formular.Block {
	publicKey := ""
	if n.config.privateKey != "" {
		if key, err := parseWireGuardPrivateKey(n.config.privateKey); err == nil && !key.IsZero() {
			publicKey = wireGuardPublicKeyBase64(key)
		}
	}
	status := n.runtimeStatusSnapshot()
	statusErr := n.formError
	if statusErr == "" {
		statusErr = n.runtimeError
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: []formular.Item{
		readonlyTextField("state", "State", n.stateTextFromSnapshot(status)),
		readonlyTextField("publicKey", "Public key", publicKey),
		readonlyIntField("listenPort", "Listen port", status.listenPort),
		readonlyTextField("tun", "TUN", attachedText(status.hasTun)),
		readonlyTextField("network", "Network", attachedText(status.hasNetwork)),
		readonlyIntField("peers", "Peers", len(n.config.peers)),
		readonlyIntField("runtimePeers", "Runtime peers", status.stats.PeerCount),
		readonlyIntField("activePeers", "Active peers", status.stats.ActivePeerCount),
		readonlyIntField("connectedPeers", "Connected peers", status.stats.ConnectedPeerCount),
		readonlyTextField("rxBytes", "RX bytes", wireGuardUint64Text(status.stats.RxBytes)),
		readonlyTextField("txBytes", "TX bytes", wireGuardUint64Text(status.stats.TxBytes)),
		readonlyTextField("rxPackets", "RX packets", wireGuardUint64Text(status.stats.RxPackets)),
		readonlyTextField("txPackets", "TX packets", wireGuardUint64Text(status.stats.TxPackets)),
		readonlyTextField("latestHandshake", "Latest handshake", wireGuardTimeText(status.stats.LastHandshakeTime)),
		readonlyTextField("error", "Error", statusErr),
	}}
}
func (n *wireGuardNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	items := []formular.Item{
		wireGuardPrivateKeySecretField(n.config.privateKeySecret, statusErr, n.formError),
		configIntField("listenPort", "Listen port", n.config.listenPort, statusErr, n.formError),
		wireGuardBatchSizeField(n.config.batchSize, statusErr, n.formError),
		configTextField("ownedIPs", "Owned IPs", n.config.ownedIPs, statusErr, n.formError, true),
	}
	items = append(items, wireGuardAmneziaConfigItems(n.config.amneziaWG, statusErr, n.formError)...)
	return formular.Block{ID: "config", Order: 20, Generation: 1, Form: true, Items: items}
}
func (n *wireGuardNode) peersBlock() formular.Block {
	statusErr := n.formError != ""
	field := &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{wireGuardPeerTemplate()},
		Elements:  wireGuardPeerElements(n.config.peers),
	}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: "peers", Order: 30, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: "peers", Label: "Peers", Field: field},
	}}
}
func (n *wireGuardNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	next := n.config.clone()
	switch msg.Field.FieldID {
	case "privateKeySecret":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.privateKeySecret = value
			next.privateKey = ""
			next.privateKeyLegacyValue = ""
		}
	case "listenPort":
		if value, ok := formular.IntValue(msg.Value); ok {
			next.listenPort = value
		}
	case "batchSize":
		if value, ok := formular.IntValue(msg.Value); ok {
			next.batchSize = value
		}
	case "ownedIPs":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.ownedIPs = value
		}
	default:
		if !applyWireGuardAmneziaValue(&next.amneziaWG, msg.Field.FieldID, msg.Value) {
			return
		}
	}
	next.normalize()
	if msg.Field.FieldID == "privateKeySecret" {
		n.sendFieldStatus(msg, n.validatePrivateKeySecretForForm(next.privateKeySecret))
		return
	}
	n.sendFieldStatus(msg, validateWireGuardConfig(next))
}
func (n *wireGuardNode) sendFormFieldStatus(msg formular.FieldValidateMessage) {
	switch msg.Field.BlockID {
	case "config":
		n.sendConfigFieldStatus(msg)
	case "peers":
		n.sendPeersFieldStatus(msg)
	}
}
func (n *wireGuardNode) sendPeersFieldStatus(msg formular.FieldValidateMessage) {
	if len(msg.Field.ElementPath) == 0 {
		return
	}
	next := n.config.clone()
	peers := next.peers
	element := msg.Field.ElementPath[len(msg.Field.ElementPath)-1]
	index := -1
	for i, peer := range peers {
		if peer.id == element.ElementID {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	switch msg.Field.FieldID {
	case "publicKey":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].publicKey = value
		}
	case "presharedKey":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].presharedKeySecret = ""
			peers[index].presharedKey = value
		}
	case "presharedKeySecret":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].presharedKeySecret = value
			peers[index].presharedKey = ""
		}
	case "endpoint":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].endpoint = value
		}
	case "allowedIPs":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].allowedIPs = value
		}
	case "persistentKeepaliveInterval":
		if value, ok := formular.IntValue(msg.Value); ok {
			peers[index].persistentKeepaliveInterval = value
		}
	case "amneziaOverride":
		if value, ok := formular.BoolValue(msg.Value); ok {
			peers[index].amneziaWGOverride = value
		}
	default:
		if !applyWireGuardAmneziaValue(&peers[index].amneziaWG, msg.Field.FieldID, msg.Value) {
			return
		}
	}
	next.peers = peers
	next.normalize()
	if msg.Field.FieldID == "presharedKeySecret" {
		n.sendFieldStatus(msg, n.validatePresharedKeySecretForForm(peers[index].presharedKeySecret, peerLabel(peers[index])))
		return
	}
	n.sendFieldStatus(msg, validateWireGuardConfig(next))
}
func (n *wireGuardNode) sendFieldStatus(msg formular.FieldValidateMessage, err error) {
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}
func attachedText(attached bool) string {
	if attached {
		return "attached"
	}
	return "detached"
}
func wireGuardBatchSizeField(value int, statusErr bool, statusText string) formular.Item {
	item := configIntField("batchSize", "Batch size", value, statusErr, statusText)
	item.Help = "wgo and batchudp batch size. Use 0 for library defaults; smaller positive values reduce retained buffers and may reduce peak throughput."
	return item
}
func wireGuardPrivateKeySecretField(value string, statusErr bool, statusText string) formular.Item {
	item := configTextField("privateKeySecret", "Private key secret", value, statusErr, statusText, false)
	item.Help = "Secret name containing the WireGuard private key. Empty or inaccessible names are replaced with a newly generated private-key secret."
	return item
}
func wireGuardUint64Text(value uint64) string {
	return strconv.FormatUint(value, 10)
}
func wireGuardTimeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
func wireGuardPeerTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "peer",
		Label: "Peer",
		Items: append([]formular.Item{
			formular.TextField("publicKey", "Public key", "", formular.Validation, formular.Required),
			formular.TextField("presharedKeySecret", "Preshared key secret", "", formular.Validation),
			formular.TextField("endpoint", "Endpoint", "", formular.Validation),
			formular.MultilineTextField("allowedIPs", "Allowed IPs", "", formular.Validation),
			formular.IntField("persistentKeepaliveInterval", "Keepalive", 0, formular.Validation),
			formular.CheckboxField("amneziaOverride", "AWG override", false),
		}, wireGuardPeerAmneziaTemplateItems()...),
	}
}
func wireGuardAmneziaConfigItems(config wireGuardAmneziaConfig, statusErr bool, statusText string) []formular.Item {
	out := make([]formular.Item, 0, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		out = append(out, configIntField(spec.formID, spec.label, spec.value(config), statusErr, statusText))
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		out = append(out, configTextField(spec.formID, spec.label, spec.value(config), statusErr, statusText, false))
	}
	return out
}
func wireGuardPeerElements(peers []wireGuardPeerConfig) []formular.ArrayElement {
	template := wireGuardPeerTemplate()
	out := make([]formular.ArrayElement, 0, len(peers))
	for i, peer := range peers {
		peer.normalize(i)
		out = append(out, formular.InstantiateArrayTemplate(template, peer.id, wireGuardPeerFormValues(peer)))
	}
	return out
}
func wireGuardPeerFormValues(peer wireGuardPeerConfig) map[string]any {
	values := map[string]any{
		"publicKey":                   peer.publicKey,
		"presharedKeySecret":          peer.presharedKeySecret,
		"endpoint":                    peer.endpoint,
		"allowedIPs":                  peer.allowedIPs,
		"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
		"amneziaOverride":             peer.amneziaWGOverride,
	}
	maps.Copy(values, wireGuardAmneziaElementValues(peer.amneziaWG))
	return values
}
func wireGuardAmneziaElementValues(config wireGuardAmneziaConfig) map[string]any {
	values := make(map[string]any, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		values[spec.formID] = spec.value(config)
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		values[spec.formID] = spec.value(config)
	}
	return values
}
func wireGuardPeerAmneziaTemplateItems() []formular.Item {
	out := make([]formular.Item, 0, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		out = append(out, formular.IntField(spec.formID, spec.label, 0, formular.Validation))
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		out = append(out, formular.TextField(spec.formID, spec.label, "", formular.Validation))
	}
	return out
}
