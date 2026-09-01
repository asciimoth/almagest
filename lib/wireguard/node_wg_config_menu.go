package wireguardnodes

import (
	"slices"

	"github.com/asciimoth/formular"
)

func (n *wgConfigNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.statusBlock(), n.peersBlock())
}

func (n *wgConfigNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.statusBlock(), n.peersBlock())
}

func (n *wgConfigNode) sendStatusBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.statusBlock())
}

func (n *wgConfigNode) statusBlock() formular.Block {
	errText := n.formError
	if errText == "" {
		errText = n.runtimeError
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: []formular.Item{
		readonlyTextField("state", "State", n.stateText()),
		readonlyTextField("wireGuard", "WireGuard", attachedText(n.runtimeWGReady)),
		readonlyTextField("network", "Network", attachedText(n.runtimeNetworkReady)),
		readonlyTextField("transport", "UDP transport", string(WGConfigTransportID(n.ID))),
		readonlyIntField("peers", "Peers", len(n.peers)),
		readonlyIntField("runtimePeers", "Runtime peers", n.runtimePeerCount),
		readonlyTextField("error", "Error", errText),
	}}
}

func (n *wgConfigNode) peersBlock() formular.Block {
	field := &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{wireGuardPeerTemplate()},
		Elements:  wireGuardPeerElements(n.peers),
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: "peers", Order: 20, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: "peers", Label: "Peers", Field: field},
	}}
}

func (n *wgConfigNode) sendPeersFieldStatus(msg formular.FieldValidateMessage) {
	if len(msg.Field.ElementPath) == 0 {
		return
	}
	peers := slices.Clone(n.peers)
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
		peers[index].publicKey, _ = formular.StringValue(msg.Value)
	case "presharedKey":
		peers[index].presharedKeySecret = ""
		peers[index].presharedKey, _ = formular.StringValue(msg.Value)
	case "presharedKeySecret":
		peers[index].presharedKeySecret, _ = formular.StringValue(msg.Value)
		peers[index].presharedKey = ""
	case "endpoint":
		peers[index].endpoint, _ = formular.StringValue(msg.Value)
	case "allowedIPs":
		peers[index].allowedIPs, _ = formular.StringValue(msg.Value)
	case "persistentKeepaliveInterval":
		peers[index].persistentKeepaliveInterval, _ = formular.IntValue(msg.Value)
	case "amneziaOverride":
		peers[index].amneziaWGOverride, _ = formular.BoolValue(msg.Value)
	default:
		if !applyWireGuardAmneziaValue(&peers[index].amneziaWG, msg.Field.FieldID, msg.Value) {
			return
		}
	}
	for i := range peers {
		peers[i].normalize(i)
	}
	if msg.Field.FieldID == "presharedKeySecret" {
		err := validateWGConfigPresharedKeySecret(n.peerSecretsStorage(), n.currentNodeName(), peers[index])
		sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
		return
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, validateWGConfigPeers(peers))
}

func validateWGConfigPresharedKeySecret(storage SecretsStorage, nodeName string, peer wireGuardPeerConfig) error {
	_, _, err := resolveWireGuardPresharedKeySecret(
		storage, nodeName, peer.presharedKeySecret, peer.presharedKey, peerLabel(peer),
	)
	return err
}
