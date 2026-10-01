package experimentalnodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
	tor "github.com/asciimoth/tor-driver"
	"github.com/asciimoth/tor-driver/direct"

	"github.com/asciimoth/almagest/lib/storage"
)

type xTorDriverBackend interface {
	WaitReady(context.Context) error
	SubscribeEvents(int) (<-chan tor.DriverEvent, func(), error)
	SetOutbound(gonnect.Network) error
	OutboundState() tor.OutboundState
	NewNetwork(tor.NetworkConfig) (xTorNetwork, error)
	NewService(context.Context, tor.ServiceConfig) (xTorService, error)
	GenerateClientAuthorization(string) (tor.ClientAuthorization, error)
	AddClientAuthorization(context.Context, string, tor.ClientAuthorization) error
	Done() <-chan struct{}
	Err() error
	Close() error
}

type xTorNetwork interface {
	gonnect.Network
	io.Closer
}

type xTorService interface {
	xTorNetwork
	Address() string
	SubscribeEvents(int) (<-chan tor.ServiceEvent, func(), error)
}

type xTorSystem interface {
	Dependencies(gonnect.Network, gonnect.Network, tor.Logger) tor.Dependencies
	Close() error
}

type xTorSystemFactory func(tor.Config) (xTorSystem, tor.Config, error)
type xTorStartFactory func(context.Context, tor.Config, tor.Dependencies) (xTorDriverBackend, error)

type directXTorSystem struct {
	*direct.BestEffortSystem
	processes   tor.Processes
	warningOnce sync.Once
}

func newDirectXTorSystem(cfg tor.Config) (xTorSystem, tor.Config, error) {
	system, resolved, err := direct.NewBestEffortSystem(cfg)
	if err != nil {
		return nil, cfg, err
	}
	processes, err := newXTorProcessPolicy(system)
	if err != nil {
		return nil, cfg, errors.Join(err, system.Close())
	}
	return &directXTorSystem{BestEffortSystem: system, processes: processes}, resolved, nil
}

func (s *directXTorSystem) Dependencies(local, outbound gonnect.Network, logger tor.Logger) tor.Dependencies {
	deps := s.BestEffortSystem.Dependencies(local, outbound, logger)
	deps.Processes = s.processes
	if report := s.Report(); logger != nil && report.ContainmentError != nil {
		s.warningOnce.Do(func() {
			logger.Warnf("XTorDriver strict network isolation is unavailable; Tor can access the host network directly: %v", report.ContainmentError)
		})
	}
	return deps
}

type realXTorDriver struct{ driver *tor.Driver }

func startRealXTorDriver(ctx context.Context, cfg tor.Config, deps tor.Dependencies) (xTorDriverBackend, error) {
	driver, err := tor.Start(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	return &realXTorDriver{driver: driver}, nil
}

func (d *realXTorDriver) WaitReady(ctx context.Context) error { return d.driver.WaitReady(ctx) }
func (d *realXTorDriver) SubscribeEvents(size int) (<-chan tor.DriverEvent, func(), error) {
	return d.driver.SubscribeEvents(size)
}
func (d *realXTorDriver) SetOutbound(network gonnect.Network) error {
	return d.driver.SetOutbound(network)
}
func (d *realXTorDriver) OutboundState() tor.OutboundState { return d.driver.OutboundState() }
func (d *realXTorDriver) NewNetwork(cfg tor.NetworkConfig) (xTorNetwork, error) {
	return d.driver.NewNetwork(cfg)
}
func (d *realXTorDriver) NewService(ctx context.Context, cfg tor.ServiceConfig) (xTorService, error) {
	return d.driver.NewService(ctx, cfg)
}
func (d *realXTorDriver) GenerateClientAuthorization(name string) (tor.ClientAuthorization, error) {
	return d.driver.GenerateClientAuthorization(name)
}
func (d *realXTorDriver) AddClientAuthorization(ctx context.Context, address string, auth tor.ClientAuthorization) error {
	return d.driver.AddClientAuthorization(ctx, address, auth)
}
func (d *realXTorDriver) Done() <-chan struct{} { return d.driver.Done() }
func (d *realXTorDriver) Err() error            { return d.driver.Err() }
func (d *realXTorDriver) Close() error          { return d.driver.Close() }

// xTorOnionKeyStore adapts node-scoped Almagest secrets to tor-driver's
// create-if-absent key store. Logical names are private to one Driver. Each
// generated selector stays generation-owned until the workspace callback
// commits the matching runtime result.
type xTorOnionKeyStore struct {
	mu          sync.Mutex
	storage     storage.SecretsStorage
	nodeName    string
	selectors   map[string]string
	generated   map[string]string
	onGenerated func(string)
}

func newXTorOnionKeyStore(store storage.SecretsStorage, nodeName string, services []XTorDriverServiceConfig, onGenerated func(string)) *xTorOnionKeyStore {
	selectors := make(map[string]string, len(services))
	for _, service := range services {
		selectors[xTorServiceKeyName(service.Name)] = service.KeySecret
	}
	return &xTorOnionKeyStore{
		storage: store, nodeName: nodeName, selectors: selectors,
		generated: make(map[string]string), onGenerated: onGenerated,
	}
}

func xTorServiceKeyName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "service-" + hex.EncodeToString(sum[:16])
}

