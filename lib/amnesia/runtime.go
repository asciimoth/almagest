package amnesianodes

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func (n *amnesiaNode) reconcile(req amnesiaRuntimeRequest) (amnesiaRuntimeResult, bool) {
	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	if req.generation != n.generation.Load() {
		n.logDebugf("Amnesia reconcile skipped before start: generation=%d current_generation=%d", req.generation, n.generation.Load())
		return amnesiaRuntimeResult{}, false
	}
	n.logDebugf(
		"Amnesia reconcile started: generation=%d enabled=%t controller_ready=%t network_ready=%t connection_secret_configured=%t country=%q connect_event=%t tls_revision=%d force_revision=%d",
		req.generation, req.config.enabled, wireguardnodes.WGCtrlIsReady(req.controller),
		nodekit.NetworkIsReady(req.network), req.config.connectionSecret != "", req.config.serverCountryCode,
		req.config.connectEvent, req.tlsRevision, req.forceRevision,
	)
	result := amnesiaRuntimeResult{generation: req.generation}
	uuid, persistent, err := n.runtimeInstallationUUID(req.ctx)
	if err != nil {
		n.logDebugf("Amnesia installation UUID unavailable: generation=%d", req.generation)
		n.closeRuntimeSessionLocked()
		result.err = err
		return result, true
	}
	result.installationUUID = uuid
	result.persistent = persistent
	n.logDebugf("Amnesia installation UUID ready: generation=%d uuid=%q persistent=%t", req.generation, uuid, persistent)

	sameController := wireguardnodes.SameWGCtrlInstance(n.runtimeController, req.controller)
	if !sameController || !wireguardnodes.WGCtrlIsReady(n.runtimeController) {
		n.logDebugf(
			"Amnesia controller runtime changing: generation=%d same=%t current_ready=%t requested_ready=%t",
			req.generation, sameController, wireguardnodes.WGCtrlIsReady(n.runtimeController),
			wireguardnodes.WGCtrlIsReady(req.controller),
		)
		n.closeRuntimeControllerLocked()
		if wireguardnodes.WGCtrlIsReady(req.controller) {
			n.runtimeController = req.controller
			n.logDebugf("Amnesia controller runtime attached: generation=%d", req.generation)
		}
	}
	result.wgReady = wireguardnodes.WGCtrlIsReady(n.runtimeController)
	if !req.config.enabled {
		n.logDebugf("Amnesia reconcile waiting: generation=%d reason=disabled", req.generation)
		n.closeRuntimeSessionLocked()
		return result, true
	}
	if !result.wgReady {
		n.logDebugf("Amnesia reconcile waiting: generation=%d reason=wireguard_controller", req.generation)
		n.closeRuntimeSessionLocked()
		return result, true
	}
	if req.config.connectionSecret == "" {
		n.logDebugf("Amnesia reconcile waiting: generation=%d reason=connection_secret", req.generation)
		n.closeRuntimeSessionLocked()
		return result, true
	}

	n.logDebugf("Amnesia reading connection secret: generation=%d", req.generation)
	input, err := n.readSecret(req.config.connectionSecret, req.nodeName)
	if err != nil {
		n.logDebugf("Amnesia connection secret read failed: generation=%d", req.generation)
		n.closeRuntimeSessionLocked()
		result.err = formatRuntimeError("read connection secret", err)
		return result, true
	}
	parsed, err := wgoamnesia.ParseInput(input)
	if err != nil {
		n.logDebugf("Amnesia connection secret parse failed: generation=%d", req.generation)
		n.closeRuntimeSessionLocked()
		result.err = formatRuntimeError("parse connection secret", err)
		return result, true
	}
	result.format = parsed.Format
	serviceImport := parsed.Format == wgoamnesia.InputFormatServiceKey
	result.requiresNetwork = serviceImport || parsed.Profile != nil && len(parsed.Profile.PreConnect) != 0
	n.logDebugf(
		"Amnesia connection secret parsed: generation=%d format=%s service_import=%t pre_connect_actions=%d network_required=%t",
		req.generation, parsed.Format, serviceImport, amnesiaPreConnectActionCount(parsed.Profile), result.requiresNetwork,
	)

	var gatewayPublicKey []byte
	if result.requiresNetwork && req.config.gatewayPublicKeySecret != "" {
		n.logDebugf("Amnesia reading gateway public key secret: generation=%d", req.generation)
		value, readErr := n.readSecret(req.config.gatewayPublicKeySecret, req.nodeName)
		if readErr != nil {
			n.logDebugf("Amnesia gateway public key secret read failed: generation=%d", req.generation)
			n.closeRuntimeSessionLocked()
			result.err = formatRuntimeError("read gateway public key secret", readErr)
			return result, true
		}
		gatewayPublicKey = []byte(value)
		n.logDebugf("Amnesia gateway public key secret read: generation=%d configured=true", req.generation)
	}
	if serviceImport && req.tlsConfigErr != "" {
		n.logDebugf("Amnesia service import blocked: generation=%d reason=tls_config", req.generation)
		n.closeRuntimeSessionLocked()
		result.err = errors.New("TLS config: " + req.tlsConfigErr)
		return result, true
	}
	if result.requiresNetwork && !nodekit.NetworkIsReady(req.network) {
		n.logDebugf("Amnesia reconcile waiting: generation=%d reason=api_network", req.generation)
		n.closeRuntimeSessionLocked()
		return result, true
	}

	material := amnesiaMaterial(req, parsed.Format, input, gatewayPublicKey)
	if n.runtimeSession != nil && wireguardnodes.WGCtrlIsReady(n.runtimeSession) &&
		n.runtimeMaterial == material &&
		(!result.requiresNetwork || nodekit.SameNetworkInstance(n.runtimeNetwork, req.network)) {
		n.logDebugf("Amnesia reusing current WireGuard session: generation=%d peer_public_key=%s", req.generation, amnesiaPublicKeyText(n.runtimePeerKey))
		return n.currentRuntimeResult(req.generation, result.requiresNetwork), true
	}

	n.logDebugf("Amnesia rebuilding WireGuard session: generation=%d", req.generation)
	n.closeRuntimeSessionLocked()
	if req.generation != n.generation.Load() {
		n.logDebugf("Amnesia reconcile became stale before controller detach: generation=%d current_generation=%d", req.generation, n.generation.Load())
		return amnesiaRuntimeResult{}, false
	}

	session := wgdevice.DetachDevice(n.runtimeController)
	n.logDebugf("Amnesia detached controller session: generation=%d", req.generation)
	profile := parsed.Profile
	var transport *http.Transport
	if serviceImport {
		n.logDebugf(
			"Amnesia creating service client: generation=%d uuid=%q custom_gateway=%t gateway_public_key_configured=%t tls_configured=%t proxy_cache=%t",
			req.generation, uuid, req.config.gatewayURL != "", len(gatewayPublicKey) != 0,
			req.tlsConfig != nil, n.nodeStore != nil,
		)
		transport = amnesiaHTTPTransport(req.network, req.tlsConfig)
		defer transport.CloseIdleConnections()
		apiTransport := newAmnesiaAPIRoundTripper(transport, n.logDebugf)
		clientOptions := wgoamnesia.ClientOptions{
			HTTPClient: &http.Client{Transport: apiTransport},
			// The attached device makes negotiation use its current public key.
			// Do not assume that a new client identity is generated for this
			// import or for a Reconnect request.
			Device: session,
			Metadata: wgoamnesia.ClientMetadata{
				InstallationUUID: uuid,
			},
			ProxyCache: newKVProxyCache(n.nodeStore),
		}
		if req.config.gatewayURL != "" {
			clientOptions.GatewayURL = req.config.gatewayURL
			// A custom deployment must not send credentials to official proxy
			// discovery or fallback endpoints.
			clientOptions.DisableS3Discovery = true
			clientOptions.PrimaryS3URLs = []string{}
			clientOptions.FallbackS3URLs = []string{}
			clientOptions.StaticProxyURLs = []string{}
		}
		if len(gatewayPublicKey) != 0 {
			clientOptions.GatewayPublicKeyPEM = gatewayPublicKey
		}
		client, clientErr := n.newClient(clientOptions)
		if clientErr != nil {
			n.logDebugf("Amnesia service client creation failed: generation=%d", req.generation)
			session.Close()
			result.err = formatRuntimeError("create Amnezia client", clientErr)
			return result, true
		}
		n.logDebugf(
			"Amnesia service profile import starting: generation=%d uuid=%q country=%q connect_event=%t timeout=%s",
			req.generation, uuid, req.config.serverCountryCode, req.config.connectEvent, amnesiaImportTimeout,
		)
		ctx, cancel := context.WithTimeout(req.ctx, amnesiaImportTimeout)
		profile, err = n.importAmnesiaProfile(ctx, client, input, wgoamnesia.NegotiationOptions{
			InteractionPolicy: wgoamnesia.FailOnInteraction,
			ServerCountryCode: req.config.serverCountryCode,
			ConnectEvent:      req.config.connectEvent,
		})
		cancel()
		if err != nil {
			n.logDebugf("Amnesia service profile import failed: generation=%d", req.generation)
			session.Close()
			result.err = amnesiaImportError(err)
			return result, true
		}
		n.logDebugf("Amnesia service profile import complete: generation=%d profile_received=%t", req.generation, profile != nil)
	}
	if profile != nil && len(profile.PreConnect) != 0 {
		if err := executePreConnectActionsLogged(req.ctx, req.network, profile.PreConnect, n.logDebugf); err != nil {
			n.logDebugf("Amnesia pre-connect sequence failed: generation=%d actions=%d", req.generation, len(profile.PreConnect))
			session.Close()
			result.err = err
			return result, true
		}
	}

	n.logDebugf("Amnesia converting profile to tracked peer: generation=%d profile_present=%t", req.generation, profile != nil)
	spec, err := amnesiaProfilePeer(session, profile)
	if err != nil {
		n.logDebugf("Amnesia profile conversion failed: generation=%d", req.generation)
		session.Close()
		result.err = formatRuntimeError("apply profile", err)
		return result, true
	}
	profileState := profileStatus(profile, req.config.serverCountryCode)
	if req.setInterfaceConfig != nil {
		dns := firstAmnesiaDNS(profileState.dns)
		n.logDebugf(
			"Amnesia WGCtrl interface update starting before tracked peer upsert: generation=%d addresses=%v dns=%q",
			req.generation, profileState.addresses, dns,
		)
		if err := req.setInterfaceConfig(req.ctx, req.generation, profileState.addresses, dns); err != nil {
			n.logDebugf("Amnesia WGCtrl interface update failed before tracked peer upsert: generation=%d", req.generation)
			session.Close()
			if req.ctx.Err() != nil || req.generation != n.generation.Load() {
				return amnesiaRuntimeResult{}, false
			}
			result.err = formatRuntimeError("apply WireGuard interface config", err)
			return result, true
		}
		n.logDebugf("Amnesia WGCtrl interface update complete before tracked peer upsert: generation=%d", req.generation)
	}
	peerEndpoint := amnesiaPeerEndpoint(spec)
	peerEndpointHost, peerEndpointPort := amnesiaEndpointHostPort(peerEndpoint)
	n.logDebugf(
		"Amnesia tracked peer configuration prepared: generation=%d peer_public_key=%s endpoint_transport=%q endpoint=%q endpoint_host=%q endpoint_port=%q allowed_ips=%v preshared_key_configured=%t keepalive_seconds=%d keepalive_range=%s %s",
		req.generation, amnesiaPublicKeyText(spec.PublicKey), amnesiaPeerTransport(spec), peerEndpoint,
		peerEndpointHost, peerEndpointPort,
		spec.AllowedIPs, spec.PresharedKey != (wgdevice.NoisePresharedKey{}), spec.PersistentKeepaliveInterval,
		amnesiaOptionalRange(spec.PersistentKeepaliveRange), amnesiaWGDebugSummary(spec.AmneziaWG),
	)
	if req.generation != n.generation.Load() {
		n.logDebugf("Amnesia reconcile became stale before tracked peer upsert: generation=%d current_generation=%d", req.generation, n.generation.Load())
		session.Close()
		return amnesiaRuntimeResult{}, false
	}
	n.logDebugf("Amnesia tracked peer upsert starting: generation=%d peer_public_key=%s", req.generation, amnesiaPublicKeyText(spec.PublicKey))
	if err := session.UpsertTrackedPeer(spec); err != nil {
		n.logDebugf("Amnesia tracked peer upsert failed: generation=%d peer_public_key=%s", req.generation, amnesiaPublicKeyText(spec.PublicKey))
		session.Close()
		result.err = formatRuntimeError("apply tracked peer", err)
		return result, true
	}
	n.logDebugf("Amnesia tracked peer upsert complete: generation=%d peer_public_key=%s", req.generation, amnesiaPublicKeyText(spec.PublicKey))

	n.runtimeSession = session
	n.runtimeNetwork = req.network
	n.runtimeMaterial = material
	n.runtimePeerKey = spec.PublicKey
	n.runtimeProfile = profileState
	n.runtimeFormat = parsed.Format
	n.runtimePersistent = persistent
	n.runtimeUnsubscribe = session.SubscribeRuntimeStats(func(wgdevice.RuntimeStats) {
		n.queuePeerStatus(req.generation, session, spec.PublicKey)
	})
	n.logDebugf("Amnesia peer runtime stats subscribed: generation=%d peer_public_key=%s", req.generation, amnesiaPublicKeyText(spec.PublicKey))

	result.ready = true
	result.profile = n.runtimeProfile
	result.peer, _ = session.PeerSnapshot(spec.PublicKey)
	profileHostname := ""
	if profile != nil {
		profileHostname = profile.HostName
	}
	profileEndpointHost, profileEndpointPort := amnesiaEndpointHostPort(n.runtimeProfile.endpoint)
	n.logDebugf(
		"Amnesia profile applied: generation=%d format=%s name=%q hostname=%q country=%q endpoint=%q endpoint_host=%q endpoint_port=%q addresses=%v dns=%v allowed_ips=%v peer_public_key=%s uuid=%q",
		req.generation, parsed.Format, n.runtimeProfile.name, profileHostname, n.runtimeProfile.serverCountry,
		n.runtimeProfile.endpoint, profileEndpointHost, profileEndpointPort,
		n.runtimeProfile.addresses, n.runtimeProfile.dns, n.runtimeProfile.allowedIPs,
		amnesiaPublicKeyText(spec.PublicKey), uuid,
	)
	return result, true
}

