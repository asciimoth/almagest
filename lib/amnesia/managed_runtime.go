package amnesianodes

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"

	conn "github.com/asciimoth/batchudp"
	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/nodekit"
)

func (n *managedAmnesiaNode) reconcile(req managedAmnesiaRuntimeRequest) (managedAmnesiaRuntimeResult, bool) {
	n.managedMu.Lock()
	defer n.managedMu.Unlock()
	if req.generation != n.generation.Load() {
		return managedAmnesiaRuntimeResult{}, false
	}
	result := managedAmnesiaRuntimeResult{generation: req.generation}
	uuid, persistent, err := n.runtimeInstallationUUID(req.ctx)
	if err != nil {
		n.closeRuntimeLocked()
		result.err = err
		return result, true
	}
	result.installationUUID = uuid
	result.persistent = persistent
	if !req.config.enabled || req.config.connectionSecret == "" ||
		!req.tunLinked || !nodekit.NetworkIsReady(req.network) {
		n.closeRuntimeLocked()
		return result, true
	}
	if !nodekit.TunIsReady(req.tun) {
		if n.runtimeClient != nil && n.runtimeConfig != nil && n.runtimeDevice == nil {
			// A linked TUN provider can briefly withdraw its old payload while it
			// applies this runtime's profile configuration. No device exists yet,
			// so retain the negotiated key and profile until the replacement
			// payload arrives.
			result.waitingForTun = true
			result.format = n.runtimeFormat
			result.profile = n.runtimeProfile
			return result, true
		}
		n.closeRuntimeLocked()
		return result, true
	}

	input, err := n.readSecret(req.config.connectionSecret, req.nodeName)
	if err != nil {
		n.closeRuntimeLocked()
		result.err = formatRuntimeError("read connection secret", err)
		return result, true
	}
	parsed, err := wgoamnesia.ParseInput(input)
	if err != nil {
		n.closeRuntimeLocked()
		result.err = formatRuntimeError("parse connection secret", err)
		return result, true
	}
	result.format = parsed.Format
	serviceImport := parsed.Format == wgoamnesia.InputFormatServiceKey
	if serviceImport && req.tlsConfigErr != "" {
		n.closeRuntimeLocked()
		result.err = errors.New("TLS config: " + req.tlsConfigErr)
		return result, true
	}

	var gatewayPublicKey []byte
	if serviceImport && req.config.gatewayPublicKeySecret != "" {
		value, readErr := n.readSecret(req.config.gatewayPublicKeySecret, req.nodeName)
		if readErr != nil {
			n.closeRuntimeLocked()
			result.err = formatRuntimeError("read gateway public key secret", readErr)
			return result, true
		}
		gatewayPublicKey = []byte(value)
	}
	material := managedAmnesiaMaterial(req, parsed.Format, input, gatewayPublicKey)
	if n.runtimeImport != nil {
		if n.pausedImportMatches(req, material) {
			return n.continuePausedImportLocked(req, result)
		}
		n.closeRuntimeLocked()
	}
	if n.runtimeMatches(req, material) {
		if n.runtimeDevice == nil {
			if n.runtimeTunConfigured {
				return n.activatePreparedRuntimeLocked(req, result)
			}
			result.runtimeID = n.runtimeID
			result.prepared = true
			result.profile = n.runtimeProfile
			return result, true
		}
		if nodekit.SameTunInstance(n.runtimeTunSource, req.tun) {
			n.refreshStatsSubscriptionLocked(req.generation)
			result.ready = true
			result.profile = n.runtimeProfile
			result.stats = n.runtimeDevice.RuntimeStats()
			result.dns = n.runtimeDNS
			return result, true
		}
	}
	if req.captcha != nil {
		result.err = errors.New("complete Amnezia captcha: the challenge is no longer active")
		return result, true
	}

	// A connection owns its client, profile, device, and private key as one unit.
	// Close the old unit before the client creates the next identity.
	n.closeRuntimeLocked()
	if req.ctx.Err() != nil || req.generation != n.generation.Load() {
		return managedAmnesiaRuntimeResult{}, false
	}

	var transport *http.Transport
	cleanupTransport := true
	defer func() {
		if cleanupTransport && transport != nil {
			transport.CloseIdleConnections()
		}
	}()

	transport = amnesiaHTTPTransport(req.network, req.tlsConfig)
	apiTransport := newAmnesiaAPIRoundTripper(transport, n.logDebugf)
	clientOptions := wgoamnesia.ClientOptions{
		HTTPClient: &http.Client{Transport: apiTransport},
		Metadata: wgoamnesia.ClientMetadata{
			InstallationUUID: uuid,
		},
		ProxyCache: newKVProxyCache(n.nodeStore),
	}
	if req.config.gatewayURL != "" {
		clientOptions.GatewayURL = req.config.gatewayURL
		clientOptions.DisableS3Discovery = true
		clientOptions.PrimaryS3URLs = []string{}
		clientOptions.FallbackS3URLs = []string{}
		clientOptions.StaticProxyURLs = []string{}
	}
	if len(gatewayPublicKey) != 0 {
		clientOptions.GatewayPublicKeyPEM = gatewayPublicKey
	}
	client, err := n.newClient(clientOptions)
	if err != nil {
		result.err = formatRuntimeError("create Amnezia client", err)
		return result, true
	}

	importContext := req.ctx
	cancelImport := func() {}
	if serviceImport {
		importContext, cancelImport = context.WithTimeout(req.ctx, amnesiaImportTimeout)
	}
	options := wgoamnesia.NegotiationOptions{
		InteractionPolicy: wgoamnesia.PauseOnInteraction,
		ServerCountryCode: req.config.serverCountryCode,
		// A managed Amnesia node always represents an active connection.
		ConnectEvent: true,
	}
	var step wgoamnesia.Step
	if serviceImport && (n.startImport != nil || isStatefulAmnesiaClient(client)) {
		importSession, startErr := n.startManagedImport(client, input, options)
		if startErr != nil {
			cancelImport()
			result.err = formatRuntimeError("start Amnezia connection import", startErr)
			return result, true
		}
		n.nextRuntimeID++
		n.runtimeID = n.nextRuntimeID
		n.runtimeClient = client
		n.runtimeImport = importSession
		n.runtimeHTTP = transport
		n.runtimeNetwork = req.network
		n.runtimeMaterial = material
		n.runtimeFormat = parsed.Format
		n.runtimePersistent = persistent
		cleanupTransport = false
		n.logDebugf("Amnesia resumable import started: generation=%d runtime=%d", req.generation, n.runtimeID)
		step, err = importSession.Next(importContext)
	} else {
		var profile *wgoamnesia.Profile
		profile, err = client.ImportNonInteractive(importContext, input, options)
		step = wgoamnesia.Step{State: wgoamnesia.NegotiationComplete, Profile: profile}
	}
	cancelImport()
	if err != nil {
		n.logDebugf("Amnesia profile import failed: generation=%d error=%v", req.generation, err)
		n.closeRuntimeLocked()
		result.err = amnesiaImportError(err)
		return result, true
	}
	if n.runtimeID == 0 {
		n.nextRuntimeID++
		n.runtimeID = n.nextRuntimeID
	}
	profile, captcha, finishErr := n.finishImportStepLocked(step)
	if finishErr != nil {
		n.closeRuntimeLocked()
		result.err = finishErr
		return result, true
	}
	if captcha != nil {
		result.runtimeID = n.runtimeID
		result.captcha = captcha
		return result, true
	}
	cleanupTransport = false
	return n.prepareImportedProfileLocked(req, result, client, transport, material, parsed.Format, profile, persistent)
}

