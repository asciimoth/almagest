package amnesianodes

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/gonnect/dns"
	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
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
	if !req.config.enabled || req.config.connectionSecret == "" || !req.tunLinked {
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
	profileMaterialMatches := n.runtimeProfileMaterialMatches(material)
	if !nodekit.NetworkIsReady(req.network) {
		if profileMaterialMatches && req.networkLinked {
			// A linked carrier provider can temporarily withdraw its payload while
			// it rebuilds a nested graph. The bind belongs to the old payload, so
			// discard the data plane. Keep the registered service profile and key
			// for the replacement. An explicit graph unlink has networkLinked=false
			// and still closes the complete runtime.
			n.closeRuntimeDeviceLocked()
			result.waitingForNetwork = true
			result.format = n.runtimeFormat
			result.profile = n.runtimeProfile
			return result, true
		}
		n.closeRuntimeLocked()
		return result, true
	}
	if !nodekit.TunIsReady(req.tun) {
		if profileMaterialMatches {
			// A linked TUN provider can briefly withdraw its old payload while it
			// applies this runtime's profile configuration. Retain the negotiated
			// key and profile until the replacement payload arrives. The old data
			// plane can be closing already, so do not try to retain its device.
			n.closeRuntimeDeviceLocked()
			result.waitingForTun = true
			result.format = n.runtimeFormat
			result.profile = n.runtimeProfile
			return result, true
		}
		n.closeRuntimeLocked()
		return result, true
	}
	if n.runtimeImport != nil {
		if n.pausedImportMatches(req, material) {
			return n.continuePausedImportLocked(req, result)
		}
		n.closeRuntimeLocked()
	}
	networkChanged := profileMaterialMatches &&
		!nodekit.SameNetworkInstance(n.runtimeNetwork, req.network)
	if networkChanged {
		// The configured bind cannot move between network wrappers. Close the
		// device, but keep the imported profile and its registered private key.
		n.closeRuntimeDeviceLocked()
		if n.runtimeHTTP != nil {
			n.runtimeHTTP.CloseIdleConnections()
		}
		n.runtimeNetwork = req.network
	}
	if profileMaterialMatches &&
		(networkChanged || n.runtimeMTURevision != req.networkMTURevision) {
		profileState, mtuWarning, mtuErr := calculateAmnesiaProfileMTU(
			req.ctx, req.network, n.runtimeConfig, n.ID, req.config.serverCountryCode,
		)
		if mtuErr != nil {
			n.closeRuntimeDeviceLocked()
			result.err = fmt.Errorf("validate changed Amnezia carrier MTU: %w", mtuErr)
			return result, true
		}
		if mtuWarning != "" && n.Logger != nil {
			n.Logger.Warnf("Amnesia automatic MTU update: %s; profile ceiling=%d", mtuWarning, n.runtimeConfig.Interface.MTU)
		}
		mtuChanged := n.runtimeProfile.mtu != profileState.mtu
		n.runtimeProfile = profileState
		n.runtimeMTURevision = req.networkMTURevision
		if mtuChanged {
			n.closeRuntimeDeviceLocked()
			n.runtimeTunConfigured = false
			result.runtimeID = n.runtimeID
			result.prepared = true
			result.profile = n.runtimeProfile
			return result, true
		}
	}
	if profileMaterialMatches && n.runtimeMTURevision == req.networkMTURevision {
		if n.runtimeDevice != nil {
			select {
			case <-n.runtimeDevice.Wait():
				n.logDebugf(
					"Amnesia closed data-plane device will be rebuilt from registered profile: generation=%d runtime=%d",
					req.generation, n.runtimeID,
				)
				n.closeRuntimeDeviceLocked()
			default:
			}
		}
		if n.runtimeDevice != nil {
			if nodekit.SameTunInstance(n.runtimeTunSource, req.tun) {
				n.refreshStatsSubscriptionLocked(req.generation)
				result.ready = true
				result.profile = n.runtimeProfile
				result.stats = n.runtimeDevice.RuntimeStats()
				result.dns = n.runtimeDNS
				return result, true
			}
			return n.replaceRuntimeTunLocked(req, result)
		}
		// The TUN can become unavailable after the readiness check at the start
		// of this reconcile. This happens when the profile configuration makes a
		// provider replace its current TUN while an equivalent graph notification
		// is already queued. Keep the registered profile across that gap. Starting
		// another import can register a second service key for the same material.
		if !nodekit.TunIsReady(req.tun) {
			result.waitingForTun = true
			result.format = n.runtimeFormat
			result.profile = n.runtimeProfile
			return result, true
		}
		if n.runtimeTunConfigured {
			return n.activatePreparedRuntimeLocked(req, result)
		}
		result.runtimeID = n.runtimeID
		result.prepared = true
		result.profile = n.runtimeProfile
		return result, true
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
	profileState, mtuWarning, mtuErr := calculateAmnesiaProfileMTU(
		req.ctx, req.network, profile, n.ID, req.config.serverCountryCode,
	)
	n.runtimeClient = client
	n.runtimeImport = nil
	n.runtimeCaptcha = nil
	n.runtimeConfig = profile
	n.runtimeHTTP = transport
	n.runtimeNetwork = req.network
	n.runtimeMaterial = material
	n.runtimeProfile = profileState
	n.runtimeFormat = format
	n.runtimePersistent = persistent
	n.runtimeMTURevision = req.networkMTURevision
	n.runtimeAnnounced = false
	if mtuErr != nil {
		n.closeRuntimeLocked()
		result.err = fmt.Errorf("validate Amnezia carrier MTU: %w", mtuErr)
		return result, true
	}
	if mtuWarning != "" && n.Logger != nil {
		n.Logger.Warnf("Amnesia automatic MTU: %s; profile ceiling=%d", mtuWarning, profile.Interface.MTU)
	}
	n.logDebugf(
		"Amnesia connection prepared: generation=%d runtime=%d format=%s profile=%q country=%q endpoint=%q protocol=%q effective_mtu=%d profile_mtu=%d carrier_udp_payload=%d s4=%d tls_revision=%d network_mtu_revision=%d addresses=%v dns=%v",
		req.generation, n.runtimeID, n.runtimeFormat, n.runtimeProfile.name,
		n.runtimeProfile.serverCountry, profile.Peer.Endpoint, profile.AmneziaWG.ProtocolVersion,
		n.runtimeProfile.mtu, n.runtimeProfile.profileMTU, n.runtimeProfile.carrierMaxUDP,
		n.runtimeProfile.transportS4, req.tlsRevision, req.networkMTURevision,
		n.runtimeProfile.addresses, n.runtimeProfile.dns,
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

	deviceTun, resolver, tunGeneration, err := n.newRuntimeTunLocked(req.tun, n.runtimeID)
	if err != nil {
		return n.failPreparedActivationLocked(req, result, err)
	}
	bindOptions := conn.StdNetBindOptions{}
	if !req.network.IsNative() {
		bindOptions.AllowSingleFamily = true
	}
	baseBind := conn.NewDefaultBindWithOptions(req.network, bindOptions)
	bind := conn.Bind(baseBind)
	if learner, ok := links.DatagramMTUProviderFromNetwork(req.network).(links.DatagramMTULearner); ok {
		bind = managedAmnesiaLearningBind{Bind: baseBind, node: n, generation: req.generation, learner: learner}
	}
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.Logger != nil {
		logger = n.Logger
	}
	dev := wgdevice.NewDevice(deviceTun, bind, logger, n.WorkerSpawner, wgdevice.DeviceOptions{})

	n.runtimeDevice = dev
	n.runtimeDNS = resolver
	n.runtimeTunObserver = deviceTun
	n.runtimeTunGeneration = tunGeneration
	if err := wgoamnesia.ApplyProfile(dev, profile); err != nil {
		n.logDebugf("Amnesia profile apply failed: generation=%d error=%v", req.generation, err)
		return n.failPreparedActivationLocked(
			req, result, formatRuntimeError("apply Amnezia profile", err),
		)
	}
	if err := executePreConnectActionsLogged(req.ctx, req.network, profile.PreConnect, n.logDebugf); err != nil {
		return n.failPreparedActivationLocked(req, result, err)
	}
	if err := dev.Up(); err != nil {
		return n.failPreparedActivationLocked(
			req, result, formatRuntimeError("start WireGuard device", err),
		)
	}
	if req.ctx.Err() != nil || req.generation != n.generation.Load() {
		// The profile key is already registered. Discard the stale data plane so
		// a newer generation can create one from its current graph inputs.
		n.closeRuntimeDeviceLocked()
		return managedAmnesiaRuntimeResult{}, false
	}

	n.runtimeTunSource = req.tun
	n.refreshStatsSubscriptionLocked(req.generation)
	result.ready = true
	result.profile = n.runtimeProfile
	result.stats = dev.RuntimeStats()
	result.dns = n.runtimeDNS
	n.logRuntimeReadyLocked(req.generation, false)
	return result, true
}

func (n *managedAmnesiaNode) failPreparedActivationLocked(
	req managedAmnesiaRuntimeRequest,
	result managedAmnesiaRuntimeResult,
	err error,
) (managedAmnesiaRuntimeResult, bool) {
	// Device, TUN, bind, DNS, and pre-connect failures do not invalidate the
	// profile that the service has already registered. Always discard only the
	// local data plane. A newer generation can arrive immediately after this
	// decision, so a current/stale check cannot safely select full cleanup.
	n.closeRuntimeDeviceLocked()
	if req.ctx.Err() != nil || req.generation != n.generation.Load() {
		// A newer graph generation owns the prepared service registration. Close
		// only the partial data plane created by this activation attempt. The next
		// generation can attach its current TUN without registering another key.
		n.logDebugf(
			"Amnesia stale activation failure retained prepared profile: generation=%d current_generation=%d runtime=%d error=%v",
			req.generation, n.generation.Load(), n.runtimeID, err,
		)
		return managedAmnesiaRuntimeResult{}, false
	}
	result.err = err
	result.format = n.runtimeFormat
	result.profile = n.runtimeProfile
	return result, true
}

func (n *managedAmnesiaNode) newRuntimeTunLocked(
	source gtun.Tun,
	runtimeID uint64,
) (*nodekit.TunFailureObserver, dns.Interface, uint64, error) {
	profile := n.runtimeConfig
	if profile == nil {
		return nil, nil, 0, errors.New("create Amnesia TUN: negotiated profile is unavailable")
	}
	// The graph owns source. The device owns only a detached child, either
	// directly or through TunnelDNS. Closing or replacing the device TUN does
	// not consume the graph payload.
	var deviceTun gtun.Tun
	var resolver dns.Interface
	if len(profile.Interface.DNS) != 0 {
		tunnelResolver, err := dnsnodes.NewTunnelDNS(source, n.Pool(), n.WorkerSpawner, dnsnodes.TunnelDNSOptions{
			Addresses:  profile.Interface.Addresses,
			Servers:    profile.Interface.DNS,
			AllowedIPs: profile.Peer.AllowedIPs,
		})
		if err != nil {
			return nil, nil, 0, formatRuntimeError("create tunnel DNS resolver", err)
		}
		deviceTun = tunnelResolver
		resolver = tunnelResolver
	} else {
		deviceTun = gtun.Detach(source, nil, n.Pool())
	}
	deviceTun = wireguardnodes.NewContentCeilingTun(deviceTun, n.runtimeProfile.mtu)
	n.nextTunGeneration++
	tunGeneration := n.nextTunGeneration
	var observedTun *nodekit.TunFailureObserver
	observedTun = nodekit.NewTunFailureObserver(deviceTun, func(err error) {
		if n.W == nil || n.ID == 0 {
			return
		}
		go n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: managedAmnesiaTunFailure{
			generation:    n.generation.Load(),
			tunGeneration: tunGeneration,
			runtimeID:     runtimeID,
			observer:      observedTun,
			err:           err,
		}})
	})
	return observedTun, resolver, tunGeneration, nil
}

