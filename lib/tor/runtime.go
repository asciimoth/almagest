package tornodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	tor "github.com/asciimoth/tor-driver"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const torOutboundRetryDelay = 250 * time.Millisecond

type torRuntime struct {
	id                     uint64
	ctx                    context.Context
	cancel                 context.CancelFunc
	driver                 torDriverBackend
	sources                map[string]gonnect.Network
	services               map[string]torService
	unsubs                 []func()
	startedWithoutOutbound atomic.Bool

	outboundWake     chan struct{}
	outboundRevision atomic.Uint64
	outboundApplied  atomic.Bool
}

func (r *torRuntime) close() error {
	if r == nil {
		return nil
	}
	r.cancel()
	for _, unsubscribe := range r.unsubs {
		if unsubscribe != nil {
			unsubscribe()
		}
	}
	var err error
	if r.driver != nil {
		err = errors.Join(err, r.driver.Close())
	}
	return err
}

func (r *torRuntime) signalOutbound() {
	if r == nil {
		return
	}
	select {
	case r.outboundWake <- struct{}{}:
	default:
	}
}

type torStarted struct {
	generation uint64
	runtimeID  uint64
	nodeName   string
	config     TorDriverConfig
	sources    map[string]gonnect.Network
	services   map[string]torServiceStatus
	state      tor.OutboundState
	activate   chan struct{}
	err        error
}

type torDriverEvent struct {
	runtimeID uint64
	event     tor.DriverEvent
}

type torServiceEvent struct {
	runtimeID uint64
	name      string
	event     tor.ServiceEvent
}

type torOutboundResult struct {
	runtimeID        uint64
	outboundRevision uint64
	state            tor.OutboundState
	ready            bool
	err              error
}

type torReadyResult struct {
	outboundRevision uint64
	err              error
}

type torConfigPersisted struct {
	id  uint64
	err error
}

type torGeneratedSecret struct {
	selector string
	nodeName string
}

type torPendingPersistence struct {
	secrets []torGeneratedSecret
	durable atomic.Bool
}

