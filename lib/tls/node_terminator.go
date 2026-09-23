package tlsnodes

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	gonnecttls "github.com/asciimoth/gonnect/tls"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type tlsTerminatorConfig struct {
	sniffBufferSize   int
	leafTTLSeconds    int
	useSNIHostname    bool
	nextProtos        string
	destinationRemaps string
}

func readTLSTerminatorConfig(cfg configer.Config) tlsTerminatorConfig {
	return tlsTerminatorConfig{
		sniffBufferSize:   readInt(cfg, fieldSniffBufferSize, 0),
		leafTTLSeconds:    readInt(cfg, fieldLeafTTLSeconds, defaultLeafTTLSeconds),
		useSNIHostname:    readBool(cfg, fieldUseSNIHostname, false),
		nextProtos:        normalizeListText(readString(cfg, fieldServerALPN, "")),
		destinationRemaps: readString(cfg, fieldDestinationRemaps, defaultRemaps),
	}
}

type tlsTerminatorNode struct {
	NodeBase

	out uint64
	in  uint64

	config tlsTerminatorConfig

	network   NetworkCloser
	networkID uint64
	outputs   NetworkOutputCache

	localCA    tls.Certificate
	localCAErr string
	formErr    string
}

func newTLSTerminatorNode(runtime Runtime, cfg configer.Config) *tlsTerminatorNode {
	return &tlsTerminatorNode{
		NodeBase: NodeBase{Runtime: runtime},
		config:   readTLSTerminatorConfig(cfg),
	}
}

func (n *tlsTerminatorNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.in = restoredRight(restored, 0)
	n.refreshLocalCA()
	n.validateConfig()
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.sendMenuSnapshot(false)
	return n.updateLabel()
}

func (n *tlsTerminatorNode) OnReady() error {
	n.requestNetwork()
	n.sendAll()
	return nil
}

func (n *tlsTerminatorNode) OnStop() {
	n.outputs.CloseAll()
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkID = 0
}

func (n *tlsTerminatorNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *tlsTerminatorNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendOutputToLink(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		n.networkID = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *tlsTerminatorNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.outputs.Close(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork && link == n.networkID:
		n.outputs.InvalidateAllLocked(n.W, n.ID)
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkID = 0
	}
	return n.updateLabel()
}

func (n *tlsTerminatorNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshOutputToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork || event.Link != n.networkID {
		return nil
	}
	next, changed := acceptNetworkPayload(n.W, n.ID, event, n.network)
	if !changed {
		return nil
	}
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.network = next
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsTerminatorNode) OnInbox(message pasta.InboxMessage) error {
	if _, ok := message.Payload.(TlsLocalCAChanged); !ok {
		return nil
	}
	n.refreshLocalCA()
	n.validateConfig()
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.sendMenuSnapshot(true)
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsTerminatorNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != configBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != configBlockID || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.validateField(msg)
	}
	return nil
}

func (n *tlsTerminatorNode) OnSave(cfg configer.Config) error {
	return saveNodeConfig(cfg, map[string]any{
		fieldSniffBufferSize:   n.config.sniffBufferSize,
		fieldLeafTTLSeconds:    n.config.leafTTLSeconds,
		fieldUseSNIHostname:    n.config.useSNIHostname,
		fieldServerALPN:        n.config.nextProtos,
		fieldDestinationRemaps: n.config.destinationRemaps,
	})
}

func (n *tlsTerminatorNode) requestNetwork() {
	if n.networkID != 0 {
		std.RequestLocked(n.W, n.ID, n.networkID)
	}
}

func (n *tlsTerminatorNode) applyConfigForm(values map[string]any) error {
	n.config = tlsTerminatorConfig{
		sniffBufferSize:   formApplyInt(values, fieldSniffBufferSize, n.config.sniffBufferSize),
		leafTTLSeconds:    formApplyInt(values, fieldLeafTTLSeconds, n.config.leafTTLSeconds),
		useSNIHostname:    formApplyBool(values, fieldUseSNIHostname, n.config.useSNIHostname),
		nextProtos:        normalizeListText(formApplyString(values, fieldServerALPN, n.config.nextProtos)),
		destinationRemaps: formApplyString(values, fieldDestinationRemaps, n.config.destinationRemaps),
	}
	n.validateConfig()
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.sendMenuSnapshot(true)
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsTerminatorNode) refreshLocalCA() {
	ca, err := resolveTLSLocalCA(&n.NodeBase)
	n.localCA = ca
	n.localCAErr = ""
	if err != nil {
		n.localCA = tls.Certificate{}
		n.localCAErr = err.Error()
	}
}