func isStatefulAmnesiaClient(client Client) bool {
	_, ok := client.(amnesiaStatefulClient)
	return ok
}

func (n *managedAmnesiaNode) startManagedImport(
	client Client,
	input string,
	options wgoamnesia.NegotiationOptions,
) (managedAmnesiaImportSession, error) {
	if n.startImport != nil {
		session, err := n.startImport(client, input, options)
		if err == nil && session == nil {
			err = errors.New("amnezia client returned no import session")
		}
		return session, err
	}
	stateful, ok := client.(amnesiaStatefulClient)
	if !ok {
		return nil, errors.New("amnezia client does not support resumable imports")
	}
	session, err := stateful.StartImport(input, options)
	if err == nil && session == nil {
		err = errors.New("amnezia client returned no import session")
	}
	return session, err
}

func (n *managedAmnesiaNode) prepareImportedProfileLocked(
	req managedAmnesiaRuntimeRequest,
	result managedAmnesiaRuntimeResult,
	client Client,
	transport interface{ CloseIdleConnections() },
	material [32]byte,
	format wgoamnesia.InputFormat,
	profile *wgoamnesia.Profile,
	persistent bool,
) (managedAmnesiaRuntimeResult, bool) {
	n.runtimeClient = client
	n.runtimeImport = nil
	n.runtimeCaptcha = nil
	n.runtimeConfig = profile
	n.runtimeHTTP = transport
	n.runtimeNetwork = req.network
	n.runtimeMaterial = material
	n.runtimeProfile = profileStatus(profile, req.config.serverCountryCode)
	n.runtimeFormat = format
	n.runtimePersistent = persistent
	n.logDebugf(
		"Amnesia connection prepared: generation=%d runtime=%d format=%s profile=%q country=%q endpoint=%q protocol=%q mtu=%d addresses=%v dns=%v",
		req.generation, n.runtimeID, n.runtimeFormat, n.runtimeProfile.name,
		n.runtimeProfile.serverCountry, profile.Peer.Endpoint, profile.AmneziaWG.ProtocolVersion,
		profile.Interface.MTU, n.runtimeProfile.addresses, n.runtimeProfile.dns,
	)
	if req.ctx.Err() != nil || req.generation != n.generation.Load() {
		// A successful negotiation has already registered this profile key. Keep
		// it for the next generation, which will either use it when effective
		// inputs match or discard it before it negotiates materially new inputs.
		n.logDebugf(
			"Amnesia prepared connection retained for newer generation: generation=%d current_generation=%d",
			req.generation, n.generation.Load(),
		)
		return managedAmnesiaRuntimeResult{}, false
	}

	// Profile negotiation also registers the generated public key. Publish the
	// profile's TUN configuration before creating a device. This order matches
	// the wgo live client and prevents a TUN rebuild from discarding a device.
	result.runtimeID = n.runtimeID
	result.prepared = true
	result.profile = n.runtimeProfile
	return result, true
}