func (n *amnesiaNode) currentRuntimeResult(generation uint64, requiresNetwork bool) amnesiaRuntimeResult {
	peer, _ := n.runtimeSession.PeerSnapshot(n.runtimePeerKey)
	if n.runtimeUnsubscribe != nil {
		n.logDebugf("Amnesia replacing peer runtime stats subscription: generation=%d peer_public_key=%s", generation, amnesiaPublicKeyText(n.runtimePeerKey))
		n.runtimeUnsubscribe()
	}
	session := n.runtimeSession
	key := n.runtimePeerKey
	n.runtimeUnsubscribe = session.SubscribeRuntimeStats(func(wgdevice.RuntimeStats) {
		n.queuePeerStatus(generation, session, key)
	})
	n.logDebugf("Amnesia current session refreshed: generation=%d peer_public_key=%s network_required=%t", generation, amnesiaPublicKeyText(key), requiresNetwork)
	return amnesiaRuntimeResult{
		generation: generation, wgReady: true, ready: true,
		requiresNetwork: requiresNetwork, format: n.runtimeFormat,
		profile: n.runtimeProfile, peer: peer,
		installationUUID: n.runtimeUUID, persistent: n.runtimePersistent,
	}
}

func (n *amnesiaNode) runtimeInstallationUUID(ctx context.Context) (string, bool, error) {
	if n.runtimeUUID != "" {
		n.logDebugf("Amnesia using cached installation UUID: uuid=%q persistent=%t", n.runtimeUUID, n.runtimePersistent)
		return n.runtimeUUID, n.runtimePersistent, nil
	}
	n.logDebugf("Amnesia loading or creating installation UUID: persistent_storage=%t", n.nodeStore != nil)
	uuid, persistent, err := loadOrCreateInstallationUUID(ctx, n.nodeStore, &n.ephemeralID)
	if err != nil {
		return "", false, err
	}
	n.runtimeUUID = uuid
	n.runtimePersistent = persistent
	n.logDebugf("Amnesia installation UUID loaded: uuid=%q persistent=%t", uuid, persistent)
	return uuid, persistent, nil
}