func (n *torDriverNode) scheduleRebuild(_ string) {
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
	n.outboundLatched = false
	n.latchedRevision = 0
	n.clearRuntimeError()
	n.bootstrap = tor.BootstrapEvent{}
	n.services = make(map[string]torServiceStatus)
	_ = n.updateLabel()
	n.sendStatusBlock()

	config := cloneTorDriverConfig(n.config)
	nodeName := n.currentNodeName()
	if err := n.SpawnWorker("TorDriver runtime reconcile", func() {
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

func (n *torDriverNode) cancelCurrentRun() {
	n.cancelMu.Lock()
	cancel := n.cancelRun
	n.cancelRun = nil
	n.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (n *torDriverNode) reconcile(ctx context.Context, generation uint64, config TorDriverConfig, nodeName string) (torStarted, bool) {
	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	if generation != n.generation.Load() || ctx.Err() != nil {
		return torStarted{}, false
	}
	n.closeRuntime()
	result := torStarted{generation: generation, nodeName: nodeName, config: config}
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
		return torStarted{}, false
	}
	n.runtimeMu.Lock()
	n.runtime = runtime
	n.runtimeMu.Unlock()

	result.runtimeID = runtime.id
	result.sources = runtime.sources
	result.services = make(map[string]torServiceStatus, len(runtime.services))
	for name, service := range runtime.services {
		result.services[name] = torServiceStatus{Address: service.Address(), Publication: tor.PublicationPending}
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

func (n *torDriverNode) buildRuntime(ctx context.Context, generation uint64, config TorDriverConfig, nodeName string) (_ *torRuntime, updated TorDriverConfig, err error) {
	updated = cloneTorDriverConfig(config)
	torConfig := config.torConfig()
	sandboxes := n.sandboxes
	if sandboxes == nil {
		sandboxes = n.Sandboxes
	}
	ownerSandboxes, err := sandboxes(ctx)
	if err != nil {
		return nil, updated, fmt.Errorf("acquire Tor sandbox capability: %w", err)
	}
	outbound, outboundRevision := n.currentOutboundState()
	runtimeCtx, cancel := context.WithCancel(n.Context())
	runtime := &torRuntime{
		ctx: runtimeCtx, cancel: cancel,
		sources: make(map[string]gonnect.Network), services: make(map[string]torService),
		outboundWake: make(chan struct{}, 1),
	}
	runtime.startedWithoutOutbound.Store(outbound == nil)
	success := false
	defer func() {
		if !success {
			err = errors.Join(err, runtime.close())
		}
	}()

	runtime.outboundRevision.Store(outboundRevision)
	keyStore := newTorOnionKeyStore(n.SecretsStorage(), nodeName, updated.Services, func(selector string) {
		n.trackGeneratedSecret(generation, nodeName, selector)
	})
	deps := torDependencies(ownerSandboxes, outbound, n.Logger)
	deps.OnionKeys = keyStore
	driver, err := n.start(ctx, torConfig, deps)
	if err != nil {
		return nil, updated, fmt.Errorf("start Tor driver: %w", err)
	}
	runtime.driver = driver
	runtime.outboundApplied.Store(outbound == nil || driver.OutboundState().Available)

	for _, network := range updated.Networks {
		value, createErr := driver.NewNetwork(network.torConfig())
		if createErr != nil {
			return nil, updated, fmt.Errorf("create Tor network %q: %w", network.Name, createErr)
		}
		runtime.sources[torNetworkKey(network.Name)] = value
	}
	for i, service := range updated.Services {
		serviceConfig, configErr := torServiceConfig(service)
		if configErr != nil {
			return nil, updated, configErr
		}
		value, createErr := driver.NewService(ctx, serviceConfig)
		if selector := keyStore.generatedSelectors()[torServiceKeyName(service.Name)]; selector != "" {
			updated.Services[i].KeySecret = selector
		}
		if createErr != nil {
			return nil, updated, fmt.Errorf("create onion service %q: %w", service.Name, createErr)
		}
		runtime.services[service.Name] = value
		runtime.sources[torServiceKey(service.Name)] = value
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

func torServiceConfig(config TorDriverServiceConfig) (tor.ServiceConfig, error) {
	out := tor.ServiceConfig{
		Ports: append([]uint16(nil), config.Ports...), MaxStreams: config.MaxStreams,
		KeyName: torServiceKeyName(config.Name),
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

func (n *torDriverNode) installClientAuthorizations(ctx context.Context, generation uint64, driver torDriverBackend, nodeName string, config *TorDriverConfig) error {
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
			owner := torClientAuthorizationSecretOwner(item.Address)
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
				selector, storeErr := newTorGeneratedSecret(store, storage.Secret{Value: string(text)}, nodeName, owner)
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
			_ = commitTorGeneratedSecret(store, item.PrivateKeySecret, nodeName)
			authorization = tor.ClientAuthorization{PrivateKey: privateKey, Name: item.Name}
		}
		if err := driver.AddClientAuthorization(ctx, item.Address, authorization); err != nil {
			return fmt.Errorf("add client authorization for %q: %w", item.Address, err)
		}
	}
	return nil
}

func (n *torDriverNode) spawnRuntimeWatchers(runtime *torRuntime, activate <-chan struct{}) error {
	events, unsubscribe, err := runtime.driver.SubscribeEvents(32)
	if err != nil {
		return fmt.Errorf("subscribe to Tor driver events: %w", err)
	}
	runtime.unsubs = append(runtime.unsubs, unsubscribe)
	if err = n.SpawnWorker("TorDriver event watcher", func() {
		if !waitTorActivation(activate, runtime.ctx.Done()) {
			return
		}
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				n.sendRuntimeInbox(torDriverEvent{runtimeID: runtime.id, event: event})
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
		if err = n.SpawnWorker("TorDriver onion service event watcher", func() {
			if !waitTorActivation(activate, runtime.ctx.Done()) {
				return
			}
			for {
				select {
				case event, ok := <-events:
					if !ok {
						return
					}
					n.sendRuntimeInbox(torServiceEvent{runtimeID: runtime.id, name: name, event: event})
				case <-runtime.ctx.Done():
					return
				}
			}
		}); err != nil {
			return err
		}
	}
	return n.SpawnWorker("TorDriver outbound reconciler", func() {
		if !waitTorActivation(activate, runtime.ctx.Done()) {
			return
		}
		n.watchRuntimeOutbound(runtime)
	})
}

func (n *torDriverNode) watchRuntimeOutbound(runtime *torRuntime) {
	var waitResult chan torReadyResult
reconcileLoop:
	for {
		network, revision := n.currentOutboundState()
		var err error
		if !runtime.outboundApplied.Load() || revision != runtime.outboundRevision.Load() {
			err = runtime.driver.SetOutbound(network)
			if err == nil {
				runtime.outboundRevision.Store(revision)
				runtime.outboundApplied.Store(true)
				if network != nil {
					runtime.startedWithoutOutbound.Store(false)
				}
			}
		}
		state := runtime.driver.OutboundState()
		if network == nil && !state.Available {
			err = nil
		}
		ready := false
		if err == nil && network != nil {
			if n.currentOutboundRevision() != revision {
				continue
			}
			if waitResult == nil {
				waitResult = make(chan torReadyResult, 1)
				result := waitResult
				go func() {
					result <- torReadyResult{
						outboundRevision: revision,
						err:              runtime.driver.WaitReady(runtime.ctx),
					}
				}()
			}
		waitLoop:
			for {
				select {
				case readyResult := <-waitResult:
					waitResult = nil
					if readyResult.outboundRevision != revision {
						continue reconcileLoop
					}
					err = readyResult.err
					ready = err == nil
					break waitLoop
				case <-runtime.outboundWake:
					if n.currentOutboundRevision() != revision {
						// WaitReady describes the attachment for this revision.
						// It can finish after SetOutbound installs a replacement.
						// Leave its buffered result for collection and start a new
						// wait for the replacement revision.
						waitResult = nil
						continue reconcileLoop
					}
				case <-runtime.ctx.Done():
					return
				}
			}
		}
		if runtime.ctx.Err() != nil {
			return
		}
		if n.currentOutboundRevision() != revision {
			continue
		}
		state = runtime.driver.OutboundState()
		n.sendRuntimeInbox(torOutboundResult{
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
		timer := time.NewTimer(torOutboundRetryDelay)
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

func waitTorActivation(activate, done <-chan struct{}) bool {
	select {
	case <-activate:
		return true
	case <-done:
		return false
	}
}

func (n *torDriverNode) sendRuntimeInbox(payload any) {
	if n.W != nil {
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: payload})
	}
}

func (n *torDriverNode) closeRuntime() {
	n.runtimeMu.Lock()
	runtime := n.runtime
	n.runtime = nil
	n.runtimeMu.Unlock()
	if runtime != nil {
		_ = runtime.close()
	}
}

func (n *torDriverNode) applyOutbound(network nodekit.NetworkCloser) {
	n.ready = false
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	if network == nil && !n.terminal {
		n.clearRuntimeError()
		n.outboundState.Available = false
	} else if network != nil {
		// A new graph attachment is the explicit rearm required by tor-driver
		// after it latches a failed attachment.
		n.outboundLatched = false
		n.latchedRevision = 0
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	n.runtimeMu.Lock()
	runtime := n.runtime
	n.runtimeMu.Unlock()
	if runtime == nil || runtime.driver == nil {
		return
	}
	if network != nil && runtime.startedWithoutOutbound.Load() && n.bootstrap.Progress > 0 {
		// Tor has already entered its retry schedule through the unavailable
		// proxy gate. Build a clean runtime with the first real
		// attachment as an initial dependency. This is the same reliable path as
		// restoring an already-connected graph, and it avoids retaining offline
		// bootstrap state after a node is wired later.
		n.scheduleRebuild("outbound attached after offline bootstrap")
		return
	}
	runtime.signalOutbound()
}

func (n *torDriverNode) handleStarted(result torStarted) {
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
	configChanged := !torConfigEqual(n.config, result.config)
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

func (n *torDriverNode) trackGeneratedSecret(generation uint64, nodeName, selector string) {
	if selector == "" {
		return
	}
	secret := torGeneratedSecret{selector: selector, nodeName: nodeName}
	if generation != n.generation.Load() {
		n.deleteGeneratedSecret(secret)
		return
	}
	n.generatedMu.Lock()
	if generation == n.generation.Load() {
		if n.generated == nil {
			n.generated = make(map[uint64][]torGeneratedSecret)
		}
		n.generated[generation] = append(n.generated[generation], secret)
		n.generatedMu.Unlock()
		return
	}
	n.generatedMu.Unlock()
	n.deleteGeneratedSecret(secret)
}

func (n *torDriverNode) takeGeneratedSecrets(generation uint64) []torGeneratedSecret {
	n.generatedMu.Lock()
	secrets := append([]torGeneratedSecret(nil), n.generated[generation]...)
	delete(n.generated, generation)
	n.generatedMu.Unlock()
	return secrets
}

func (n *torDriverNode) persistGeneratedSecrets(secrets []torGeneratedSecret) {
	if len(secrets) == 0 {
		return
	}
	n.persistNext++
	id := n.persistNext
	if n.persisting == nil {
		n.persisting = make(map[uint64]*torPendingPersistence)
	}
	pending := &torPendingPersistence{secrets: append([]torGeneratedSecret(nil), secrets...)}
	n.persisting[id] = pending
	result := n.NotifyConfigChanged()
	if result == nil {
		n.handleConfigPersisted(torConfigPersisted{id: id, err: errors.New("node configuration persistence returned no result")})
		return
	}
	err := n.SpawnWorker("TorDriver generated-secret persistence", func() {
		select {
		case persistErr, ok := <-result:
			if !ok {
				persistErr = errors.New("node configuration persistence ended without a result")
			}
			if persistErr == nil {
				pending.durable.Store(true)
			}
			n.sendRuntimeInbox(torConfigPersisted{id: id, err: persistErr})
		case <-n.persistStop:
		}
	})
	if err != nil {
		n.handleConfigPersisted(torConfigPersisted{id: id, err: err})
	}
}

func (n *torDriverNode) handleConfigPersisted(result torConfigPersisted) {
	pending := n.persisting[result.id]
	if pending == nil {
		return
	}
	secrets := append([]torGeneratedSecret(nil), pending.secrets...)
	delete(n.persisting, result.id)
	if result.err == nil {
		for _, secret := range secrets {
			if torConfigReferencesSecret(n.config, secret.selector) {
				if err := commitTorGeneratedSecret(n.SecretsStorage(), secret.selector, secret.nodeName); err != nil && n.Logger != nil {
					n.Logger.Errf("commit generated Tor secret: %v", err)
				}
			} else {
				n.deleteGeneratedSecret(secret, n.currentNodeName())
			}
		}
		return
	}

	affectsCurrentConfig := false
	for _, secret := range secrets {
		if torConfigReferencesSecret(n.config, secret.selector) {
			affectsCurrentConfig = true
			clearTorGeneratedSelector(&n.config, secret.selector)
		}
		n.deleteGeneratedSecret(secret, n.currentNodeName())
	}
	if !affectsCurrentConfig {
		return
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

func torConfigReferencesSecret(config TorDriverConfig, selector string) bool {
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

func clearTorGeneratedSelector(config *TorDriverConfig, selector string) {
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

func (n *torDriverNode) deleteGeneratedSecrets(secrets []torGeneratedSecret) {
	for _, secret := range secrets {
		n.deleteGeneratedSecret(secret, n.currentNodeName())
	}
}

func (n *torDriverNode) discardAllPersistingSecrets() {
	for id, pending := range n.persisting {
		if pending != nil && !pending.durable.Load() {
			n.deleteGeneratedSecrets(pending.secrets)
		}
		delete(n.persisting, id)
	}
}

func (n *torDriverNode) renameTrackedGeneratedSecret(previous, next string) {
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

func (n *torDriverNode) discardGeneratedSecretsBefore(generation uint64) {
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

func (n *torDriverNode) discardAllGeneratedSecrets() {
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

func (n *torDriverNode) discardGeneratedSecrets(generation uint64, alternativeNodeNames ...string) {
	n.generatedMu.Lock()
	secrets := append([]torGeneratedSecret(nil), n.generated[generation]...)
	delete(n.generated, generation)
	n.generatedMu.Unlock()
	for _, secret := range secrets {
		n.deleteGeneratedSecret(secret, alternativeNodeNames...)
	}
}

func (n *torDriverNode) deleteGeneratedSecret(secret torGeneratedSecret, alternativeNodeNames ...string) {
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

func (n *torDriverNode) handleDriverEvent(message torDriverEvent) {
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
		// The graph can change before its SetOutbound call runs. An event from
		// the previous driver generation must not latch the pending attachment.
		revision := n.currentOutboundRevision()
		if !runtime.outboundApplied.Load() || runtime.outboundRevision.Load() != revision {
			return
		}
		state := runtime.driver.OutboundState()
		if event.Generation != state.Generation {
			return
		}
		n.outboundState = state
		if event.Latched && !state.Available {
			n.outboundLatched = true
			n.latchedRevision = revision
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

func (n *torDriverNode) handleServiceEvent(message torServiceEvent) {
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

func (n *torDriverNode) handleOutboundResult(result torOutboundResult) {
	if result.runtimeID == 0 || result.runtimeID != n.activeID {
		return
	}
	if n.terminal {
		return
	}
	if result.outboundRevision != n.currentOutboundRevision() {
		return
	}
	if n.outboundLatched && result.outboundRevision == n.latchedRevision {
		return
	}
	n.outboundState = result.state
	if result.err != nil {
		n.setRuntimeError("update Tor outbound network: "+result.err.Error(), true)
	} else {
		n.clearRuntimeError()
	}
	if result.err == nil && result.ready {
		n.outboundLatched = false
		n.latchedRevision = 0
		n.ready = true
		n.sendAllOutputs()
	} else {
		n.ready = false
		n.outputs.InvalidateAllLocked(n.W, n.ID)
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *torDriverNode) setRuntimeError(message string, recoverable bool) {
	n.runtimeError = message
	n.runtimeErrorRecoverable = message != "" && recoverable
}

func (n *torDriverNode) clearRuntimeError() {
	n.runtimeError = ""
	n.runtimeErrorRecoverable = false
}

func cloneTorDriverConfig(config TorDriverConfig) TorDriverConfig {
	clone := config
	clone.Networks = append([]TorDriverNetworkConfig(nil), config.Networks...)
	clone.Services = append([]TorDriverServiceConfig(nil), config.Services...)
	for i := range clone.Services {
		clone.Services[i].Ports = append([]uint16(nil), config.Services[i].Ports...)
		clone.Services[i].AuthorizedClients = append([]string(nil), config.Services[i].AuthorizedClients...)
	}
	clone.ClientAuthorizations = append([]TorDriverClientAuthConfig(nil), config.ClientAuthorizations...)
	clone.Driver.ReachableORPorts = append([]uint16(nil), config.Driver.ReachableORPorts...)
	clone.Driver.EntryNodes = append([]string(nil), config.Driver.EntryNodes...)
	clone.Driver.ExitNodes = append([]string(nil), config.Driver.ExitNodes...)
	clone.Driver.ExcludeNodes = append([]string(nil), config.Driver.ExcludeNodes...)
	clone.Driver.ExcludeExitNodes = append([]string(nil), config.Driver.ExcludeExitNodes...)
	clone.Driver.Bridges = append([]TorDriverBridge(nil), config.Driver.Bridges...)
	return clone
}

func (n *torDriverNode) currentNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok && strings.TrimSpace(snapshot.Name) != "" {
			return snapshot.Name
		}
	}
	return n.nodeName
}
