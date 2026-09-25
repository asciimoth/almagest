package experimentalnodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	tor "github.com/asciimoth/tor-driver"
	"github.com/asciimoth/tor-driver/direct"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const xTorOutboundRetryDelay = 250 * time.Millisecond

type xTorRuntime struct {
	id       uint64
	ctx      context.Context
	cancel   context.CancelFunc
	driver   xTorDriverBackend
	system   xTorSystem
	sources  map[string]gonnect.Network
	services map[string]xTorService
	unsubs   []func()

	outboundWake     chan struct{}
	outboundRevision uint64
	outboundWaitMu   sync.Mutex
	outboundWait     context.CancelFunc
}

func (r *xTorRuntime) close() error {
	if r == nil {
		return nil
	}
	r.cancel()
	r.cancelOutboundWait()
	for _, unsubscribe := range r.unsubs {
		if unsubscribe != nil {
			unsubscribe()
		}
	}
	var err error
	if r.driver != nil {
		err = errors.Join(err, r.driver.Close())
	}
	if r.system != nil {
		err = errors.Join(err, r.system.Close())
	}
	return err
}

func (r *xTorRuntime) signalOutbound() {
	if r == nil {
		return
	}
	select {
	case r.outboundWake <- struct{}{}:
	default:
	}
}

func (r *xTorRuntime) setOutboundWait(cancel context.CancelFunc) {
	r.outboundWaitMu.Lock()
	r.outboundWait = cancel
	r.outboundWaitMu.Unlock()
}

func (r *xTorRuntime) clearOutboundWait() {
	r.outboundWaitMu.Lock()
	r.outboundWait = nil
	r.outboundWaitMu.Unlock()
}

func (r *xTorRuntime) cancelOutboundWait() {
	if r == nil {
		return
	}
	r.outboundWaitMu.Lock()
	cancel := r.outboundWait
	r.outboundWait = nil
	r.outboundWaitMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type xTorStarted struct {
	generation uint64
	runtimeID  uint64
	nodeName   string
	config     XTorDriverConfig
	sources    map[string]gonnect.Network
	services   map[string]xTorServiceStatus
	state      tor.OutboundState
	activate   chan struct{}
	err        error
}

type xTorDriverEvent struct {
	runtimeID uint64
	event     tor.DriverEvent
}

type xTorServiceEvent struct {
	runtimeID uint64
	name      string
	event     tor.ServiceEvent
}

type xTorOutboundResult struct {
	runtimeID        uint64
	outboundRevision uint64
	state            tor.OutboundState
	ready            bool
	err              error
}

type xTorConfigPersisted struct {
	id  uint64
	err error
}

type xTorGeneratedSecret struct {
	selector string
	nodeName string
}

type xTorPendingPersistence struct {
	secrets []xTorGeneratedSecret
	durable atomic.Bool
}

func (n *xTorDriverNode) scheduleRebuild(_ string) {
	if n.stopped {
		return
	}
	generation := n.generation.Add(1)
	n.cancelCurrentRun()
	n.discardGeneratedSecretsBefore(generation)
	ctx, cancel := context.WithCancel(n.Context())
	n.cancelMu.Lock()
	n.cancelRun = cancel
	n.cancelMu.Unlock()

	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.activeID = 0
	n.activeSources = nil
	n.ready = false
	n.pending = true
	n.terminal = false
	n.clearRuntimeError()
	n.bootstrap = tor.BootstrapEvent{}
	n.services = make(map[string]xTorServiceStatus)
	_ = n.updateLabel()
	n.sendStatusBlock()

	config := cloneXTorDriverConfig(n.config)
	nodeName := n.currentNodeName()
	if err := n.SpawnWorker("XTorDriver runtime reconcile", func() {
		result, send := n.reconcile(ctx, generation, config, nodeName)
		if send && n.W != nil {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: result})
		}
	}); err != nil {
		cancel()
		n.pending = false
		n.setRuntimeError("start Tor runtime worker: "+err.Error(), false)
		_ = n.updateLabel()
		n.sendStatusBlock()
	}
}