func (n *managedAmnesiaNode) pausedImportMatches(req managedAmnesiaRuntimeRequest, material [32]byte) bool {
	return n.runtimeImport != nil && n.runtimeConfig == nil && n.runtimeMaterial == material &&
		nodekit.SameNetworkInstance(n.runtimeNetwork, req.network)
}

func (n *managedAmnesiaNode) continuePausedImportLocked(
	req managedAmnesiaRuntimeRequest,
	result managedAmnesiaRuntimeResult,
) (managedAmnesiaRuntimeResult, bool) {
	result.format = n.runtimeFormat
	result.runtimeID = n.runtimeID
	responseMatches := req.captcha != nil && n.runtimeCaptcha != nil && req.captcha.runtimeID == n.runtimeID &&
		req.captcha.dialogID == n.runtimeCaptcha.dialogID
	if !responseMatches {
		n.logDebugf(
			"Amnesia captcha remains pending: generation=%d runtime=%d response_present=%t",
			req.generation, n.runtimeID, req.captcha != nil,
		)
		result.captcha = cloneAmnesiaCaptchaPrompt(n.runtimeCaptcha)
		return result, true
	}

	attempt := n.runtimeCaptcha.request.Attempt
	n.logDebugf("Amnesia captcha submission started: generation=%d runtime=%d attempt=%d", req.generation, n.runtimeID, attempt)
	ctx, cancel := context.WithTimeout(req.ctx, amnesiaImportTimeout)
	step, err := n.runtimeImport.Resume(ctx, wgoamnesia.InteractionResponse{
		CaptchaSolution: req.captcha.solution,
	})
	cancel()
	if err != nil {
		n.logDebugf(
			"Amnesia captcha submission failed: generation=%d runtime=%d attempt=%d error_type=%T",
			req.generation, n.runtimeID, attempt, err,
		)
		n.closeRuntimeLocked()
		result.err = amnesiaImportError(err)
		return result, true
	}
	profile, captcha, err := n.finishImportStepLocked(step)
	if err != nil {
		n.closeRuntimeLocked()
		result.err = err
		return result, true
	}
	if captcha != nil {
		n.logDebugf("Amnesia captcha submission returned another challenge: generation=%d runtime=%d attempt=%d", req.generation, n.runtimeID, attempt)
		result.captcha = captcha
		return result, true
	}
	n.logDebugf("Amnesia captcha submission completed: generation=%d runtime=%d attempt=%d", req.generation, n.runtimeID, attempt)
	return n.prepareImportedProfileLocked(
		req, result, n.runtimeClient, n.runtimeHTTP, n.runtimeMaterial,
		n.runtimeFormat, profile, n.runtimePersistent,
	)
}