func xTorServiceSecretOwner(name string) string {
	return "xtor/onion-service/" + name
}

func xTorClientAuthorizationSecretOwner(address string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(address)))
	return "xtor/client-authorization/" + hex.EncodeToString(sum[:16])
}

func (s *xTorOnionKeyStore) Load(_ context.Context, name string) (tor.OnionServiceKey, error) {
	s.mu.Lock()
	selector := s.selectors[name]
	if s.storage == nil {
		s.mu.Unlock()
		return tor.OnionServiceKey{}, errors.New("tor onion key secret storage is unavailable")
	}
	if selector == "" {
		generated, ok := s.storage.(storage.GeneratedSecretsStorage)
		if !ok {
			s.mu.Unlock()
			return tor.OnionServiceKey{}, errors.New("tor generated-secret recovery is unavailable")
		}
		selector, secret, err := generated.RecoverGeneratedSecret(xTorServiceSecretOwner(name), s.nodeName)
		if err != nil {
			s.mu.Unlock()
			return tor.OnionServiceKey{}, err
		}
		if selector == "" {
			s.mu.Unlock()
			return tor.OnionServiceKey{}, fs.ErrNotExist
		}
		s.selectors[name] = selector
		s.generated[name] = selector
		if s.onGenerated != nil {
			s.onGenerated(selector)
		}
		key, parseErr := tor.ParseOnionServiceKey([]byte(secret.Value))
		s.mu.Unlock()
		return key, parseErr
	}
	s.mu.Unlock()
	secret, err := s.storage.GetSecret(selector, s.nodeName)
	if err != nil {
		return tor.OnionServiceKey{}, err
	}
	key, err := tor.ParseOnionServiceKey([]byte(secret.Value))
	if err == nil {
		_ = commitXTorGeneratedSecret(s.storage, selector, s.nodeName)
	}
	return key, err
}

func (s *xTorOnionKeyStore) Store(_ context.Context, name string, key tor.OnionServiceKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectors[name] != "" {
		return fs.ErrExist
	}
	if s.storage == nil {
		return errors.New("tor onion key secret storage is unavailable")
	}
	text, err := key.MarshalText()
	if err != nil {
		return err
	}
	defer clear(text)
	selector, err := newXTorGeneratedSecret(s.storage, storage.Secret{Value: string(text)}, s.nodeName, xTorServiceSecretOwner(name))
	if err != nil {
		return err
	}
	if selector == "" {
		return storage.ErrSecretInvalidName
	}
	s.selectors[name] = selector
	s.generated[name] = selector
	if s.onGenerated != nil {
		s.onGenerated(selector)
	}
	return nil
}

func newXTorGeneratedSecret(store storage.SecretsStorage, secret storage.Secret, nodeName, owner string) (string, error) {
	generated, ok := store.(storage.GeneratedSecretsStorage)
	if !ok {
		return "", errors.New("tor generated-secret cleanup is unavailable")
	}
	return generated.NewGeneratedSecret(secret, nodeName, owner)
}

func commitXTorGeneratedSecret(store storage.SecretsStorage, selector, nodeName string) error {
	generated, ok := store.(storage.GeneratedSecretsStorage)
	if !ok {
		return errors.New("tor generated-secret commit is unavailable")
	}
	return generated.CommitGeneratedSecret(selector, nodeName)
}

func (s *xTorOnionKeyStore) generatedSelectors() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.generated))
	for name, selector := range s.generated {
		out[name] = selector
	}
	return out
}

var _ tor.OnionKeyStore = (*xTorOnionKeyStore)(nil)