func (n *xTorDriverNode) cancelCurrentRun() {
	n.cancelMu.Lock()
	cancel := n.cancelRun
	n.cancelRun = nil
	n.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (n *xTorDriverNode) reconcile(ctx context.Context, generation uint64, config XTorDriverConfig, nodeName string) (xTorStarted, bool) {
	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	if generation != n.generation.Load() || ctx.Err() != nil {
		return xTorStarted{}, false
	}
	n.closeRuntime()
	result := xTorStarted{generation: generation, nodeName: nodeName, config: config}
	if !config.Enabled {
		return result, true
	}

	runtime, updated, err := n.buildRuntime(ctx, generation, config, nodeName)
	result.config = updated
	if err != nil {
		result.err = err
		return result, true
	}
	if generation != n.generation.Load() || ctx.Err() != nil {
		_ = runtime.close()
		return xTorStarted{}, false
	}
	n.runtimeMu.Lock()
	n.runtime = runtime
	n.runtimeMu.Unlock()

	result.runtimeID = runtime.id
	result.sources = runtime.sources
	result.services = make(map[string]xTorServiceStatus, len(runtime.services))
	for name, service := range runtime.services {
		result.services[name] = xTorServiceStatus{Address: service.Address(), Publication: tor.PublicationPending}
	}
	result.state = runtime.driver.OutboundState()
	result.activate = make(chan struct{})
	if err := n.spawnRuntimeWatchers(runtime, result.activate); err != nil {
		n.runtimeMu.Lock()
		if n.runtime == runtime {
			n.runtime = nil
		}
		n.runtimeMu.Unlock()
		_ = runtime.close()
		result.err = err
		result.runtimeID = 0
		result.sources = nil
		result.services = nil
		result.activate = nil
	}
	return result, true
}

func (n *xTorDriverNode) buildRuntime(ctx context.Context, generation uint64, config XTorDriverConfig, nodeName string) (_ *xTorRuntime, updated XTorDriverConfig, err error) {
	updated = cloneXTorDriverConfig(config)
	torConfig, err := config.torConfig(n.host, nodeName)
	if err != nil {
		return nil, updated, err
	}
	system, torConfig, err := n.newSystem(torConfig)
	if err != nil {
		return nil, updated, fmt.Errorf("prepare Tor process: %w", err)
	}
	runtimeCtx, cancel := context.WithCancel(n.Context())
	runtime := &xTorRuntime{
		ctx: runtimeCtx, cancel: cancel, system: system,
		sources: make(map[string]gonnect.Network), services: make(map[string]xTorService),
		outboundWake: make(chan struct{}, 1),
	}
	success := false
	defer func() {
		if !success {
			err = errors.Join(err, runtime.close())
		}
	}()

	outbound, outboundRevision := n.currentOutboundState()
	runtime.outboundRevision = outboundRevision
	keyStore := newXTorOnionKeyStore(n.SecretsStorage(), nodeName, updated.Services, func(selector string) {
		n.trackGeneratedSecret(generation, nodeName, selector)
	})
	deps := system.Dependencies(direct.Network(), outbound, n.Logger)
	deps = applyXTorE2EDependencies(deps)
	deps.OnionKeys = keyStore
	driver, err := n.start(ctx, torConfig, deps)
	if err != nil {
		return nil, updated, fmt.Errorf("start Tor driver: %w", err)
	}
	runtime.driver = driver

	for _, network := range updated.Networks {
		value, createErr := driver.NewNetwork(network.torConfig())
		if createErr != nil {
			return nil, updated, fmt.Errorf("create Tor network %q: %w", network.Name, createErr)
		}
		runtime.sources[xTorNetworkKey(network.Name)] = value
	}
	for i, service := range updated.Services {
		serviceConfig, configErr := xTorServiceConfig(service)
		if configErr != nil {
			return nil, updated, configErr
		}
		value, createErr := driver.NewService(ctx, serviceConfig)
		if selector := keyStore.generatedSelectors()[xTorServiceKeyName(service.Name)]; selector != "" {
			updated.Services[i].KeySecret = selector
		}
		if createErr != nil {
			return nil, updated, fmt.Errorf("create onion service %q: %w", service.Name, createErr)
		}
		runtime.services[service.Name] = value
		runtime.sources[xTorServiceKey(service.Name)] = value
	}
	if err = n.installClientAuthorizations(ctx, generation, driver, nodeName, &updated); err != nil {
		return nil, updated, err
	}

	n.nextID++
	runtime.id = n.nextID
	if generation != n.generation.Load() || ctx.Err() != nil {
		return nil, updated, context.Canceled
	}
	success = true
	return runtime, updated, nil
}

func xTorServiceConfig(config XTorDriverServiceConfig) (tor.ServiceConfig, error) {
	out := tor.ServiceConfig{
		Ports: append([]uint16(nil), config.Ports...), MaxStreams: config.MaxStreams,
		KeyName: xTorServiceKeyName(config.Name),
	}
	if config.MaxStreamsPolicy == "close" {
		out.MaxStreamsPolicy = tor.CloseRendezvousCircuit
	}
	for _, value := range config.AuthorizedClients {
		key, err := tor.ParseClientAuthorizationPublicKey([]byte(value))
		if err != nil {
			return tor.ServiceConfig{}, fmt.Errorf("service %q authorized client: %w", config.Name, err)
		}
		out.AuthorizedClients = append(out.AuthorizedClients, key)
	}
	return out, nil
}

func (n *xTorDriverNode) installClientAuthorizations(ctx context.Context, generation uint64, driver xTorDriverBackend, nodeName string, config *XTorDriverConfig) error {
	for i := range config.ClientAuthorizations {
		item := &config.ClientAuthorizations[i]
		var authorization tor.ClientAuthorization
		if item.PrivateKeySecret == "" {
			store := n.SecretsStorage()
			if store == nil {
				return errors.New("tor client authorization secret storage is unavailable")
			}
			generatedStore, ok := store.(storage.GeneratedSecretsStorage)
			if !ok {
				return errors.New("tor generated-secret recovery is unavailable")
			}
			owner := xTorClientAuthorizationSecretOwner(item.Address)
			selector, secret, err := generatedStore.RecoverGeneratedSecret(owner, nodeName)
			if err != nil {
				return fmt.Errorf("recover client authorization for %q: %w", item.Address, err)
			}
			if selector != "" {
				privateKey, parseErr := tor.ParseClientAuthorizationPrivateKey([]byte(secret.Value))
				if parseErr != nil {
					return fmt.Errorf("parse recovered client authorization for %q: %w", item.Address, parseErr)
				}
				n.trackGeneratedSecret(generation, nodeName, selector)
				item.PrivateKeySecret = selector
				authorization = tor.ClientAuthorization{PrivateKey: privateKey, Name: item.Name}
			} else {
				generated, generateErr := driver.GenerateClientAuthorization(item.Name)
				if generateErr != nil {
					return fmt.Errorf("generate client authorization for %q: %w", item.Address, generateErr)
				}
				text, marshalErr := generated.PrivateKey.MarshalText()
				if marshalErr != nil {
					return marshalErr
				}
				selector, storeErr := newXTorGeneratedSecret(store, storage.Secret{Value: string(text)}, nodeName, owner)
				clear(text)
				if storeErr != nil {
					return fmt.Errorf("store client authorization for %q: %w", item.Address, storeErr)
				}
				n.trackGeneratedSecret(generation, nodeName, selector)
				item.PrivateKeySecret = selector
				authorization = generated
			}
		} else {
			store := n.SecretsStorage()
			if store == nil {
				return errors.New("tor client authorization secret storage is unavailable")
			}
			secret, err := store.GetSecret(item.PrivateKeySecret, nodeName)
			if err != nil {
				return fmt.Errorf("read client authorization secret %q: %w", item.PrivateKeySecret, err)
			}
			privateKey, err := tor.ParseClientAuthorizationPrivateKey([]byte(secret.Value))
			if err != nil {
				return fmt.Errorf("parse client authorization secret %q: %w", item.PrivateKeySecret, err)
			}
			_ = commitXTorGeneratedSecret(store, item.PrivateKeySecret, nodeName)
			authorization = tor.ClientAuthorization{PrivateKey: privateKey, Name: item.Name}
		}
		if err := driver.AddClientAuthorization(ctx, item.Address, authorization); err != nil {
			return fmt.Errorf("add client authorization for %q: %w", item.Address, err)
		}
	}
	return nil
}

func (n *xTorDriverNode) spawnRuntimeWatchers(runtime *xTorRuntime, activate <-chan struct{}) error {
	events, unsubscribe, err := runtime.driver.SubscribeEvents(32)
	if err != nil {
		return fmt.Errorf("subscribe to Tor driver events: %w", err)
	}
	runtime.unsubs = append(runtime.unsubs, unsubscribe)
	if err = n.SpawnWorker("XTorDriver event watcher", func() {
		if !waitXTorActivation(activate, runtime.ctx.Done()) {
			return
		}
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				n.sendRuntimeInbox(xTorDriverEvent{runtimeID: runtime.id, event: event})
			case <-runtime.ctx.Done():
				return
			}
		}
	}); err != nil {
		return err
	}
	for name, service := range runtime.services {
		events, stop, subscribeErr := service.SubscribeEvents(8)
		if subscribeErr != nil {
			return fmt.Errorf("subscribe to onion service %q events: %w", name, subscribeErr)
		}
		runtime.unsubs = append(runtime.unsubs, stop)
		name := name
		if err = n.SpawnWorker("XTorDriver onion service event watcher", func() {
			if !waitXTorActivation(activate, runtime.ctx.Done()) {
				return
			}
			for {
				select {
				case event, ok := <-events:
					if !ok {
						return
					}
					n.sendRuntimeInbox(xTorServiceEvent{runtimeID: runtime.id, name: name, event: event})
				case <-runtime.ctx.Done():
					return
				}
			}
		}); err != nil {
			return err
		}
	}
	return n.SpawnWorker("XTorDriver outbound reconciler", func() {
		if !waitXTorActivation(activate, runtime.ctx.Done()) {
			return
		}
		n.watchRuntimeOutbound(runtime)
	})
}