func (n *amnesiaNode) readSecret(name, nodeName string) (string, error) {
	if n.secrets == nil {
		return "", errors.New("secret storage is unavailable")
	}
	secret, err := n.secrets.GetSecret(name, nodeName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(secret.Value) == "" {
		return "", errors.New("secret is empty")
	}
	return secret.Value, nil
}

func amnesiaHTTPTransport(network nodekit.NetworkCloser, tlsConfig *tls.Config) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, networkName, address string) (net.Conn, error) {
			return network.Dial(ctx, networkName, address)
		},
		TLSClientConfig:   nodekit.CloneTLSClientConfig(tlsConfig),
		ForceAttemptHTTP2: true,
	}
}

func amnesiaMaterial(req amnesiaRuntimeRequest, format wgoamnesia.InputFormat, input string, gatewayKey []byte) [32]byte {
	hash := sha256.New()
	writeHashString(hash, string(format))
	writeHashString(hash, input)
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], req.forceRevision)
	_, _ = hash.Write(raw[:])
	if format == wgoamnesia.InputFormatServiceKey {
		writeHashString(hash, req.config.serverCountryCode)
		writeHashString(hash, req.config.gatewayURL)
		writeHashString(hash, req.config.gatewayPublicKeySecret)
		writeHashString(hash, string(gatewayKey))
		binary.LittleEndian.PutUint64(raw[:], req.tlsRevision)
		_, _ = hash.Write(raw[:])
		if req.config.connectEvent {
			_, _ = hash.Write([]byte{1})
		} else {
			_, _ = hash.Write([]byte{0})
		}
	}
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out
}