func (n *managedAmnesiaNode) finishImportStepLocked(
	step wgoamnesia.Step,
) (*wgoamnesia.Profile, *amnesiaCaptchaPrompt, error) {
	if step.Interaction != nil {
		previousAttempt := 0
		if n.runtimeCaptcha != nil {
			previousAttempt = n.runtimeCaptcha.request.Attempt
		}
		prompt, err := newAmnesiaCaptchaPrompt(n.runtimeID, step.Interaction)
		if err != nil {
			return nil, nil, err
		}
		n.runtimeCaptcha = cloneAmnesiaCaptchaPrompt(prompt)
		n.logDebugf(
			"Amnesia captcha challenge received: runtime=%d attempt=%d previous_attempt=%d reason=%s",
			n.runtimeID, prompt.request.Attempt, previousAttempt, prompt.request.Captcha.Reason,
		)
		return nil, prompt, nil
	}
	if step.Profile == nil {
		return nil, nil, errors.New("import Amnezia connection: client returned no profile")
	}
	n.runtimeCaptcha = nil
	return step.Profile, nil, nil
}

// activatePreparedRuntimeLocked creates the device only after the graph has
// applied the negotiated TUN configuration. This is the same startup order as
// the wgo live client: configured TUN and bind, profile application, then Up.
func (n *managedAmnesiaNode) activatePreparedRuntimeLocked(
	req managedAmnesiaRuntimeRequest,
	result managedAmnesiaRuntimeResult,
) (managedAmnesiaRuntimeResult, bool) {
	if req.ctx.Err() != nil || req.generation != n.generation.Load() {
		return managedAmnesiaRuntimeResult{}, false
	}
	client := n.runtimeClient
	profile := n.runtimeConfig
	if client == nil || profile == nil || n.runtimeDevice != nil {
		result.err = errors.New("activate Amnesia connection: negotiated profile is unavailable")
		return result, true
	}

	// The graph owns req.tun. The device owns only a detached child, either
	// directly or through TunnelDNS. Closing the connection therefore does not
	// consume the graph payload.
	var deviceTun gtun.Tun
	if len(profile.Interface.DNS) != 0 {
		resolver, err := dnsnodes.NewTunnelDNS(req.tun, n.Pool(), n.WorkerSpawner, dnsnodes.TunnelDNSOptions{
			Addresses:  profile.Interface.Addresses,
			Servers:    profile.Interface.DNS,
			AllowedIPs: profile.Peer.AllowedIPs,
		})
		if err != nil {
			n.closeRuntimeLocked()
			result.err = formatRuntimeError("create tunnel DNS resolver", err)
			return result, true
		}
		deviceTun = resolver
		n.runtimeDNS = resolver
	} else {
		deviceTun = gtun.Detach(req.tun, nil, n.Pool())
	}
	bindOptions := conn.StdNetBindOptions{}
	if !req.network.IsNative() {
		bindOptions.AllowSingleFamily = true
	}
	bind := conn.NewDefaultBindWithOptions(req.network, bindOptions)
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.Logger != nil {
		logger = n.Logger
	}
	dev := wgdevice.NewDevice(deviceTun, bind, logger, n.WorkerSpawner, wgdevice.DeviceOptions{})

	n.runtimeDevice = dev
	if err := wgoamnesia.ApplyProfile(dev, profile); err != nil {
		n.logDebugf("Amnesia profile apply failed: generation=%d error=%v", req.generation, err)
		n.closeRuntimeLocked()
		result.err = formatRuntimeError("apply Amnezia profile", err)
		return result, true
	}
	if err := executePreConnectActionsLogged(req.ctx, req.network, profile.PreConnect, n.logDebugf); err != nil {
		if req.ctx.Err() != nil || req.generation != n.generation.Load() {
			// Keep the registered profile so the next generation can retry it with
			// a fresh device if the effective inputs did not change.
			n.closeRuntimeDeviceLocked()
			return managedAmnesiaRuntimeResult{}, false
		}
		n.closeRuntimeLocked()
		result.err = err
		return result, true
	}
	if err := dev.Up(); err != nil {
		n.closeRuntimeLocked()
		result.err = formatRuntimeError("start WireGuard device", err)
		return result, true
	}
	if req.ctx.Err() != nil || req.generation != n.generation.Load() {
		// The profile key is already registered. Retain the profile across an
		// equivalent graph generation, but never retain a device that consumed a
		// stale TUN payload.
		n.closeRuntimeDeviceLocked()
		return managedAmnesiaRuntimeResult{}, false
	}

	n.runtimeTunSource = req.tun
	n.refreshStatsSubscriptionLocked(req.generation)
	result.ready = true
	result.profile = n.runtimeProfile
	result.stats = dev.RuntimeStats()
	result.dns = n.runtimeDNS
	privateKey := dev.PrivateKey()
	localPublicKey := "none"
	if !privateKey.IsZero() {
		localPublicKey = privateKey.PublicKey().Base64()
	}
	n.logDebugf(
		"Amnesia connection created: generation=%d format=%s local_public_key=%s profile=%q country=%q addresses=%v dns=%v",
		req.generation, n.runtimeFormat, localPublicKey, n.runtimeProfile.name,
		n.runtimeProfile.serverCountry, n.runtimeProfile.addresses, n.runtimeProfile.dns,
	)
	return result, true
}

