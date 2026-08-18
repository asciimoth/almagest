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

type tlsWrapperConfig struct {
	tlsConfigName    string
	clientServerName string
	clientALPN       string
	serverName       string
	serverALPN       string
	leafTTLSeconds   int
}

func readTLSWrapperConfig(cfg configer.Config) tlsWrapperConfig {
	return tlsWrapperConfig{
		tlsConfigName:    readTLSConfigName(cfg),
		clientServerName: strings.TrimSpace(readString(cfg, fieldClientServerName, "")),
		clientALPN:       normalizeListText(readString(cfg, fieldClientALPN, "")),
		serverName:       strings.TrimSpace(readString(cfg, fieldServerName, "localhost")),
		serverALPN:       normalizeListText(readString(cfg, fieldServerALPN, "")),
		leafTTLSeconds:   readInt(cfg, fieldLeafTTLSeconds, defaultLeafTTLSeconds),
	}
}

type tlsWrapperNode struct {
	NodeBase

	out uint64
	in  uint64

	config tlsWrapperConfig

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

func newTLSWrapperNode(runtime Runtime, cfg configer.Config) *tlsWrapperNode {
	return &tlsWrapperNode{
		NodeBase: NodeBase{Runtime: runtime},
		config:   readTLSWrapperConfig(cfg),
	}
}

func (n *tlsWrapperNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
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

func (n *tlsWrapperNode) OnReady() error {
	n.requestNetwork()
	n.sendAll()
	return nil
}

func (n *tlsWrapperNode) OnStop() {
	n.outputs.CloseAll()
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkID = 0
}

func (n *tlsWrapperNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
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

func (n *tlsWrapperNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendOutputToLink(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		n.networkID = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *tlsWrapperNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.outputs.Close(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork && link == n.networkID:
		n.outputs.CloseAll()
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkID = 0
	}
	return n.updateLabel()
}

func (n *tlsWrapperNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
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
	n.outputs.CloseAll()
	n.network = next
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsWrapperNode) OnInbox(message pasta.InboxMessage) error {
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
	n.outputs.CloseAll()
	n.sendMenuSnapshot(true)
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsWrapperNode) OnFormularMsg(message any) error {
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

func (n *tlsWrapperNode) OnSave(cfg configer.Config) error {
	return saveNodeConfig(cfg, map[string]any{
		fieldTLSConfig:        n.config.tlsConfigName,
		fieldClientServerName: n.config.clientServerName,
		fieldClientALPN:       n.config.clientALPN,
		fieldServerName:       n.config.serverName,
		fieldServerALPN:       n.config.serverALPN,
		fieldLeafTTLSeconds:   n.config.leafTTLSeconds,
	})
}

func (n *tlsWrapperNode) requestNetwork() {
	if n.networkID != 0 {
		std.RequestLocked(n.W, n.ID, n.networkID)
	}
}

func (n *tlsWrapperNode) applyConfigForm(values map[string]any) error {
	next := tlsWrapperConfig{
		tlsConfigName:    n.config.tlsConfigName,
		clientServerName: strings.TrimSpace(formApplyString(values, fieldClientServerName, n.config.clientServerName)),
		clientALPN:       normalizeListText(formApplyString(values, fieldClientALPN, n.config.clientALPN)),
		serverName:       strings.TrimSpace(formApplyString(values, fieldServerName, n.config.serverName)),
		serverALPN:       normalizeListText(formApplyString(values, fieldServerALPN, n.config.serverALPN)),
		leafTTLSeconds:   formApplyInt(values, fieldLeafTTLSeconds, n.config.leafTTLSeconds),
	}
	if value, ok := tlsConfigValueFromAny(values[fieldTLSConfig]); ok {
		next.tlsConfigName = value
	}
	n.config = next
	n.refreshTLSConfig()
	n.validateConfig()
	n.outputs.CloseAll()
	n.sendMenuSnapshot(true)
	n.sendAll()
	return n.updateLabel()
}

func (n *tlsWrapperNode) refreshTLSConfig() {
	result, err := resolveTLSClientConfig(&n.NodeBase, n.config.tlsConfigName)
	n.tlsConfig = result.Config
	n.tlsConfigEffective = result.EffectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfig = nil
		n.tlsConfigErr = err.Error()
	}
}

func (n *tlsWrapperNode) refreshLocalCA() {
	ca, err := resolveTLSLocalCA(&n.NodeBase)
	n.localCA = ca
	n.localCAErr = ""
	if err != nil {
		n.localCA = tls.Certificate{}
		n.localCAErr = err.Error()
	}
}

func (n *tlsWrapperNode) validateConfig() {
	n.formErr = ""
	if _, err := durationFromSeconds(n.config.leafTTLSeconds); err != nil {
		n.formErr = "leaf TTL: " + err.Error()
		return
	}
	if n.config.serverName == "" {
		n.formErr = "server name is empty"
		return
	}
	if _, _, err := parseLocalCA(n.localCA); err != nil && n.localCAErr == "" {
		n.formErr = err.Error()
	}
}

func (n *tlsWrapperNode) validateField(msg formular.FieldValidateMessage) {
	var err error
	switch msg.Field.FieldID {
	case fieldLeafTTLSeconds:
		_, err = durationFromSeconds(formApplyInt(map[string]any{fieldLeafTTLSeconds: msg.Value}, fieldLeafTTLSeconds, 0))
	case fieldServerName:
		if strings.TrimSpace(formApplyString(map[string]any{fieldServerName: msg.Value}, fieldServerName, "")) == "" {
			err = fmt.Errorf("server name is empty")
		}
	default:
		return
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *tlsWrapperNode) ready() bool {
	return n.formErr == "" && n.tlsConfigErr == "" && n.localCAErr == "" && networkIsReady(n.network)
}

func (n *tlsWrapperNode) sendAll() {
	if !n.ready() {
		return
	}
	broadcastWrappedNetwork(&n.outputs, n.W, n.ID, n.out, n.network, n.wrapNetwork)
}

func (n *tlsWrapperNode) sendOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	emitWrappedNetwork(&n.outputs, n.W, n.ID, link, n.network, n.wrapNetwork)
}

func (n *tlsWrapperNode) refreshOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	refreshWrappedNetwork(&n.outputs, n.W, n.ID, link, n.network, n.wrapNetwork)
}

func (n *tlsWrapperNode) wrapNetwork(input NetworkCloser) (NetworkCloser, error) {
	leafTTL, err := durationFromSeconds(n.config.leafTTLSeconds)
	if err != nil {
		return nil, err
	}
	signer, err := newLocalLeafSigner(n.localCA, leafTTL)
	if err != nil {
		return nil, err
	}

	clientConfig := cloneTLSClientConfig(n.tlsConfig)
	if clientConfig == nil {
		clientConfig = &tls.Config{}
	}
	if n.config.clientServerName != "" {
		clientConfig.ServerName = n.config.clientServerName
	}
	if values := splitListText(n.config.clientALPN); len(values) > 0 {
		clientConfig.NextProtos = values
	}

	serverConfig := &tls.Config{ // #nosec G402 -- Go defaults are used; certificates are generated per SNI.
		MinVersion: tls.VersionTLS12,
		NextProtos: splitListText(n.config.serverALPN),
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := strings.TrimSpace(hello.ServerName)
			if host == "" {
				host = n.config.serverName
			}
			cert, err := signer.certificate(host)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}
	return gonnecttls.NewClientServerNetwork(input, clientConfig, serverConfig)
}

func (n *tlsWrapperNode) sendMenuSnapshot(force bool) {
	sendNodeMenuSnapshot(n.W, n.ID, force, n.configBlock())
}

func (n *tlsWrapperNode) configBlock() formular.Block {
	return formular.Block{ID: configBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		tlsConfigSelector(&n.NodeBase, n.config.tlsConfigName, n.tlsConfigErr),
		configTextFieldPlaceholder(fieldClientServerName, "Client server name", n.config.clientServerName, placeholderClientServerName, false, "", false),
		configTextFieldPlaceholder(fieldClientALPN, "Client ALPN", n.config.clientALPN, placeholderALPN, false, "", true),
		configTextFieldPlaceholder(fieldServerName, "Server name", n.config.serverName, placeholderServerName, n.formErr == "server name is empty", n.formErr, false),
		configTextFieldPlaceholder(fieldServerALPN, "Server ALPN", n.config.serverALPN, placeholderALPN, false, "", true),
		configIntFieldPlaceholder(fieldLeafTTLSeconds, "Leaf TTL seconds", n.config.leafTTLSeconds, placeholderLeafTTLSeconds, strings.HasPrefix(n.formErr, "leaf TTL:"), n.formErr),
	}}
}

func (n *tlsWrapperNode) updateLabel() error {
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
		return n.SetLabel("wrapping TLS")
	}
}