func (n *tlsTerminatorNode) validateConfig() {
	n.formErr = ""
	if n.config.sniffBufferSize < 0 {
		n.formErr = "sniff buffer size cannot be negative"
		return
	}
	if _, err := durationFromSeconds(n.config.leafTTLSeconds); err != nil {
		n.formErr = "leaf TTL: " + err.Error()
		return
	}
	if _, err := parseTerminatorRemaps(n.config.destinationRemaps); err != nil {
		n.formErr = "destination remaps: " + err.Error()
		return
	}
	if _, _, err := parseLocalCA(n.localCA); err != nil && n.localCAErr == "" {
		n.formErr = err.Error()
	}
}

func (n *tlsTerminatorNode) validateField(msg formular.FieldValidateMessage) {
	var err error
	switch msg.Field.FieldID {
	case fieldSniffBufferSize:
		if formApplyInt(map[string]any{fieldSniffBufferSize: msg.Value}, fieldSniffBufferSize, 0) < 0 {
			err = fmt.Errorf("sniff buffer size cannot be negative")
		}
	case fieldLeafTTLSeconds:
		_, err = durationFromSeconds(formApplyInt(map[string]any{fieldLeafTTLSeconds: msg.Value}, fieldLeafTTLSeconds, 0))
	case fieldDestinationRemaps:
		_, err = parseTerminatorRemaps(formApplyString(map[string]any{fieldDestinationRemaps: msg.Value}, fieldDestinationRemaps, defaultRemaps))
	default:
		return
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *tlsTerminatorNode) ready() bool {
	return n.formErr == "" && n.localCAErr == "" && networkIsReady(n.network)
}

func (n *tlsTerminatorNode) sendAll() {
	if !n.ready() {
		return
	}
	broadcastWrappedNetwork(&n.outputs, n.W, n.ID, n.out, n.network, n.wrapNetwork)
}

func (n *tlsTerminatorNode) sendOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	emitWrappedNetwork(&n.outputs, n.W, n.ID, link, n.network, n.wrapNetwork)
}

func (n *tlsTerminatorNode) refreshOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	refreshWrappedNetwork(&n.outputs, n.W, n.ID, link, n.network, n.wrapNetwork)
}

func (n *tlsTerminatorNode) wrapNetwork(input NetworkCloser) (NetworkCloser, error) {
	leafTTL, err := durationFromSeconds(n.config.leafTTLSeconds)
	if err != nil {
		return nil, err
	}
	remaps, err := parseTerminatorRemaps(n.config.destinationRemaps)
	if err != nil {
		return nil, err
	}
	return gonnecttls.NewTerminatorWithConfig(input, gonnecttls.TerminatorConfig{
		CA:                cloneTLSCertificate(n.localCA),
		SniffBufferSize:   n.config.sniffBufferSize,
		LeafTTL:           leafTTL,
		DestinationRemaps: remaps,
		UseSNIHostname:    n.config.useSNIHostname,
		NextProtos:        splitListText(n.config.nextProtos),
		Spawner:           n.WorkerSpawner,
	})
}

func (n *tlsTerminatorNode) sendMenuSnapshot(force bool) {
	sendNodeMenuSnapshot(n.W, n.ID, force, n.configBlock())
}

func (n *tlsTerminatorNode) configBlock() formular.Block {
	return formular.Block{ID: configBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configIntFieldPlaceholder(fieldSniffBufferSize, "Sniff buffer bytes", n.config.sniffBufferSize, placeholderSniffBufferBytes, strings.Contains(n.formErr, "sniff buffer"), n.formErr),
		configIntFieldPlaceholder(fieldLeafTTLSeconds, "Leaf TTL seconds", n.config.leafTTLSeconds, placeholderLeafTTLSeconds, strings.HasPrefix(n.formErr, "leaf TTL:"), n.formErr),
		configCheckbox(fieldUseSNIHostname, "Use SNI hostname", n.config.useSNIHostname),
		configTextFieldPlaceholder(fieldServerALPN, "Client-facing ALPN", n.config.nextProtos, placeholderALPN, false, "", true),
		configTextFieldPlaceholder(fieldDestinationRemaps, "Destination remaps JSON", n.config.destinationRemaps, placeholderDestinationRemaps, strings.HasPrefix(n.formErr, "destination remaps:"), n.formErr, true),
	}}
}

func (n *tlsTerminatorNode) updateLabel() error {
	switch {
	case n.formErr != "":
		return n.SetLabel("invalid config")
	case n.localCAErr != "":
		return n.SetLabel("local TLS CA unavailable")
	case !networkIsReady(n.network):
		return n.SetLabel("waiting for network")
	default:
		return n.SetLabel("terminating TLS")
	}
}