func (n *managedAmnesiaNode) replaceRuntimeTunLocked(
	req managedAmnesiaRuntimeRequest,
	result managedAmnesiaRuntimeResult,
) (managedAmnesiaRuntimeResult, bool) {
	dev := n.runtimeDevice
	if dev == nil {
		return n.activatePreparedRuntimeLocked(req, result)
	}
	deviceTun, resolver, tunGeneration, err := n.newRuntimeTunLocked(req.tun, n.runtimeID)
	if err != nil {
		return n.failPreparedActivationLocked(req, result, err)
	}
	if err := dev.ReplaceTUN(deviceTun); err != nil {
		_ = deviceTun.Close()
		if req.ctx.Err() != nil || req.generation != n.generation.Load() {
			return n.failPreparedActivationLocked(req, result, err)
		}
		// A replacement with larger fixed geometry can require a new device.
		// Keep the registered profile while the data plane is rebuilt.
		n.closeRuntimeDeviceLocked()
		return n.activatePreparedRuntimeLocked(req, result)
	}
	n.runtimeDNS = resolver
	n.runtimeTunObserver = deviceTun
	n.runtimeTunGeneration = tunGeneration
	if err := dev.Up(); err != nil {
		return n.failPreparedActivationLocked(
			req, result, formatRuntimeError("start WireGuard device after TUN replacement", err),
		)
	}
	if req.ctx.Err() != nil || req.generation != n.generation.Load() || !nodekit.TunIsReady(req.tun) {
		n.closeRuntimeDeviceLocked()
		return managedAmnesiaRuntimeResult{}, false
	}
	n.runtimeTunSource = req.tun
	n.refreshStatsSubscriptionLocked(req.generation)
	result.ready = true
	result.profile = n.runtimeProfile
	result.stats = dev.RuntimeStats()
	result.dns = n.runtimeDNS
	n.logRuntimeReadyLocked(req.generation, true)
	return result, true
}

