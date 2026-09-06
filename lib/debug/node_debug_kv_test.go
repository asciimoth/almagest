package debugnodes

import (
	"context"
	"encoding/base64"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
)

func TestDebugKVClassShapeAndUnavailableMenu(t *testing.T) {
	t.Parallel()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(DebugKVClass{}); err != nil {
		t.Fatalf("AddNodeClass: %v", err)
	}

	state, sub := testkit.SubscribeMenuState(t, w)
	t.Cleanup(func() { w.UnsubscribeNotifications(sub) })
	node, err := w.AddNodeByClass(NodeTypeDebugKV, "kv")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)

	snapshot, ok := w.NodeSnapshot(node)
	if !ok {
		t.Fatal("node snapshot missing")
	}
	if len(snapshot.LeftPorts) != 0 || len(snapshot.RightPorts) != 0 {
		t.Fatalf("ports = %d left and %d right, want none", len(snapshot.LeftPorts), len(snapshot.RightPorts))
	}

	menu := debugKVMenu(t, state, node)
	entry := testkit.MustFindBlock(t, menu, debugKVEntryBlockID)
	for _, id := range []string{debugKVReadButton, debugKVSetButton, debugKVDeleteButton} {
		item := debugKVItem(t, entry, id)
		if item.Type != formular.ItemButton || !item.Inactive {
			t.Fatalf("%s button = %#v, want inactive button", id, item)
		}
	}
	if item := debugKVItem(t, entry, debugKVPathField); item.Value != "[]" {
		t.Fatalf("path value = %#v, want []", item.Value)
	}
	if item := debugKVItem(t, entry, debugKVStringField); item.Value != true {
		t.Fatalf("string value = %#v, want true", item.Value)
	}
}

func TestDebugKVSetReadUpdateAndDelete(t *testing.T) {
	t.Parallel()
	manager := newDebugKVMemoryManager()
	w, state, node := newDebugKVTestNode(t, manager)

	debugKVUpdateField(w, node, debugKVPathField, `["branch", "leaf"]`)
	debugKVUpdateField(w, node, debugKVKeyField, "entry")
	debugKVUpdateField(w, node, debugKVValueField, "first")
	debugKVUpdateField(w, node, debugKVHiddenField, true)
	debugKVPress(w, node, debugKVSetButton)
	assertDebugKVStatus(t, state, node, "created", "")

	got, found := manager.defaultStore.value([]string{"branch", "leaf"}, "entry")
	if !found || string(got.Data) != "first" || !got.String || !got.Hidden {
		t.Fatalf("created value = %#v, found %v", got, found)
	}

	debugKVUpdateField(w, node, debugKVValueField, "second")
	debugKVPress(w, node, debugKVSetButton)
	assertDebugKVStatus(t, state, node, "updated", "")

	manager.defaultStore.put([]string{"branch", "leaf"}, "entry", storage.KValue{
		Data:   []byte{0xde, 0xad, 0xbe, 0xef},
		Hidden: true,
	})
	debugKVPress(w, node, debugKVReadButton)
	assertDebugKVStatus(t, state, node, "read", "")
	entry := testkit.MustFindBlock(t, debugKVMenu(t, state, node), debugKVEntryBlockID)
	if got := debugKVItem(t, entry, debugKVValueField).Value; got != base64.StdEncoding.EncodeToString([]byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("read value = %#v", got)
	}
	if got := debugKVItem(t, entry, debugKVStringField).Value; got != false {
		t.Fatalf("read string flag = %#v, want false", got)
	}
	valueItem := debugKVItem(t, entry, debugKVValueField)
	if valueItem.Field == nil || !valueItem.Secret {
		t.Fatalf("hidden value field = %#v, want secret", valueItem.Field)
	}

	debugKVPress(w, node, debugKVDeleteButton)
	assertDebugKVStatus(t, state, node, "deleted", "")
	if _, found := manager.defaultStore.value([]string{"branch", "leaf"}, "entry"); found {
		t.Fatal("value still exists after delete")
	}
	debugKVPress(w, node, debugKVDeleteButton)
	assertDebugKVStatus(t, state, node, "not found", "")
}

func TestDebugKVPluginStoreAndInvalidBase64(t *testing.T) {
	t.Parallel()
	manager := newDebugKVMemoryManager()
	w, state, node := newDebugKVTestNode(t, manager)

	debugKVUpdateField(w, node, debugKVStoreField, debugKVStorePlugin)
	debugKVUpdateField(w, node, debugKVPluginField, "example")
	debugKVUpdateField(w, node, debugKVKeyField, "bytes")
	debugKVUpdateField(w, node, debugKVStringField, false)
	debugKVUpdateField(w, node, debugKVValueField, "not base64")
	debugKVPress(w, node, debugKVSetButton)
	assertDebugKVStatus(t, state, node, "set failed", "value is not valid base64")

	debugKVUpdateField(w, node, debugKVValueField, "AAEC")
	debugKVPress(w, node, debugKVSetButton)
	assertDebugKVStatus(t, state, node, "created", "")
	got, found := manager.plugin("example").value(nil, "bytes")
	if !found || !reflect.DeepEqual(got.Data, []byte{0, 1, 2}) || got.String {
		t.Fatalf("plugin value = %#v, found %v", got, found)
	}
}

func TestParseDebugKVPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		want []string
		err  bool
	}{
		{text: `[]`, want: []string{}},
		{text: `["one", "two/three"]`, want: []string{"one", "two/three"}},
		{text: ``, err: true},
		{text: `null`, err: true},
		{text: `[""]`, err: true},
		{text: `[1]`, err: true},
	} {
		got, err := parseDebugKVPath(tc.text)
		if (err != nil) != tc.err {
			t.Errorf("parseDebugKVPath(%q) error = %v, want error %v", tc.text, err, tc.err)
			continue
		}
		if !tc.err && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseDebugKVPath(%q) = %#v, want %#v", tc.text, got, tc.want)
		}
	}
}

func newDebugKVTestNode(t *testing.T, manager *debugKVMemoryManager) (*pasta.Workspace, *formular.MenuSnapshotState, uint64) {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(DebugKVClass{Manager: manager}); err != nil {
		t.Fatalf("AddNodeClass: %v", err)
	}
	state, sub := testkit.SubscribeMenuState(t, w)
	t.Cleanup(func() { w.UnsubscribeNotifications(sub) })
	node, err := w.AddNodeByClass(NodeTypeDebugKV, "kv")
	if err != nil {
		t.Fatalf("AddNodeByClass: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)
	return w, state, node
}

func debugKVUpdateField(w *pasta.Workspace, node uint64, field string, value any) {
	w.SendNodeFormularMsg(node, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, node),
		Field:       formular.FieldRef{BlockID: debugKVEntryBlockID, FieldID: field},
		Value:       value,
	})
}

func debugKVPress(w *pasta.Workspace, node uint64, button string) {
	w.SendNodeFormularMsg(node, formular.ButtonPressMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageButtonPress, node),
		BlockID:     debugKVEntryBlockID,
		ButtonID:    button,
	})
}

func assertDebugKVStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, status, errText string) {
	t.Helper()
	result := testkit.MustFindBlock(t, debugKVMenu(t, state, node), debugKVResultBlockID)
	if got := debugKVItem(t, result, "status").Text; got != "Status: "+status {
		t.Fatalf("status = %q, want %q", got, "Status: "+status)
	}
	if got := debugKVItem(t, result, "error").Value; got != errText {
		t.Fatalf("error = %#v, want %q", got, errText)
	}
}

func debugKVMenu(t *testing.T, state *formular.MenuSnapshotState, node uint64) formular.MenuSnapshotMessage {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	return snapshot
}

func debugKVItem(t *testing.T, block formular.Block, id string) formular.Item {
	t.Helper()
	item, ok := testkit.FindItem(block, id)
	if !ok {
		t.Fatalf("%s.%s item missing", block.ID, id)
	}
	return item
}

type debugKVMemoryManager struct {
	mu           sync.Mutex
	defaultStore *debugKVMemoryStore
	plugins      map[string]*debugKVMemoryStore
}

func newDebugKVMemoryManager() *debugKVMemoryManager {
	manager := &debugKVMemoryManager{plugins: make(map[string]*debugKVMemoryStore)}
	manager.defaultStore = &debugKVMemoryStore{manager: manager, root: newDebugKVMemoryRoot()}
	return manager
}

func (m *debugKVMemoryManager) DefaultStore() storage.KVStore { return m.defaultStore }

func (m *debugKVMemoryManager) PluginStore(name string) storage.KVStore { return m.plugin(name) }

func (m *debugKVMemoryManager) plugin(name string) *debugKVMemoryStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	store := m.plugins[name]
	if store == nil {
		store = &debugKVMemoryStore{manager: m, root: newDebugKVMemoryRoot()}
		m.plugins[name] = store
	}
	return store
}

type debugKVMemoryRoot struct {
	values map[string]map[string]storage.KValue
	exists map[string]bool
}

func newDebugKVMemoryRoot() *debugKVMemoryRoot {
	return &debugKVMemoryRoot{
		values: make(map[string]map[string]storage.KValue),
		exists: map[string]bool{"": true},
	}
}

type debugKVMemoryStore struct {
	manager *debugKVMemoryManager
	root    *debugKVMemoryRoot
	path    []string
	owner   string
}

func (s *debugKVMemoryStore) Subtree(path ...string) storage.KVStore {
	next := *s
	next.path = append(append([]string(nil), s.path...), path...)
	return &next
}

func (s *debugKVMemoryStore) OwnedBy(owner string) storage.KVStore {
	next := *s
	next.path = append([]string(nil), s.path...)
	next.owner = owner
	return &next
}

