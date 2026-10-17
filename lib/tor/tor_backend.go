package tornodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"

	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/gonnect"
	tor "github.com/asciimoth/tor-driver"

	"github.com/asciimoth/almagest/lib/storage"
)

type torDriverBackend interface {
	WaitReady(context.Context) error
	SubscribeEvents(int) (<-chan tor.DriverEvent, func(), error)
	SetOutbound(gonnect.Network) error
	OutboundState() tor.OutboundState
	NewNetwork(tor.NetworkConfig) (torNetwork, error)
	NewService(context.Context, tor.ServiceConfig) (torService, error)
	GenerateClientAuthorization(string) (tor.ClientAuthorization, error)
	AddClientAuthorization(context.Context, string, tor.ClientAuthorization) error
	Done() <-chan struct{}
	Err() error
	Close() error
}

type torNetwork interface {
	gonnect.Network
	io.Closer
}

type torService interface {
	torNetwork
	Address() string
	SubscribeEvents(int) (<-chan tor.ServiceEvent, func(), error)
}

type torStartFactory func(context.Context, tor.Config, tor.Dependencies) (torDriverBackend, error)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Timeout(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, duration)
}
func (realClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func torDependencies(sandboxes sandbox.OwnerSandboxes, outbound gonnect.Network, logger tor.Logger) tor.Dependencies {
	return tor.Dependencies{
		Sandboxes: sandboxes,
		Clock:     realClock{},
		Random:    rand.Reader,
		Logger:    logger,
		Outbound:  outbound,
	}
}

type realTorDriver struct{ driver *tor.Driver }

func startRealTorDriver(ctx context.Context, cfg tor.Config, deps tor.Dependencies) (torDriverBackend, error) {
	driver, err := tor.Start(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	return &realTorDriver{driver: driver}, nil
}

func (d *realTorDriver) WaitReady(ctx context.Context) error { return d.driver.WaitReady(ctx) }
func (d *realTorDriver) SubscribeEvents(size int) (<-chan tor.DriverEvent, func(), error) {
	return d.driver.SubscribeEvents(size)
}
func (d *realTorDriver) SetOutbound(network gonnect.Network) error {
	return d.driver.SetOutbound(network)
}
func (d *realTorDriver) OutboundState() tor.OutboundState { return d.driver.OutboundState() }
func (d *realTorDriver) NewNetwork(cfg tor.NetworkConfig) (torNetwork, error) {
	return d.driver.NewNetwork(cfg)
}
func (d *realTorDriver) NewService(ctx context.Context, cfg tor.ServiceConfig) (torService, error) {
	return d.driver.NewService(ctx, cfg)
}
func (d *realTorDriver) GenerateClientAuthorization(name string) (tor.ClientAuthorization, error) {
	return d.driver.GenerateClientAuthorization(name)
}
func (d *realTorDriver) AddClientAuthorization(ctx context.Context, address string, auth tor.ClientAuthorization) error {
	return d.driver.AddClientAuthorization(ctx, address, auth)
}
func (d *realTorDriver) Done() <-chan struct{} { return d.driver.Done() }
func (d *realTorDriver) Err() error            { return d.driver.Err() }
func (d *realTorDriver) Close() error          { return d.driver.Close() }

// torOnionKeyStore adapts node-scoped Almagest secrets to tor-driver's
// create-if-absent key store. Logical names are private to one Driver. Each
// generated selector stays generation-owned until the workspace callback
// commits the matching runtime result.
type torOnionKeyStore struct {
	mu          sync.Mutex
	storage     storage.SecretsStorage
	nodeName    string
	selectors   map[string]string
	generated   map[string]string
	onGenerated func(string)
}

func newTorOnionKeyStore(store storage.SecretsStorage, nodeName string, services []TorDriverServiceConfig, onGenerated func(string)) *torOnionKeyStore {
	selectors := make(map[string]string, len(services))
	for _, service := range services {
		selectors[torServiceKeyName(service.Name)] = service.KeySecret
	}
	return &torOnionKeyStore{
		storage: store, nodeName: nodeName, selectors: selectors,
		generated: make(map[string]string), onGenerated: onGenerated,
	}
}

func torServiceKeyName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "service-" + hex.EncodeToString(sum[:16])
}

func torServiceSecretOwner(name string) string {
	return "tor/onion-service/" + name
}

func torClientAuthorizationSecretOwner(address string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(address)))
	return "tor/client-authorization/" + hex.EncodeToString(sum[:16])
}

func (s *torOnionKeyStore) Load(_ context.Context, name string) (tor.OnionServiceKey, error) {
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
		selector, secret, err := generated.RecoverGeneratedSecret(torServiceSecretOwner(name), s.nodeName)
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
		_ = commitTorGeneratedSecret(s.storage, selector, s.nodeName)
	}
	return key, err
}

func (s *torOnionKeyStore) Store(_ context.Context, name string, key tor.OnionServiceKey) error {
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
	selector, err := newTorGeneratedSecret(s.storage, storage.Secret{Value: string(text)}, s.nodeName, torServiceSecretOwner(name))
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

func newTorGeneratedSecret(store storage.SecretsStorage, secret storage.Secret, nodeName, owner string) (string, error) {
	generated, ok := store.(storage.GeneratedSecretsStorage)
	if !ok {
		return "", errors.New("tor generated-secret cleanup is unavailable")
	}
	return generated.NewGeneratedSecret(secret, nodeName, owner)
}

func commitTorGeneratedSecret(store storage.SecretsStorage, selector, nodeName string) error {
	generated, ok := store.(storage.GeneratedSecretsStorage)
	if !ok {
		return errors.New("tor generated-secret commit is unavailable")
	}
	return generated.CommitGeneratedSecret(selector, nodeName)
}

func (s *torOnionKeyStore) generatedSelectors() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.generated))
	for name, selector := range s.generated {
		out[name] = selector
	}
	return out
}

var _ tor.OnionKeyStore = (*torOnionKeyStore)(nil)
