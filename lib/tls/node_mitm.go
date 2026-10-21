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

type tlsMITMConfig struct {
	tlsConfigName   string
	sniffBufferSize int
	leafTTLSeconds  int
	filterMode      string
	filterRules     string
}

func readTLSMITMConfig(cfg configer.Config) tlsMITMConfig {
	return tlsMITMConfig{
		tlsConfigName:   readTLSConfigName(cfg),
		sniffBufferSize: readInt(cfg, fieldSniffBufferSize, 0),
		leafTTLSeconds:  readInt(cfg, fieldLeafTTLSeconds, defaultLeafTTLSeconds),
		filterMode:      formatInterceptionMode(readString(cfg, fieldFilterMode, "off")),
		filterRules:     readString(cfg, fieldFilterRules, defaultFilterRules),
	}
}

type tlsMITMNode struct {
	NodeBase

	out uint64
	in  uint64

	config tlsMITMConfig

	network   NetworkCloser
	networkID uint64
	outputs   NetworkOutputCache

	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string
	localCA            tls.Certificate
	localCAErr         string
	formErr            string
}

func newTLSMITMNode(runtime Runtime, cfg configer.Config) *tlsMITMNode {
	return &tlsMITMNode{
		NodeBase: NodeBase{Runtime: runtime},
		config:   readTLSMITMConfig(cfg),
	}
}

func (n *tlsMITMNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.in = restoredRight(restored, 0)
	n.refreshTLSConfig()
	n.refreshLocalCA()
	n.validateConfig()
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.sendMenuSnapshot(false)
	return n.updateLabel()
}

func (n *tlsMITMNode) OnReady() error {
	n.requestNetwork()
	n.sendAll()
	return nil
}

func (n *tlsMITMNode) OnStop() {
	n.outputs.CloseAll()
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkID = 0
}

