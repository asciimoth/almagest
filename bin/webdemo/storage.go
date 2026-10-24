package webdemo

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/asciimoth/almagest/lib/storage"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

const (
	kvStorageKey      = "almagest.webdemo.kv.v1"
	secretsStorageKey = "almagest.webdemo.secrets.v1"
)

// Persistence is the small synchronous browser-storage contract used by the
// web demo. The WASM command implements it with window.localStorage.
type Persistence interface {
	Load(key string) (value []byte, found bool, err error)
	Store(key string, value []byte) error
}

// Storage contains the storage services used by the web demo engine.
type Storage struct {
	KV      storage.KVManager
	DNS     storage.DnsCacheManager
	Secrets storage.SecretsStorage
	TLS     storage.TlsConfigStorage
}

// NewStorage loads persistent KV and secret data and creates empty in-memory
// DNS and TLS stores.
func NewStorage(persistence Persistence) (*Storage, error) {
	if persistence == nil {
		return nil, fmt.Errorf("web demo storage: nil persistence")
	}
	kv, err := newKVManager(persistence)
	if err != nil {
		return nil, err
	}
	secrets, err := newSecretsStorage(persistence)
	if err != nil {
		return nil, err
	}
	return &Storage{
		KV:      kv,
		DNS:     &memoryDNSManager{stores: map[string]*gdns.MemoryStorage{}},
		Secrets: secrets,
		TLS:     memoryTLSStorage{},
	}, nil
}

type persistedKVState struct {
	Root *kvNode `json:"root"`
}

type kvNode struct {
	Owner    string                      `json:"owner,omitempty"`
	Values   map[string]persistedKVValue `json:"values,omitempty"`
	Subtrees map[string]*kvNode          `json:"subtrees,omitempty"`
}