func (s *debugKVMemoryStore) View(ctx context.Context, fn func(storage.KVReadTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	return fn(debugKVMemoryReadTx{root: s.root, path: append([]string(nil), s.path...), owner: s.owner})
}

func (s *debugKVMemoryStore) Update(ctx context.Context, fn func(storage.KVWriteTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	return fn(debugKVMemoryWriteTx{debugKVMemoryReadTx{root: s.root, path: append([]string(nil), s.path...), owner: s.owner}})
}

func (s *debugKVMemoryStore) value(path []string, key string) (storage.KValue, bool) {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	value, found := s.root.values[debugKVMemoryPath(path)][key]
	return value.Clone(), found
}

func (s *debugKVMemoryStore) put(path []string, key string, value storage.KValue) {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	debugKVMemorySet(s.root, path, key, value)
}

type debugKVMemoryReadTx struct {
	root  *debugKVMemoryRoot
	path  []string
	owner string
}

func (tx debugKVMemoryReadTx) Exists() bool           { return tx.root.exists[debugKVMemoryPath(tx.path)] }
func (tx debugKVMemoryReadTx) Owner() (string, error) { return tx.owner, nil }

func (tx debugKVMemoryReadTx) Get(key string) (*storage.KValue, bool, error) {
	if key == "" {
		return nil, false, storage.ErrKVInvalidName
	}
	value, found := tx.root.values[debugKVMemoryPath(tx.path)][key]
	if !found {
		return nil, false, nil
	}
	clone := value.Clone()
	return &clone, true, nil
}

func (tx debugKVMemoryReadTx) ForEachValue(fn func(string, storage.KValue) error) error {
	keys := make([]string, 0, len(tx.root.values[debugKVMemoryPath(tx.path)]))
	for key := range tx.root.values[debugKVMemoryPath(tx.path)] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := fn(key, tx.root.values[debugKVMemoryPath(tx.path)][key].Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (tx debugKVMemoryReadTx) ForEachSubtree(fn func(string) error) error {
	prefix := debugKVMemoryPath(tx.path)
	if prefix != "" {
		prefix += "\x00"
	}
	names := make(map[string]struct{})
	for path, exists := range tx.root.exists {
		if !exists || !strings.HasPrefix(path, prefix) || path == prefix {
			continue
		}
		name := strings.Split(strings.TrimPrefix(path, prefix), "\x00")[0]
		names[name] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		if err := fn(name); err != nil {
			return err
		}
	}
	return nil
}

func (tx debugKVMemoryReadTx) Subtree(path ...string) storage.KVReadTx {
	tx.path = append(append([]string(nil), tx.path...), path...)
	return tx
}

type debugKVMemoryWriteTx struct{ debugKVMemoryReadTx }

func (tx debugKVMemoryWriteTx) Subtree(path ...string) storage.KVWriteTx {
	tx.path = append(append([]string(nil), tx.path...), path...)
	return tx
}

func (tx debugKVMemoryWriteTx) Ensure() error {
	debugKVMemoryEnsure(tx.root, tx.path)
	return nil
}

func (tx debugKVMemoryWriteTx) Set(key string, value storage.KValue) error {
	if key == "" {
		return storage.ErrKVInvalidName
	}
	value.Owner = tx.owner
	debugKVMemorySet(tx.root, tx.path, key, value)
	return nil
}

func (tx debugKVMemoryWriteTx) Delete(key string) error {
	if key == "" {
		return storage.ErrKVInvalidName
	}
	delete(tx.root.values[debugKVMemoryPath(tx.path)], key)
	return nil
}

func (tx debugKVMemoryWriteTx) Drop() error {
	if len(tx.path) == 0 {
		return storage.ErrKVRoot
	}
	prefix := debugKVMemoryPath(tx.path)
	for path := range tx.root.exists {
		if path == prefix || strings.HasPrefix(path, prefix+"\x00") {
			delete(tx.root.exists, path)
			delete(tx.root.values, path)
		}
	}
	return nil
}

func debugKVMemorySet(root *debugKVMemoryRoot, path []string, key string, value storage.KValue) {
	debugKVMemoryEnsure(root, path)
	pathKey := debugKVMemoryPath(path)
	if root.values[pathKey] == nil {
		root.values[pathKey] = make(map[string]storage.KValue)
	}
	root.values[pathKey][key] = value.Clone()
}

func debugKVMemoryEnsure(root *debugKVMemoryRoot, path []string) {
	for i := 0; i <= len(path); i++ {
		root.exists[debugKVMemoryPath(path[:i])] = true
	}
}

func debugKVMemoryPath(path []string) string { return strings.Join(path, "\x00") }

var _ storage.KVManager = (*debugKVMemoryManager)(nil)
var _ storage.KVStore = (*debugKVMemoryStore)(nil)
var _ storage.KVReadTx = debugKVMemoryReadTx{}
var _ storage.KVWriteTx = debugKVMemoryWriteTx{}