func (n *tlsMITMNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
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

func (n *tlsMITMNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendOutputToLink(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		n.networkID = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *tlsMITMNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
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

func (n *tlsMITMNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
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

func (n *tlsMITMNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case TlsConfigChanged:
		nextName, relevant := handleTLSConfigChanged(n.config.tlsConfigName, n.tlsConfigEffective, payload)
		if !relevant {
			return nil
		}
		n.config.tlsConfigName = nextName
		n.refreshTLSConfig()
	case TlsLocalCAChanged:
		n.refreshLocalCA()
	default:
		return nil
	}
	n.validateConfig()
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.sendMenuSnapshot(true)
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsMITMNode) OnFormularMsg(message any) error {
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

func (n *tlsMITMNode) OnSave(cfg configer.Config) error {
	return saveNodeConfig(cfg, map[string]any{
		fieldTLSConfig:       n.config.tlsConfigName,
		fieldSniffBufferSize: n.config.sniffBufferSize,
		fieldLeafTTLSeconds:  n.config.leafTTLSeconds,
		fieldFilterMode:      n.config.filterMode,
		fieldFilterRules:     n.config.filterRules,
	})
}

func (n *tlsMITMNode) requestNetwork() {
	if n.networkID != 0 {
		std.RequestLocked(n.W, n.ID, n.networkID)
	}
}

func (n *tlsMITMNode) applyConfigForm(values map[string]any) error {
	next := tlsMITMConfig{
		tlsConfigName:   n.config.tlsConfigName,
		sniffBufferSize: formApplyInt(values, fieldSniffBufferSize, n.config.sniffBufferSize),
		leafTTLSeconds:  formApplyInt(values, fieldLeafTTLSeconds, n.config.leafTTLSeconds),
		filterMode:      formatInterceptionMode(formApplyString(values, fieldFilterMode, n.config.filterMode)),
		filterRules:     formApplyString(values, fieldFilterRules, n.config.filterRules),
	}
	if value, ok := tlsConfigValueFromAny(values[fieldTLSConfig]); ok {
		next.tlsConfigName = value
	}
	n.config = next
	n.refreshTLSConfig()
	n.validateConfig()
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.sendMenuSnapshot(true)
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsMITMNode) refreshTLSConfig() {
	result, err := resolveTLSClientConfig(&n.NodeBase, n.config.tlsConfigName)
	n.tlsConfig = result.Config
	n.tlsConfigEffective = result.EffectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfig = nil
		n.tlsConfigErr = err.Error()
	}
}

func (n *tlsMITMNode) refreshLocalCA() {
	ca, err := resolveTLSLocalCA(&n.NodeBase)
	n.localCA = ca
	n.localCAErr = ""
	if err != nil {
		n.localCA = tls.Certificate{}
		n.localCAErr = err.Error()
	}
}

func (n *tlsMITMNode) validateConfig() {
	n.formErr = ""
	if n.config.sniffBufferSize < 0 {
		n.formErr = "sniff buffer size cannot be negative"
		return
	}
	if _, err := durationFromSeconds(n.config.leafTTLSeconds); err != nil {
		n.formErr = "leaf TTL: " + err.Error()
		return
	}
	if _, err := parseInterceptionMode(n.config.filterMode); err != nil {
		n.formErr = "filter mode: " + err.Error()
		return
	}
	if _, err := parseInterceptionRules(n.config.filterRules); err != nil {
		n.formErr = "filter rules: " + err.Error()
		return
	}
	if _, _, err := parseLocalCA(n.localCA); err != nil && n.localCAErr == "" {
		n.formErr = err.Error()
	}
}

func (n *tlsMITMNode) validateField(msg formular.FieldValidateMessage) {
	var err error
	switch msg.Field.FieldID {
	case fieldSniffBufferSize:
		if formApplyInt(map[string]any{fieldSniffBufferSize: msg.Value}, fieldSniffBufferSize, 0) < 0 {
			err = fmt.Errorf("sniff buffer size cannot be negative")
		}
	case fieldLeafTTLSeconds:
		_, err = durationFromSeconds(formApplyInt(map[string]any{fieldLeafTTLSeconds: msg.Value}, fieldLeafTTLSeconds, 0))
	case fieldFilterMode:
		_, err = parseInterceptionMode(formApplyString(map[string]any{fieldFilterMode: msg.Value}, fieldFilterMode, "off"))
	case fieldFilterRules:
		_, err = parseInterceptionRules(formApplyString(map[string]any{fieldFilterRules: msg.Value}, fieldFilterRules, defaultFilterRules))
	default:
		return
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *tlsMITMNode) ready() bool {
	return n.formErr == "" && n.tlsConfigErr == "" && n.localCAErr == "" && networkIsReady(n.network)
}

func (n *tlsMITMNode) sendAll() {
	if !n.ready() {
		return
	}
	broadcastWrappedNetwork(&n.outputs, n.W, n.ID, n.out, n.network, n.wrapNetwork)
}

func (n *tlsMITMNode) sendOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	emitWrappedNetwork(&n.outputs, n.W, n.ID, link, n.network, n.wrapNetwork)
}

func (n *tlsMITMNode) refreshOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	refreshWrappedNetwork(&n.outputs, n.W, n.ID, link, n.network, n.wrapNetwork)
}

func (n *tlsMITMNode) wrapNetwork(input NetworkCloser) (NetworkCloser, error) {
	leafTTL, err := durationFromSeconds(n.config.leafTTLSeconds)
	if err != nil {
		return nil, err
	}
	mode, err := parseInterceptionMode(n.config.filterMode)
	if err != nil {
		return nil, err
	}
	rules, err := parseInterceptionRules(n.config.filterRules)
	if err != nil {
		return nil, err
	}
	return gonnecttls.NewNetworkWithConfig(input, gonnecttls.Config{
		CA:              cloneTLSCertificate(n.localCA),
		ClientConfig:    cloneTLSClientConfig(n.tlsConfig),
		SniffBufferSize: n.config.sniffBufferSize,
		LeafTTL:         leafTTL,
		InterceptionFilter: gonnecttls.InterceptionFilter{
			Mode:  mode,
			Rules: rules,
		},
		Spawner: n.WorkerSpawner,
	})
}

func (n *tlsMITMNode) sendMenuSnapshot(force bool) {
	sendNodeMenuSnapshot(n.W, n.ID, force, n.configBlock())
}

func (n *tlsMITMNode) configBlock() formular.Block {
	filterRules := configTextFieldPlaceholder(fieldFilterRules, "Filter rules JSON", n.config.filterRules, placeholderFilterRules, strings.HasPrefix(n.formErr, "filter rules:"), n.formErr, true)
	filterRules.Subtype = "json"
	return formular.Block{ID: configBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		tlsConfigSelector(&n.NodeBase, n.config.tlsConfigName, n.tlsConfigErr),
		configIntFieldPlaceholder(fieldSniffBufferSize, "Sniff buffer bytes", n.config.sniffBufferSize, placeholderSniffBufferBytes, strings.Contains(n.formErr, "sniff buffer"), n.formErr),
		configIntFieldPlaceholder(fieldLeafTTLSeconds, "Leaf TTL seconds", n.config.leafTTLSeconds, placeholderLeafTTLSeconds, strings.HasPrefix(n.formErr, "leaf TTL:"), n.formErr),
		formular.TextField(fieldFilterMode, "Filter mode", n.config.filterMode, formular.AllowedValues(modeAllowedValues()...), formular.Validation, formular.Placeholder(placeholderFilterMode)),
		filterRules,
	}}
}

func (n *tlsMITMNode) updateLabel() error {
	switch {
	case n.formErr != "":
		return n.SetLabel("invalid config")
	case n.tlsConfigErr != "":
		return n.SetLabel("invalid TLS config")
	case n.localCAErr != "":
		return n.SetLabel("local TLS CA unavailable")
	case !networkIsReady(n.network):
		return n.SetLabel("waiting for network")
	default:
		return n.SetLabel("intercepting TLS")
	}
}