type stringHashWriter interface {
	Write([]byte) (int, error)
}

func writeHashString(hash stringHashWriter, value string) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write([]byte(value))
}

func amnesiaImportError(err error) error {
	var interaction *wgoamnesia.InteractionRequiredError
	if errors.As(err, &interaction) {
		if interaction.Request.Kind == wgoamnesia.InteractionCaptcha {
			return ErrCaptchaUnsupported
		}
		return fmt.Errorf("amnezia interaction %q is not supported", interaction.Request.Kind)
	}
	return formatRuntimeError("import Amnezia connection", err)
}

func profileStatus(profile *wgoamnesia.Profile, requestedCountry string) amnesiaProfileStatus {
	if profile == nil {
		return amnesiaProfileStatus{}
	}
	country := strings.TrimSpace(profile.API.ServerCountryCode)
	if country == "" {
		country = requestedCountry
	}
	return amnesiaProfileStatus{
		name:          profile.Name,
		serverCountry: country,
		endpoint:      profile.Peer.Endpoint,
		publicKey:     profile.Peer.PublicKey,
		addresses:     append([]string(nil), profile.Interface.Addresses...),
		dns:           append([]string(nil), profile.Interface.DNS...),
		allowedIPs:    append([]string(nil), profile.Peer.AllowedIPs...),
	}
}