func (n *managedAmnesiaNode) runtimeMatches(req managedAmnesiaRuntimeRequest, material [32]byte) bool {
	if n.runtimeClient == nil || n.runtimeConfig == nil {
		return false
	}
	materialMatches := n.runtimeMaterial == material
	networkReady := nodekit.NetworkIsReady(req.network)
	networkMatches := nodekit.SameNetworkInstance(n.runtimeNetwork, req.network)
	if n.runtimeDevice != nil {
		select {
		case <-n.runtimeDevice.Wait():
			n.logDebugf("Amnesia runtime cannot be reused: device_closed=true")
			return false
		default:
		}
	}
	matched := materialMatches && nodekit.TunIsReady(req.tun) && networkReady && networkMatches
	if !matched {
		n.logDebugf(
			"Amnesia runtime cannot be reused: material_match=%t tun_ready=%t network_ready=%t network_match=%t",
			materialMatches, nodekit.TunIsReady(req.tun), networkReady, networkMatches,
		)
	}
	return matched
}

func (n *managedAmnesiaNode) refreshStatsSubscriptionLocked(generation uint64) {
	if n.runtimeUnsubscribe != nil {
		n.runtimeUnsubscribe()
		n.runtimeUnsubscribe = nil
	}
	dev := n.runtimeDevice
	if dev == nil {
		return
	}
	n.runtimeUnsubscribe = dev.SubscribeRuntimeStats(func(stats wgdevice.RuntimeStats) {
		if n.W == nil || n.ID == 0 {
			return
		}
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: managedAmnesiaStats{
			generation: generation,
			stats:      stats,
		}})
	})
}

func (n *managedAmnesiaNode) runtimeInstallationUUID(ctx context.Context) (string, bool, error) {
	if n.runtimeUUID != "" {
		return n.runtimeUUID, n.runtimePersistent, nil
	}
	uuid, persistent, err := loadOrCreateInstallationUUID(ctx, n.nodeStore, &n.ephemeralID)
	if err != nil {
		return "", false, err
	}
	n.runtimeUUID = uuid
	n.runtimePersistent = persistent
	return uuid, persistent, nil
}

func (n *managedAmnesiaNode) closeRuntimeLocked() {
	n.closeRuntimeDeviceLocked()
	if n.runtimeHTTP != nil {
		n.runtimeHTTP.CloseIdleConnections()
	}
	n.runtimeClient = nil
	n.runtimeImport = nil
	n.runtimeCaptcha = nil
	n.runtimeConfig = nil
	n.runtimeHTTP = nil
	n.runtimeNetwork = nil
	n.runtimeMaterial = [32]byte{}
	n.runtimeProfile = amnesiaProfileStatus{}
	n.runtimeFormat = wgoamnesia.InputFormatUnknown
	n.runtimeTunConfigured = false
	n.runtimeID = 0
}

// closeRuntimeDeviceLocked closes only the data plane. It leaves the client
// and negotiated profile available for an equivalent newer generation.
func (n *managedAmnesiaNode) closeRuntimeDeviceLocked() {
	if n.runtimeUnsubscribe != nil {
		n.runtimeUnsubscribe()
		n.runtimeUnsubscribe = nil
	}
	if n.runtimeDevice != nil {
		n.runtimeDevice.Close()
	} else if n.runtimeDNS != nil {
		dnsnodes.CloseDNS(n.runtimeDNS)
	}
	n.runtimeDevice = nil
	n.runtimeDNS = nil
	n.runtimeTunSource = nil
}

func managedAmnesiaMaterial(
	req managedAmnesiaRuntimeRequest,
	format wgoamnesia.InputFormat,
	input string,
	gatewayKey []byte,
) [32]byte {
	hash := sha256.New()
	writeHashString(hash, string(format))
	writeHashString(hash, input)
	if format == wgoamnesia.InputFormatServiceKey {
		writeHashString(hash, req.config.serverCountryCode)
		writeHashString(hash, req.config.gatewayURL)
		writeHashString(hash, req.config.gatewayPublicKeySecret)
		writeHashString(hash, string(gatewayKey))
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], req.tlsRevision)
		_, _ = hash.Write(raw[:])
	}
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out
}