func (n *managedAmnesiaNode) logRuntimeReadyLocked(generation uint64, replaced bool) {
	if n.runtimeAnnounced {
		if replaced {
			n.logDebugf("Amnesia runtime TUN replaced: generation=%d runtime=%d", generation, n.runtimeID)
		}
		return
	}
	privateKey := n.runtimeDevice.PrivateKey()
	localPublicKey := "none"
	if !privateKey.IsZero() {
		localPublicKey = privateKey.PublicKey().Base64()
	}
	n.logDebugf(
		"Amnesia connection created: generation=%d format=%s local_public_key=%s profile=%q country=%q addresses=%v dns=%v",
		generation, n.runtimeFormat, localPublicKey, n.runtimeProfile.name,
		n.runtimeProfile.serverCountry, n.runtimeProfile.addresses, n.runtimeProfile.dns,
	)
	n.runtimeAnnounced = true
}

func (n *managedAmnesiaNode) runtimeProfileMaterialMatches(material [32]byte) bool {
	return n.runtimeClient != nil && n.runtimeConfig != nil && n.runtimeMaterial == material
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
	n.runtimeMTURevision = 0
	n.runtimeTunConfigured = false
	n.runtimeAnnounced = false
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
	n.runtimeTunObserver = nil
	n.runtimeTunGeneration = 0
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