func (n *amnesiaNode) queuePeerStatus(generation uint64, session wgdevice.DeviceAPI, key wgdevice.NoisePublicKey) {
	if n.W == nil || n.ID == 0 || session == nil {
		n.logDebugf("Amnesia peer status not queued: generation=%d workspace_ready=%t session_ready=%t", generation, n.W != nil && n.ID != 0, session != nil)
		return
	}
	peer, ok := session.PeerSnapshot(key)
	if !ok {
		n.logDebugf("Amnesia peer status snapshot unavailable: generation=%d peer_public_key=%s", generation, amnesiaPublicKeyText(key))
		return
	}
	n.logDebugf("Amnesia peer status queued: generation=%d peer_public_key=%s revision=%d", generation, amnesiaPublicKeyText(key), peer.Revision)
	n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: amnesiaPeerStatus{
		generation: generation, peer: peer,
	}})
}

func (n *amnesiaNode) closeRuntimeSessionLocked() {
	if n.runtimeUnsubscribe != nil {
		n.logDebugf("Amnesia peer runtime stats unsubscribing: peer_public_key=%s", amnesiaPublicKeyText(n.runtimePeerKey))
		n.runtimeUnsubscribe()
		n.runtimeUnsubscribe = nil
	}
	if n.runtimeSession != nil {
		n.logDebugf("Amnesia closing WireGuard session and removing tracked resources: peer_public_key=%s", amnesiaPublicKeyText(n.runtimePeerKey))
		n.runtimeSession.Close()
	}
	n.runtimeSession = nil
	n.runtimeNetwork = nil
	n.runtimeMaterial = [32]byte{}
	n.runtimePeerKey = wgdevice.NoisePublicKey{}
	n.runtimeProfile = amnesiaProfileStatus{}
	n.runtimeFormat = wgoamnesia.InputFormatUnknown
}