type persistedKVValue struct {
	Data   []byte `json:"data,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
	String bool   `json:"string,omitempty"`
	Owner  string `json:"owner,omitempty"`
}

type kvManager struct {
	mu          sync.Mutex
	persistence Persistence
	state       persistedKVState
}

type kvStore struct {
	manager        *kvManager
	path           []string
	protectedDepth int
	invalid        bool
	invalidOwner   bool
	owner          string
	ownershipDepth int
}

type kvReader struct {
	state   *persistedKVState
	path    []string
	invalid bool
}

type kvReadTx struct{ kvReader }

type kvWriteTx struct {
	kvReader
	protectedDepth int
	owner          string
	ownershipDepth int
}

func newKVManager(persistence Persistence) (*kvManager, error) {
	state := persistedKVState{Root: newKVNode()}
	raw, found, err := persistence.Load(kvStorageKey)
	if err != nil {
		return nil, fmt.Errorf("load web demo KV storage: %w", err)
	}
	if found {
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, fmt.Errorf("decode web demo KV storage: %w", err)
		}
		if err := validateKVNode(state.Root); err != nil {
			return nil, fmt.Errorf("decode web demo KV storage: %w", err)
		}
	}
	ensureKVNode(state.Root)
	ensureChild(state.Root, "default")
	ensureChild(state.Root, "plugins")
	return &kvManager{persistence: persistence, state: state}, nil
}

func (m *kvManager) DefaultStore() storage.KVStore {
	return &kvStore{manager: m, path: []string{"default"}, protectedDepth: 1}
}

func (m *kvManager) PluginStore(name string) storage.KVStore {
	return &kvStore{manager: m, path: []string{"plugins", name}, protectedDepth: 2, invalid: !validKVName(name)}
}

func (s *kvStore) Subtree(path ...string) storage.KVStore {
	return &kvStore{
		manager: s.manager, path: append(append([]string(nil), s.path...), path...),
		protectedDepth: s.protectedDepth, invalid: s.invalid || !validKVPath(path),
		invalidOwner: s.invalidOwner, owner: s.owner, ownershipDepth: s.ownershipDepth,
	}
}

func (s *kvStore) OwnedBy(owner string) storage.KVStore {
	return &kvStore{
		manager: s.manager, path: append([]string(nil), s.path...), protectedDepth: s.protectedDepth,
		invalid: s.invalid, invalidOwner: pasta.ValidateNodeName(owner) != nil,
		owner: owner, ownershipDepth: len(s.path),
	}
}

func (s *kvStore) View(ctx context.Context, fn func(storage.KVReadTx) error) error {
	if s.invalid {
		return storage.ErrKVInvalidName
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	return fn(&kvReadTx{kvReader{state: &s.manager.state, path: append([]string(nil), s.path...)}})
}

func (s *kvStore) Update(ctx context.Context, fn func(storage.KVWriteTx) error) error {
	if s.invalid {
		return storage.ErrKVInvalidName
	}
	if s.invalidOwner {
		return storage.ErrKVInvalidOwner
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	next := cloneKVState(s.manager.state)
	tx := &kvWriteTx{
		kvReader:       kvReader{state: &next, path: append([]string(nil), s.path...)},
		protectedDepth: s.protectedDepth, owner: s.owner, ownershipDepth: s.ownershipDepth,
	}
	if err := fn(tx); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode web demo KV storage: %w", err)
	}
	if err := s.manager.persistence.Store(kvStorageKey, raw); err != nil {
		return fmt.Errorf("save web demo KV storage: %w", err)
	}
	s.manager.state = next
	return nil
}

func (r *kvReader) Exists() bool { return !r.invalid && nodeAt(r.state.Root, r.path) != nil }

func (r *kvReader) Owner() (string, error) {
	if r.invalid {
		return "", storage.ErrKVInvalidName
	}
	node := nodeAt(r.state.Root, r.path)
	if node == nil {
		return "", nil
	}
	return node.Owner, nil
}

func (r *kvReader) Get(key string) (*storage.KValue, bool, error) {
	if r.invalid || !validKVName(key) {
		return nil, false, storage.ErrKVInvalidName
	}
	node := nodeAt(r.state.Root, r.path)
	if node == nil {
		return nil, false, nil
	}
	value, ok := node.Values[key]
	if !ok {
		return nil, false, nil
	}
	return &storage.KValue{Data: append([]byte(nil), value.Data...), Hidden: value.Hidden, String: value.String, Owner: value.Owner}, true, nil
}

func (r *kvReader) ForEachValue(fn func(string, storage.KValue) error) error {
	if r.invalid {
		return storage.ErrKVInvalidName
	}
	node := nodeAt(r.state.Root, r.path)
	if node == nil {
		return nil
	}
	for key, value := range node.Values {
		if err := fn(key, storage.KValue{Data: append([]byte(nil), value.Data...), Hidden: value.Hidden, String: value.String, Owner: value.Owner}); err != nil {
			return err
		}
	}
	return nil
}

func (r *kvReader) ForEachSubtree(fn func(string) error) error {
	if r.invalid {
		return storage.ErrKVInvalidName
	}
	node := nodeAt(r.state.Root, r.path)
	if node == nil {
		return nil
	}
	for name := range node.Subtrees {
		if err := fn(name); err != nil {
			return err
		}
	}
	return nil
}

func (r *kvReadTx) Subtree(path ...string) storage.KVReadTx {
	return &kvReadTx{kvReader{state: r.state, path: append(append([]string(nil), r.path...), path...), invalid: r.invalid || !validKVPath(path)}}
}

func (w *kvWriteTx) Subtree(path ...string) storage.KVWriteTx {
	return &kvWriteTx{
		kvReader:       kvReader{state: w.state, path: append(append([]string(nil), w.path...), path...), invalid: w.invalid || !validKVPath(path)},
		protectedDepth: w.protectedDepth, owner: w.owner, ownershipDepth: w.ownershipDepth,
	}
}

func (w *kvWriteTx) Ensure() error {
	if w.invalid {
		return storage.ErrKVInvalidName
	}
	if _, err := ensurePath(w.state.Root, w.path); err != nil {
		return err
	}
	w.claimSubtrees()
	return nil
}

func (w *kvWriteTx) Set(key string, value storage.KValue) error {
	if w.invalid || !validKVName(key) {
		return storage.ErrKVInvalidName
	}
	if value.String && !utf8.Valid(value.Data) {
		return storage.ErrKVInvalidValue
	}
	node, err := ensurePath(w.state.Root, w.path)
	if err != nil {
		return err
	}
	if _, conflict := node.Subtrees[key]; conflict {
		return storage.ErrKVKindConflict
	}
	w.claimSubtrees()
	node.Values[key] = persistedKVValue{Data: append([]byte(nil), value.Data...), Hidden: value.Hidden, String: value.String, Owner: w.owner}
	return nil
}

func (w *kvWriteTx) Delete(key string) error {
	if w.invalid || !validKVName(key) {
		return storage.ErrKVInvalidName
	}
	node := nodeAt(w.state.Root, w.path)
	if node != nil {
		delete(node.Values, key)
	}
	return nil
}

func (w *kvWriteTx) Drop() error {
	if w.invalid {
		return storage.ErrKVInvalidName
	}
	if len(w.path) <= w.protectedDepth {
		return storage.ErrKVRoot
	}
	parent := nodeAt(w.state.Root, w.path[:len(w.path)-1])
	if parent != nil {
		delete(parent.Subtrees, w.path[len(w.path)-1])
	}
	return nil
}

func (w *kvWriteTx) claimSubtrees() {
	if w.owner == "" {
		return
	}
	for depth := max(w.ownershipDepth, w.protectedDepth+1); depth <= len(w.path); depth++ {
		if node := nodeAt(w.state.Root, w.path[:depth]); node != nil {
			node.Owner = w.owner
		}
	}
}

func newKVNode() *kvNode {
	return &kvNode{Values: map[string]persistedKVValue{}, Subtrees: map[string]*kvNode{}}
}

func ensureKVNode(node *kvNode) {
	if node.Values == nil {
		node.Values = map[string]persistedKVValue{}
	}
	if node.Subtrees == nil {
		node.Subtrees = map[string]*kvNode{}
	}
	for _, child := range node.Subtrees {
		ensureKVNode(child)
	}
}

func validateKVNode(node *kvNode) error {
	if node == nil {
		return fmt.Errorf("missing subtree")
	}
	if node.Owner != "" && pasta.ValidateNodeName(node.Owner) != nil {
		return storage.ErrKVInvalidOwner
	}
	for name, value := range node.Values {
		if !validKVName(name) {
			return storage.ErrKVInvalidName
		}
		if value.String && !utf8.Valid(value.Data) {
			return storage.ErrKVInvalidValue
		}
		if value.Owner != "" && pasta.ValidateNodeName(value.Owner) != nil {
			return storage.ErrKVInvalidOwner
		}
		if _, conflict := node.Subtrees[name]; conflict {
			return storage.ErrKVKindConflict
		}
	}
	for name, child := range node.Subtrees {
		if !validKVName(name) {
			return storage.ErrKVInvalidName
		}
		if err := validateKVNode(child); err != nil {
			return err
		}
	}
	return nil
}

func ensureChild(node *kvNode, name string) *kvNode {
	child := node.Subtrees[name]
	if child == nil {
		child = newKVNode()
		node.Subtrees[name] = child
	}
	return child
}

func ensurePath(root *kvNode, path []string) (*kvNode, error) {
	node := root
	for _, name := range path {
		if _, conflict := node.Values[name]; conflict {
			return nil, storage.ErrKVKindConflict
		}
		node = ensureChild(node, name)
	}
	return node, nil
}

func nodeAt(root *kvNode, path []string) *kvNode {
	node := root
	for _, name := range path {
		if node == nil {
			return nil
		}
		node = node.Subtrees[name]
	}
	return node
}

func cloneKVState(state persistedKVState) persistedKVState {
	raw, _ := json.Marshal(state)
	var out persistedKVState
	_ = json.Unmarshal(raw, &out)
	ensureKVNode(out.Root)
	return out
}

func validKVName(name string) bool { return name != "" }

func validKVPath(path []string) bool {
	for _, name := range path {
		if !validKVName(name) {
			return false
		}
	}
	return true
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type persistedSecrets struct {
	Records map[string]persistedSecret `json:"records,omitempty"`
}

type persistedSecret struct {
	Value string   `json:"value,omitempty"`
	Nodes []string `json:"nodes,omitempty"`
	Owner string   `json:"owner,omitempty"`
}

type secretsStorage struct {
	mu          sync.Mutex
	persistence Persistence
	records     map[string]persistedSecret
}

func newSecretsStorage(persistence Persistence) (*secretsStorage, error) {
	state := persistedSecrets{Records: map[string]persistedSecret{}}
	raw, found, err := persistence.Load(secretsStorageKey)
	if err != nil {
		return nil, fmt.Errorf("load web demo secrets: %w", err)
	}
	if found {
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, fmt.Errorf("decode web demo secrets: %w", err)
		}
		if state.Records == nil {
			state.Records = map[string]persistedSecret{}
		}
		for key, record := range state.Records {
			if key == "" || !utf8.ValidString(key) || !utf8.ValidString(record.Value) || !validSecretNodes(record.Nodes) || (record.Owner != "" && !validGeneratedOwner(record.Owner)) {
				return nil, fmt.Errorf("decode web demo secrets: invalid record %q", key)
			}
		}
	}
	return &secretsStorage{persistence: persistence, records: state.Records}, nil
}

func (s *secretsStorage) GetSecret(key, node string) (storage.Secret, error) {
	if key == "" || !utf8.ValidString(key) || pasta.ValidateNodeName(node) != nil {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok || !slices.Contains(record.Nodes, node) {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	return storage.Secret{Value: record.Value}, nil
}

func (s *secretsStorage) NewSecret(sec storage.Secret, node string) (string, error) {
	return s.newSecret(sec, node, "")
}

func (s *secretsStorage) NewGeneratedSecret(sec storage.Secret, node, owner string) (string, error) {
	if !validGeneratedOwner(owner) {
		return "", storage.ErrSecretInvalidName
	}
	return s.newSecret(sec, node, owner)
}

func (s *secretsStorage) newSecret(sec storage.Secret, node, owner string) (string, error) {
	if pasta.ValidateNodeName(node) != nil {
		return "", storage.ErrSecretInvalidName
	}
	if !utf8.ValidString(sec.Value) {
		return "", storage.ErrSecretInvalidValue
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner != "" {
		for key, record := range s.records {
			if record.Owner == owner && len(record.Nodes) == 1 && record.Nodes[0] == node {
				return key, nil
			}
		}
	}
	for range 64 {
		key, err := randomSecretKey()
		if err != nil {
			return "", err
		}
		if _, exists := s.records[key]; exists {
			continue
		}
		next := cloneSecrets(s.records)
		next[key] = persistedSecret{Value: sec.Value, Nodes: []string{node}, Owner: owner}
		if err := s.save(next); err != nil {
			return "", err
		}
		s.records = next
		return key, nil
	}
	return "", storage.ErrSecretConflict
}

func (s *secretsStorage) RecoverGeneratedSecret(owner, node string) (string, storage.Secret, error) {
	if !validGeneratedOwner(owner) || pasta.ValidateNodeName(node) != nil {
		return "", storage.Secret{}, storage.ErrSecretInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, record := range s.records {
		if record.Owner == owner && len(record.Nodes) == 1 && record.Nodes[0] == node {
			return key, storage.Secret{Value: record.Value}, nil
		}
	}
	return "", storage.Secret{}, nil
}

func (s *secretsStorage) CommitGeneratedSecret(key, node string) error {
	if key == "" || !utf8.ValidString(key) || pasta.ValidateNodeName(node) != nil {
		return storage.ErrSecretInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok || len(record.Nodes) != 1 || record.Nodes[0] != node {
		return storage.ErrSecretAccessDenied
	}
	next := cloneSecrets(s.records)
	record.Owner = ""
	next[key] = record
	if err := s.save(next); err != nil {
		return err
	}
	s.records = next
	return nil
}

func (s *secretsStorage) DeleteGeneratedSecret(key, node string) error {
	if key == "" || !utf8.ValidString(key) || pasta.ValidateNodeName(node) != nil {
		return storage.ErrSecretInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return nil
	}
	if len(record.Nodes) != 1 || record.Nodes[0] != node {
		return storage.ErrSecretAccessDenied
	}
	next := cloneSecrets(s.records)
	delete(next, key)
	if err := s.save(next); err != nil {
		return err
	}
	s.records = next
	return nil
}

func (s *secretsStorage) save(records map[string]persistedSecret) error {
	raw, err := json.Marshal(persistedSecrets{Records: records})
	if err != nil {
		return fmt.Errorf("encode web demo secrets: %w", err)
	}
	if err := s.persistence.Store(secretsStorageKey, raw); err != nil {
		return fmt.Errorf("save web demo secrets: %w", err)
	}
	return nil
}

func cloneSecrets(in map[string]persistedSecret) map[string]persistedSecret {
	out := make(map[string]persistedSecret, len(in))
	for key, record := range in {
		record.Nodes = append([]string(nil), record.Nodes...)
		out[key] = record
	}
	return out
}

func validSecretNodes(nodes []string) bool {
	for _, node := range nodes {
		if pasta.ValidateNodeName(node) != nil {
			return false
		}
	}
	return true
}

func validGeneratedOwner(owner string) bool {
	return owner != "" && len(owner) <= 256 && utf8.ValidString(owner)
}

func randomSecretKey() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "secret-" + hex.EncodeToString(data[:]), nil
}

type memoryDNSManager struct {
	mu     sync.Mutex
	stores map[string]*gdns.MemoryStorage
}

func (m *memoryDNSManager) Storage(name string) gdns.CacheStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	store := m.stores[name]
	if store == nil {
		store = gdns.NewMemoryStorage()
		m.stores[name] = store
	}
	return store
}

func (m *memoryDNSManager) StorageNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.stores))
	for name := range m.stores {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type memoryTLSStorage struct{}

func (memoryTLSStorage) ListConfigs() []string { return []string{} }

func (memoryTLSStorage) GetClientConfig(string, string) (tls.Config, error) {
	return tls.Config{}, storage.ErrTlsConfigNotFound
}

func (memoryTLSStorage) GetLocalCA(string) (tls.Certificate, error) {
	return tls.Certificate{}, storage.ErrTlsConfigNotFound
}

var (
	_ storage.KVManager               = (*kvManager)(nil)
	_ storage.KVStore                 = (*kvStore)(nil)
	_ storage.KVReadTx                = (*kvReadTx)(nil)
	_ storage.KVWriteTx               = (*kvWriteTx)(nil)
	_ storage.GeneratedSecretsStorage = (*secretsStorage)(nil)
	_ storage.DnsCacheStorageLister   = (*memoryDNSManager)(nil)
	_ storage.TlsConfigStorage        = memoryTLSStorage{}
)