func (n *xTorDriverNode) watchRuntimeOutbound(runtime *xTorRuntime) {
	for {
		network, revision := n.currentOutboundState()
		var err error
		if revision != runtime.outboundRevision {
			err = runtime.driver.SetOutbound(network)
			if err == nil {
				runtime.outboundRevision = revision
			}
		}
		state := runtime.driver.OutboundState()
		if network == nil && !state.Available {
			err = nil
		}
		ready := false
		if err == nil && network != nil {
			waitCtx, cancel := context.WithCancel(runtime.ctx)
			runtime.setOutboundWait(cancel)
			if n.currentOutboundRevision() != revision {
				cancel()
				runtime.clearOutboundWait()
				continue
			}
			err = runtime.driver.WaitReady(waitCtx)
			cancel()
			runtime.clearOutboundWait()
			ready = err == nil
		}
		if runtime.ctx.Err() != nil {
			return
		}
		if n.currentOutboundRevision() != revision {
			continue
		}
		state = runtime.driver.OutboundState()
		n.sendRuntimeInbox(xTorOutboundResult{
			runtimeID: runtime.id, outboundRevision: revision,
			state: state, ready: ready, err: err,
		})
		if err == nil || network == nil {
			select {
			case <-runtime.outboundWake:
			case <-runtime.ctx.Done():
				return
			}
			continue
		}
		timer := time.NewTimer(xTorOutboundRetryDelay)
		select {
		case <-runtime.outboundWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		case <-runtime.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func waitXTorActivation(activate, done <-chan struct{}) bool {
	select {
	case <-activate:
		return true
	case <-done:
		return false
	}
}

func (n *xTorDriverNode) sendRuntimeInbox(payload any) {
	if n.W != nil {
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: payload})
	}
}

func (n *xTorDriverNode) closeRuntime() {
	n.runtimeMu.Lock()
	runtime := n.runtime
	n.runtime = nil
	n.runtimeMu.Unlock()
	if runtime != nil {
		_ = runtime.close()
	}
}

func (n *xTorDriverNode) applyOutbound(network nodekit.NetworkCloser) {
	n.ready = false
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	if network == nil && !n.terminal {
		n.clearRuntimeError()
		n.outboundState.Available = false
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	n.runtimeMu.Lock()
	runtime := n.runtime
	n.runtimeMu.Unlock()
	if runtime == nil || runtime.driver == nil {
		return
	}
	runtime.cancelOutboundWait()
	runtime.signalOutbound()
}

func (n *xTorDriverNode) handleStarted(result xTorStarted) {
	currentNodeName := n.currentNodeName()
	if result.generation != n.generation.Load() {
		n.discardGeneratedSecrets(result.generation, currentNodeName)
		return
	}
	if result.nodeName != currentNodeName {
		n.discardGeneratedSecrets(result.generation, currentNodeName)
		n.scheduleRebuild("node name changed during Tor startup")
		return
	}
	n.pending = false
	if result.err != nil {
		n.discardGeneratedSecrets(result.generation, currentNodeName)
		n.setRuntimeError(result.err.Error(), false)
		n.activeID = 0
		n.activeSources = nil
		n.services = nil
		n.outboundState = result.state
		_ = n.updateLabel()
		n.sendMenuSnapshot(false)
		return
	}
	configChanged := !xTorConfigEqual(n.config, result.config)
	n.config = result.config
	generated := n.takeGeneratedSecrets(result.generation)
	n.activeID = result.runtimeID
	n.activeSources = result.sources
	n.services = result.services
	n.outboundState = result.state
	n.clearRuntimeError()
	n.terminal = false
	_ = n.updateLabel()
	n.sendMenuSnapshot(false)
	if configChanged {
		if len(generated) > 0 {
			n.persistGeneratedSecrets(generated)
		} else {
			_ = n.NotifyConfigChanged()
		}
	} else {
		n.deleteGeneratedSecrets(generated)
	}
	if result.activate != nil {
		close(result.activate)
	}
}

func (n *xTorDriverNode) trackGeneratedSecret(generation uint64, nodeName, selector string) {
	if selector == "" {
		return
	}
	secret := xTorGeneratedSecret{selector: selector, nodeName: nodeName}
	if generation != n.generation.Load() {
		n.deleteGeneratedSecret(secret)
		return
	}
	n.generatedMu.Lock()
	if generation == n.generation.Load() {
		if n.generated == nil {
			n.generated = make(map[uint64][]xTorGeneratedSecret)
		}
		n.generated[generation] = append(n.generated[generation], secret)
		n.generatedMu.Unlock()
		return
	}
	n.generatedMu.Unlock()
	n.deleteGeneratedSecret(secret)
}

func (n *xTorDriverNode) takeGeneratedSecrets(generation uint64) []xTorGeneratedSecret {
	n.generatedMu.Lock()
	secrets := append([]xTorGeneratedSecret(nil), n.generated[generation]...)
	delete(n.generated, generation)
	n.generatedMu.Unlock()
	return secrets
}

func (n *xTorDriverNode) persistGeneratedSecrets(secrets []xTorGeneratedSecret) {
	if len(secrets) == 0 {
		return
	}
	n.persistNext++
	id := n.persistNext
	if n.persisting == nil {
		n.persisting = make(map[uint64]*xTorPendingPersistence)
	}
	pending := &xTorPendingPersistence{secrets: append([]xTorGeneratedSecret(nil), secrets...)}
	n.persisting[id] = pending
	result := n.NotifyConfigChanged()
	if result == nil {
		n.handleConfigPersisted(xTorConfigPersisted{id: id, err: errors.New("node configuration persistence returned no result")})
		return
	}
	err := n.SpawnWorker("XTorDriver generated-secret persistence", func() {
		select {
		case persistErr, ok := <-result:
			if !ok {
				persistErr = errors.New("node configuration persistence ended without a result")
			}
			if persistErr == nil {
				pending.durable.Store(true)
			}
			n.sendRuntimeInbox(xTorConfigPersisted{id: id, err: persistErr})
		case <-n.persistStop:
		}
	})
	if err != nil {
		n.handleConfigPersisted(xTorConfigPersisted{id: id, err: err})
	}
}

func (n *xTorDriverNode) handleConfigPersisted(result xTorConfigPersisted) {
	pending := n.persisting[result.id]
	if pending == nil {
		return
	}
	secrets := append([]xTorGeneratedSecret(nil), pending.secrets...)
	delete(n.persisting, result.id)
	if result.err == nil {
		for _, secret := range secrets {
			if xTorConfigReferencesSecret(n.config, secret.selector) {
				if err := commitXTorGeneratedSecret(n.SecretsStorage(), secret.selector, secret.nodeName); err != nil && n.Logger != nil {
					n.Logger.Errf("commit generated Tor secret: %v", err)
				}
			} else {
				n.deleteGeneratedSecret(secret, n.currentNodeName())
			}
		}
		return
	}

	for _, secret := range secrets {
		clearXTorGeneratedSelector(&n.config, secret.selector)
		n.deleteGeneratedSecret(secret, n.currentNodeName())
	}
	n.generation.Add(1)
	n.cancelCurrentRun()
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.activeID = 0
	n.activeSources = nil
	n.ready = false
	n.pending = false
	n.setRuntimeError("persist generated Tor secret selectors: "+result.err.Error(), false)
	_ = n.updateLabel()
	n.sendMenuSnapshot(false)
	go n.closeRuntime()
}

func xTorConfigReferencesSecret(config XTorDriverConfig, selector string) bool {
	if selector == "" {
		return false
	}
	for _, service := range config.Services {
		if service.KeySecret == selector {
			return true
		}
	}
	for _, authorization := range config.ClientAuthorizations {
		if authorization.PrivateKeySecret == selector {
			return true
		}
	}
	return false
}

func clearXTorGeneratedSelector(config *XTorDriverConfig, selector string) {
	if config == nil || selector == "" {
		return
	}
	for i := range config.Services {
		if config.Services[i].KeySecret == selector {
			config.Services[i].KeySecret = ""
		}
	}
	for i := range config.ClientAuthorizations {
		if config.ClientAuthorizations[i].PrivateKeySecret == selector {
			config.ClientAuthorizations[i].PrivateKeySecret = ""
		}
	}
}

func (n *xTorDriverNode) deleteGeneratedSecrets(secrets []xTorGeneratedSecret) {
	for _, secret := range secrets {
		n.deleteGeneratedSecret(secret, n.currentNodeName())
	}
}

func (n *xTorDriverNode) discardAllPersistingSecrets() {
	for id, pending := range n.persisting {
		if pending != nil && !pending.durable.Load() {
			n.deleteGeneratedSecrets(pending.secrets)
		}
		delete(n.persisting, id)
	}
}

func (n *xTorDriverNode) renameTrackedGeneratedSecret(previous, next string) {
	if previous == "" || next == "" || previous == next {
		return
	}
	n.generatedMu.Lock()
	defer n.generatedMu.Unlock()
	for generation := range n.generated {
		for i := range n.generated[generation] {
			if n.generated[generation][i].selector == previous {
				n.generated[generation][i].selector = next
			}
		}
	}
	for id := range n.persisting {
		pending := n.persisting[id]
		if pending == nil {
			continue
		}
		for i := range pending.secrets {
			if pending.secrets[i].selector == previous {
				pending.secrets[i].selector = next
			}
		}
	}
}

func (n *xTorDriverNode) discardGeneratedSecretsBefore(generation uint64) {
	n.generatedMu.Lock()
	var generations []uint64
	for candidate := range n.generated {
		if candidate < generation {
			generations = append(generations, candidate)
		}
	}
	n.generatedMu.Unlock()
	for _, candidate := range generations {
		n.discardGeneratedSecrets(candidate)
	}
}

func (n *xTorDriverNode) discardAllGeneratedSecrets() {
	n.generatedMu.Lock()
	generations := make([]uint64, 0, len(n.generated))
	for generation := range n.generated {
		generations = append(generations, generation)
	}
	n.generatedMu.Unlock()
	for _, generation := range generations {
		n.discardGeneratedSecrets(generation)
	}
}

func (n *xTorDriverNode) discardGeneratedSecrets(generation uint64, alternativeNodeNames ...string) {
	n.generatedMu.Lock()
	secrets := append([]xTorGeneratedSecret(nil), n.generated[generation]...)
	delete(n.generated, generation)
	n.generatedMu.Unlock()
	for _, secret := range secrets {
		n.deleteGeneratedSecret(secret, alternativeNodeNames...)
	}
}

func (n *xTorDriverNode) deleteGeneratedSecret(secret xTorGeneratedSecret, alternativeNodeNames ...string) {
	store, ok := n.SecretsStorage().(storage.GeneratedSecretsStorage)
	if !ok {
		return
	}
	err := store.DeleteGeneratedSecret(secret.selector, secret.nodeName)
	for _, nodeName := range alternativeNodeNames {
		if err == nil || !errors.Is(err, storage.ErrSecretAccessDenied) || nodeName == "" || nodeName == secret.nodeName {
			break
		}
		err = store.DeleteGeneratedSecret(secret.selector, nodeName)
	}
	if err != nil && n.Logger != nil {
		n.Logger.Errf("discard uncommitted Tor secret: %v", err)
	}
}

func (n *xTorDriverNode) handleDriverEvent(message xTorDriverEvent) {
	if message.runtimeID == 0 || message.runtimeID != n.activeID {
		return
	}
	switch event := message.event.(type) {
	case tor.BootstrapEvent:
		n.bootstrap = event
	case tor.TransportEvent:
		n.transport = event
	case tor.OutboundEvent:
		// Closing an intentional nil attachment can report failures from Tor
		// sessions that were already in flight. The missing input is the
		// authoritative state and is not a runtime fault.
		if n.currentOutboundNetwork() == nil {
			return
		}
		n.runtimeMu.Lock()
		runtime := n.runtime
		n.runtimeMu.Unlock()
		if runtime == nil || runtime.id != message.runtimeID {
			return
		}
		state := runtime.driver.OutboundState()
		if event.Generation != state.Generation {
			return
		}
		n.outboundState = state
		if event.Latched && !state.Available {
			runtime.cancelOutboundWait()
			n.setRuntimeError("Tor outbound network is latched after a runtime failure; reconnect the input or restart the node", false)
			n.ready = false
			n.outputs.InvalidateAllLocked(n.W, n.ID)
		}
	case tor.TerminalEvent:
		n.setRuntimeError("Tor driver stopped after a terminal runtime failure", false)
		n.terminal = true
		n.ready = false
		n.outputs.InvalidateAllLocked(n.W, n.ID)
		n.runtimeMu.Lock()
		runtime := n.runtime
		n.runtimeMu.Unlock()
		if runtime != nil && runtime.id == message.runtimeID {
			runtime.cancel()
		}
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *xTorDriverNode) handleServiceEvent(message xTorServiceEvent) {
	if message.runtimeID == 0 || message.runtimeID != n.activeID {
		return
	}
	status, ok := n.services[message.name]
	if !ok {
		return
	}
	if event, ok := message.event.(tor.PublicationEvent); ok {
		status.Publication = event.State
		status.Problem = string(event.Problem)
		n.services[message.name] = status
		_ = n.updateLabel()
		n.sendStatusBlock()
	}
}

func (n *xTorDriverNode) handleOutboundResult(result xTorOutboundResult) {
	if result.runtimeID == 0 || result.runtimeID != n.activeID {
		return
	}
	if n.terminal {
		return
	}
	if result.outboundRevision != n.currentOutboundRevision() {
		return
	}
	n.outboundState = result.state
	if result.err != nil {
		n.setRuntimeError("update Tor outbound network: "+result.err.Error(), true)
	} else {
		n.clearRuntimeError()
	}
	if result.err == nil && result.ready {
		n.ready = true
		n.sendAllOutputs()
	} else {
		n.ready = false
		n.outputs.InvalidateAllLocked(n.W, n.ID)
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *xTorDriverNode) setRuntimeError(message string, recoverable bool) {
	n.runtimeError = message
	n.runtimeErrorRecoverable = message != "" && recoverable
}

func (n *xTorDriverNode) clearRuntimeError() {
	n.runtimeError = ""
	n.runtimeErrorRecoverable = false
}

func cloneXTorDriverConfig(config XTorDriverConfig) XTorDriverConfig {
	clone := config
	clone.Networks = append([]XTorDriverNetworkConfig(nil), config.Networks...)
	clone.Services = append([]XTorDriverServiceConfig(nil), config.Services...)
	for i := range clone.Services {
		clone.Services[i].Ports = append([]uint16(nil), config.Services[i].Ports...)
		clone.Services[i].AuthorizedClients = append([]string(nil), config.Services[i].AuthorizedClients...)
	}
	clone.ClientAuthorizations = append([]XTorDriverClientAuthConfig(nil), config.ClientAuthorizations...)
	clone.Driver.ReachableORPorts = append([]uint16(nil), config.Driver.ReachableORPorts...)
	clone.Driver.EntryNodes = append([]string(nil), config.Driver.EntryNodes...)
	clone.Driver.ExitNodes = append([]string(nil), config.Driver.ExitNodes...)
	clone.Driver.ExcludeNodes = append([]string(nil), config.Driver.ExcludeNodes...)
	clone.Driver.ExcludeExitNodes = append([]string(nil), config.Driver.ExcludeExitNodes...)
	clone.Driver.Bridges = append([]XTorDriverBridge(nil), config.Driver.Bridges...)
	return clone
}

func (n *xTorDriverNode) currentNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok && strings.TrimSpace(snapshot.Name) != "" {
			return snapshot.Name
		}
	}
	return n.nodeName
}