func (n *amnesiaNode) closeRuntimeControllerLocked() {
	n.closeRuntimeSessionLocked()
	controller := n.runtimeController
	n.runtimeController = nil
	if wireguardnodes.WGCtrlIsReady(controller) {
		n.logDebugf("Amnesia closing detached WireGuard controller")
	}
	wireguardnodes.CloseWGCtrl(controller)
}

func amnesiaPreConnectActionCount(profile *wgoamnesia.Profile) int {
	if profile == nil {
		return 0
	}
	return len(profile.PreConnect)
}

func amnesiaPeerTransport(spec wgdevice.PeerSpec) string {
	if spec.Endpoint == nil {
		return ""
	}
	return string(spec.Endpoint.Transport)
}

func amnesiaPeerEndpoint(spec wgdevice.PeerSpec) string {
	if spec.Endpoint == nil {
		return ""
	}
	return spec.Endpoint.Address
}

func amnesiaOptionalRange(value *wgdevice.AmneziaWGRange) string {
	if value == nil {
		return "off"
	}
	return value.String()
}

func amnesiaWGDebugSummary(cfg *wgdevice.AmneziaWGConfig) string {
	if cfg == nil {
		return "amneziawg_configured=false"
	}
	return fmt.Sprintf(
		"amneziawg_configured=true amneziawg_version=%d junk_count=%d junk_min=%d junk_max=%d init_padding=%d response_padding=%d cookie_padding=%d transport_padding=%d header_protection_key_configured=%t content_padding=%s rekey_after_time=%s rekey_timeout=%s reject_after_time=%s keepalive_timeout=%s max_handshake_attempts=%s random_trailers=%t disable_cookies=%t",
		cfg.Version, cfg.JunkCount, cfg.JunkMin, cfg.JunkMax, cfg.InitPadding, cfg.ResponsePadding,
		cfg.CookiePadding, cfg.TransportPadding, !cfg.HeaderProtectionKey.IsZero(), cfg.ContentPadding.String(),
		cfg.RekeyAfterTime.String(), cfg.RekeyTimeout.String(), cfg.RejectAfterTime.String(),
		cfg.KeepaliveTimeout.String(), cfg.MaxHandshakeAttempts.String(), cfg.RandomTrailers, cfg.DisableCookies,
	)
}
