package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestSecretsStorageImmutableOverridesMutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("shared", "mutable"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("shared", []string{"mutable-node"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManagerWithOptions(path, bboltKVManagerOptions{
		ImmutableSecrets: map[string]immutableSecret{
			"shared": {
				secretRecord: secretRecord{
					Value: "protected",
					Nodes: []string{"protected-node"},
				},
				source: api.SecretSourceProtected,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	sec, err := manager.GetSecret("shared", "protected-node")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Value != "protected" || !sec.ReadOnly {
		t.Fatalf("GetSecret immutable = %#v, want protected read-only value", sec)
	}
	if _, err := manager.GetSecret("shared", "mutable-node"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("GetSecret shadowed mutable node error = %v, want ErrSecretAccessDenied", err)
	}
	if err := manager.DeleteGeneratedSecret("shared", "protected-node"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("immutable generated-secret deletion error = %v, want ErrSecretAccessDenied", err)
	}

	snapshot, err := manager.secretsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Secrets) != 1 || snapshot.Secrets[0].Value != "protected" || !snapshot.Secrets[0].ReadOnly {
		t.Fatalf("snapshot = %#v, want only effective immutable secret", snapshot)
	}

	key, err := manager.NewSecret(storage.Secret{Value: "generated", ReadOnly: true}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	sec, err = manager.GetSecret(key, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Value != "generated" || sec.ReadOnly {
		t.Fatalf("generated secret = %#v, want mutable value", sec)
	}
}

func TestDeleteGeneratedSecretRequiresExclusiveNodeOwnership(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	generated, err := manager.NewSecret(storage.Secret{Value: "private"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.DeleteGeneratedSecret(generated, "other"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("wrong-owner deletion error = %v, want ErrSecretAccessDenied", err)
	}
	if _, err = manager.GetSecret(generated, "owner"); err != nil {
		t.Fatalf("wrong-owner deletion removed secret: %v", err)
	}

	if _, err = manager.setSecretNodes(generated, []string{"owner", "other"}); err != nil {
		t.Fatal(err)
	}
	if err = manager.DeleteGeneratedSecret(generated, "owner"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("shared-secret deletion error = %v, want ErrSecretAccessDenied", err)
	}
	if _, err = manager.GetSecret(generated, "owner"); err != nil {
		t.Fatalf("shared-secret deletion removed secret: %v", err)
	}

	if _, err = manager.setSecretNodes(generated, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	if err = manager.DeleteGeneratedSecret(generated, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.GetSecret(generated, "owner"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("deleted secret access error = %v, want ErrSecretAccessDenied", err)
	}
	if err = manager.DeleteGeneratedSecret(generated, "owner"); err != nil {
		t.Fatalf("repeated deletion error = %v, want nil", err)
	}
}

func TestGeneratedSecretRecoverySurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}

	key, err := manager.NewGeneratedSecret(storage.Secret{Value: "first"}, "tor-node", "xtor/service/test")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := manager.NewGeneratedSecret(storage.Secret{Value: "second"}, "tor-node", "xtor/service/test")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate != key {
		t.Fatalf("duplicate generated key = %q, want %q", duplicate, key)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	recoveredKey, recovered, err := manager.RecoverGeneratedSecret("xtor/service/test", "tor-node")
	if err != nil {
		t.Fatal(err)
	}
	if recoveredKey != key || recovered.Value != "first" || recovered.ReadOnly {
		t.Fatalf("recovered generated secret = %q %#v, want %q first mutable", recoveredKey, recovered, key)
	}

	if err := manager.CommitGeneratedSecret(key, "tor-node"); err != nil {
		t.Fatal(err)
	}
	recoveredKey, _, err = manager.RecoverGeneratedSecret("xtor/service/test", "tor-node")
	if err != nil {
		t.Fatal(err)
	}
	if recoveredKey != "" {
		t.Fatalf("recovered committed key = %q, want empty", recoveredKey)
	}
	secret, err := manager.GetSecret(key, "tor-node")
	if err != nil || secret.Value != "first" {
		t.Fatalf("committed secret = %#v, %v", secret, err)
	}
}

func TestGeneratedSecretRecoveryIsScopedAndValidated(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	first, err := manager.NewGeneratedSecret(storage.Secret{Value: "one"}, "node-one", "shared-owner")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.NewGeneratedSecret(storage.Secret{Value: "two"}, "node-two", "shared-owner")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different nodes shared generated key %q", first)
	}
	if err := manager.CommitGeneratedSecret(first, "node-two"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("wrong-node commit error = %v, want ErrSecretAccessDenied", err)
	}
	if err := manager.DeleteGeneratedSecret(first, "node-one"); err != nil {
		t.Fatal(err)
	}
	key, _, err := manager.RecoverGeneratedSecret("shared-owner", "node-one")
	if err != nil || key != "" {
		t.Fatalf("recovery after deletion = %q, %v", key, err)
	}

	if _, err := manager.NewGeneratedSecret(storage.Secret{Value: "bad"}, "node-one", ""); !errors.Is(err, storage.ErrSecretInvalidName) {
		t.Fatalf("empty owner error = %v, want ErrSecretInvalidName", err)
	}
	if _, err := encodeSecretRecord(secretRecord{Value: "bad", Nodes: []string{"node-one"}, GeneratedOwner: strings.Repeat("x", 257)}); !errors.Is(err, storage.ErrSecretInvalidName) {
		t.Fatalf("invalid encoded owner error = %v, want ErrSecretInvalidName", err)
	}
}

func TestSecretsAdminEventsValidationAndInbox(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	inbox := make(chan storage.SecretValueChanged, 8)
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		[]pasta.NodeClass{secretInboxNodeClass{inbox: inbox}},
		nil,
		false)

	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck
	if _, err := engine.AddNode("main", "test/SecretInbox", "allowed"); err != nil {
		t.Fatal(err)
	}

	var events []api.Message
	session := (&AdminAPI{secrets: manager}).newAPISession(engine, nil, func(msg api.Message) error {
		if msg.Kind == api.MessageEvent && msg.Surface == api.SurfaceSecrets {
			events = append(events, msg)
		}
		return nil
	})
	defer session.close()

	subReq := mustRequest(t, "1", api.SurfaceSecrets, string(api.MethodSecretsSubscribe), nil)
	resp, err := session.handleMessage(subReq)
	if err != nil {
		t.Fatal(err)
	}
	subResp, err := api.DecodePayload[api.SecretsSubscribeResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if subResp.SubscriptionID == 0 || len(subResp.Snapshot.Secrets) != 0 {
		t.Fatalf("subscribe response = %#v, want live empty snapshot", subResp)
	}

	missingNodeReq := mustRequest(t, "2", api.SurfaceSecrets, string(api.MethodSecretsSetNodes), api.SecretsSetNodesRequest{
		Key:   "token",
		Nodes: []string{"missing"},
	})
	resp, err = session.handleMessage(missingNodeReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != api.ErrorNotFound {
		t.Fatalf("missing node response = %#v, want not_found error", resp)
	}

	events = nil
	setValueReq := mustRequest(t, "3", api.SurfaceSecrets, string(api.MethodSecretsSetValue), api.SecretsSetValueRequest{
		Key:   "token",
		Value: "old",
	})
	if resp, err = session.handleMessage(setValueReq); err != nil || resp.Error != nil {
		t.Fatalf("set value response = %#v, err %v", resp, err)
	}
	assertSecretEvent(t, events, subResp.SubscriptionID, "token", false, "old")
	assertNoSecretInbox(t, inbox)

	events = nil
	setNodesReq := mustRequest(t, "4", api.SurfaceSecrets, string(api.MethodSecretsSetNodes), api.SecretsSetNodesRequest{
		Key:   "token",
		Nodes: []string{"allowed"},
	})
	if resp, err = session.handleMessage(setNodesReq); err != nil || resp.Error != nil {
		t.Fatalf("set nodes response = %#v, err %v", resp, err)
	}
	assertSecretEvent(t, events, subResp.SubscriptionID, "token", false, "old")
	assertSecretInbox(t, inbox, "token", "token")

	events = nil
	setValueReq = mustRequest(t, "5", api.SurfaceSecrets, string(api.MethodSecretsSetValue), api.SecretsSetValueRequest{
		Key:   "token",
		Value: "new",
	})
	if resp, err = session.handleMessage(setValueReq); err != nil || resp.Error != nil {
		t.Fatalf("set value response = %#v, err %v", resp, err)
	}
	assertSecretEvent(t, events, subResp.SubscriptionID, "token", false, "new")
	assertSecretInbox(t, inbox, "token", "token")

	events = nil
	renameReq := mustRequest(t, "6", api.SurfaceSecrets, string(api.MethodSecretsRename), api.SecretsRenameRequest{
		Key:    "token",
		NewKey: "renamed",
	})
	if resp, err = session.handleMessage(renameReq); err != nil || resp.Error != nil {
		t.Fatalf("rename response = %#v, err %v", resp, err)
	}
	if len(events) != 2 {
		t.Fatalf("rename events = %d, want delete and upsert", len(events))
	}
	assertSecretEvent(t, events[:1], subResp.SubscriptionID, "token", true, "")
	assertSecretEvent(t, events[1:], subResp.SubscriptionID, "renamed", false, "new")
	assertSecretInbox(t, inbox, "token", "renamed")

	events = nil
	deleteReq := mustRequest(t, "7", api.SurfaceSecrets, string(api.MethodSecretsDelete), api.SecretKeyRequest{Key: "renamed"})
	if resp, err = session.handleMessage(deleteReq); err != nil || resp.Error != nil {
		t.Fatalf("delete response = %#v, err %v", resp, err)
	}
	assertSecretEvent(t, events, subResp.SubscriptionID, "renamed", true, "")
	assertSecretInbox(t, inbox, "renamed", "renamed")
}

func TestSecretNodeNameSyncUpdatesMutableAndImmutableAllowLists(t *testing.T) {
	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{
		ImmutableSecrets: map[string]immutableSecret{
			"immutable": {
				secretRecord: secretRecord{
					Value: "protected",
					Nodes: []string{"alpha"},
				},
				source: api.SecretSourceProtected,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	if _, err := manager.setSecretValue("mutable", "value"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("mutable", []string{"alpha"}); err != nil {
		t.Fatal(err)
	}

	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		[]pasta.NodeClass{secretInboxNodeClass{}},
		nil,
		false)

	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck
	nodeID, err := engine.AddNode("main", "test/SecretInbox", "alpha")
	if err != nil {
		t.Fatal(err)
	}

	admin := &AdminAPI{secrets: manager}
	subID := admin.startSecretNodeNameSync(engine, nil)
	if subID == 0 {
		t.Fatal("secret node sync subscription failed")
	}
	defer engine.UnsubscribeNotifications(subID)

	engine.SetNodeName("main", nodeID, "beta")
	assertSecretAccess(t, manager, "mutable", "beta", "value")
	assertSecretAccess(t, manager, "immutable", "beta", "protected")
	assertSecretDenied(t, manager, "mutable", "alpha")
	assertSecretDenied(t, manager, "immutable", "alpha")

	engine.RemoveNode("main", nodeID)
	assertSecretDenied(t, manager, "mutable", "beta")
	assertSecretDenied(t, manager, "immutable", "beta")
}

func TestSecretNodeNameSyncInitialSnapshotRemovesStaleAllowListNames(t *testing.T) {
	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{
		ImmutableSecrets: map[string]immutableSecret{
			"immutable": {
				secretRecord: secretRecord{
					Value: "protected",
					Nodes: []string{"active", "stale"},
				},
				source: api.SecretSourceProtected,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	if _, err := manager.setSecretValue("mutable", "value"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("mutable", []string{"active", "stale"}); err != nil {
		t.Fatal(err)
	}

	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		[]pasta.NodeClass{secretInboxNodeClass{}},
		nil,
		false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.AddNode("main", "test/SecretInbox", "active"); err != nil {
		t.Fatal(err)
	}

	admin := &AdminAPI{secrets: manager}
	subID := admin.startSecretNodeNameSync(engine, nil)
	if subID == 0 {
		t.Fatal("secret node sync subscription failed")
	}

	assertSecretAccess(t, manager, "mutable", "active", "value")
	assertSecretAccess(t, manager, "immutable", "active", "protected")
	assertSecretDenied(t, manager, "mutable", "stale")
	assertSecretDenied(t, manager, "immutable", "stale")

	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	assertSecretAccess(t, manager, "mutable", "active", "value")
}

func TestNodeNameSyncRenamesAndDeletesOwnedKVEntries(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,
		[]pasta.NodeClass{secretInboxNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck
	nodeID, err := engine.AddNode(almagest.MainWorkspace, "test/SecretInbox", "alpha")
	if err != nil {
		t.Fatal(err)
	}

	admin := &AdminAPI{kv: manager}
	subID := admin.startSecretNodeNameSync(engine, nil)
	if subID == 0 {
		t.Fatal("node name sync subscription failed")
	}
	defer engine.UnsubscribeNotifications(subID)
	owned := manager.DefaultStore().Subtree("node-state").OwnedBy("alpha")
	if err := owned.Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set("value", storage.KValue{Data: []byte("state")})
	}); err != nil {
		t.Fatal(err)
	}

	engine.SetNodeName(almagest.MainWorkspace, nodeID, "beta")
	if err := owned.View(context.Background(), func(tx storage.KVReadTx) error {
		owner, err := tx.Owner()
		if err != nil || owner != "beta" {
			t.Fatalf("KV owner after rename = %q, err=%v; want beta", owner, err)
		}
		value, found, err := tx.Get("value")
		if err != nil || !found || value.Owner != "beta" {
			t.Fatalf("KV value after rename = %#v, found=%t, err=%v", value, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	engine.RemoveNode(almagest.MainWorkspace, nodeID)
	if err := manager.DefaultStore().View(context.Background(), func(tx storage.KVReadTx) error {
		if tx.Subtree("node-state").Exists() {
			t.Fatal("owned KV subtree exists after node deletion")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsMutableErrorAndLifecycleBranches(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}

	invalidUTF8 := string([]byte{0xff})
	if _, err := manager.GetSecret("", "node"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("invalid get error = %v", err)
	}
	if _, err := manager.NewSecret(storage.Secret{}, ""); !errors.Is(err, storage.ErrSecretInvalidName) {
		t.Fatalf("invalid new node error = %v", err)
	}
	if _, err := manager.NewSecret(storage.Secret{Value: invalidUTF8}, "node"); !errors.Is(err, storage.ErrSecretInvalidValue) {
		t.Fatalf("invalid new value error = %v", err)
	}
	if _, err := manager.setSecretValue("", "value"); !errors.Is(err, storage.ErrSecretInvalidName) {
		t.Fatalf("invalid set key error = %v", err)
	}
	if _, err := manager.setSecretValue("one", invalidUTF8); !errors.Is(err, storage.ErrSecretInvalidValue) {
		t.Fatalf("invalid set value error = %v", err)
	}
	if _, err := manager.setSecretNodes("one", []string{""}); !errors.Is(err, storage.ErrSecretInvalidName) {
		t.Fatalf("invalid nodes error = %v", err)
	}
	if _, err := manager.renameSecret("missing", "missing"); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("same missing rename error = %v", err)
	}
	if _, err := manager.renameSecret("missing", "new"); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("missing rename error = %v", err)
	}
	if _, err := manager.deleteSecret(""); !errors.Is(err, storage.ErrSecretInvalidName) {
		t.Fatalf("invalid delete error = %v", err)
	}
	if _, err := manager.deleteSecret("missing"); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.setSecretValue("one", "value"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("one", []string{"node-b", "node-a", "node-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("two", "occupied"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.renameSecret("one", "two"); !errors.Is(err, storage.ErrSecretConflict) {
		t.Fatalf("rename conflict error = %v", err)
	}
	result, err := manager.renameSecret("one", "renamed")
	if err != nil || len(result.notifyNodes) != 2 {
		t.Fatalf("rename result = %#v, err=%v", result, err)
	}
	if _, err := manager.renameSecret("renamed", "renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.deleteSecret("renamed"); err != nil {
		t.Fatal(err)
	}

	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.secretsSnapshot(); !errors.Is(err, storage.ErrSecretClosed) {
		t.Fatalf("closed snapshot error = %v", err)
	}
	if _, err := manager.setSecretValue("closed", "value"); !errors.Is(err, storage.ErrSecretClosed) {
		t.Fatalf("closed set error = %v", err)
	}
	if _, err := manager.deleteSecret("closed"); !errors.Is(err, storage.ErrSecretClosed) {
		t.Fatalf("closed delete error = %v", err)
	}
}

type secretInboxNodeClass struct {
	inbox chan storage.SecretValueChanged
}

func (c secretInboxNodeClass) ClassName() string { return "test/SecretInbox" }
func (c secretInboxNodeClass) ShortDescription() string {
	return "Secret inbox test node"
}
func (c secretInboxNodeClass) LongDescription() string {
	return "Receives SecretValueChanged inbox messages."
}
func (c secretInboxNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}
func (c secretInboxNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &secretInboxNode{inbox: c.inbox}, nil
}

type secretInboxNode struct {
	pasta.BasicNode
	inbox chan storage.SecretValueChanged
}

func (n *secretInboxNode) OnInbox(message pasta.InboxMessage) error {
	if n.inbox == nil {
		return nil
	}
	if payload, ok := message.Payload.(storage.SecretValueChanged); ok {
		n.inbox <- payload
	}
	return nil
}

func assertSecretEvent(t *testing.T, events []api.Message, subscriptionID uint64, key string, deleted bool, value string) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("secret events = %d, want 1", len(events))
	}
	if events[0].Method != string(api.EventSecretsUpdate) {
		t.Fatalf("secret event method = %q, want update", events[0].Method)
	}
	var event api.SecretsEvent
	if err := json.Unmarshal(events[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.SubscriptionID != subscriptionID || event.Change.Key != key || event.Change.Deleted != deleted {
		t.Fatalf("secret event = %#v, want key %q deleted %v", event, key, deleted)
	}
	if deleted {
		if event.Change.Entry != nil {
			t.Fatalf("deleted event entry = %#v, want nil", event.Change.Entry)
		}
		return
	}
	if event.Change.Entry == nil || event.Change.Entry.Value != value {
		t.Fatalf("secret event entry = %#v, want value %q", event.Change.Entry, value)
	}
}

func assertSecretInbox(t *testing.T, inbox <-chan storage.SecretValueChanged, prev, next string) {
	t.Helper()
	select {
	case got := <-inbox:
		if got.SecretNamePrev != prev || got.SecretNameNew != next {
			t.Fatalf("secret inbox = %#v, want %q -> %q", got, prev, next)
		}
	default:
		t.Fatalf("missing secret inbox %q -> %q", prev, next)
	}
}

func assertNoSecretInbox(t *testing.T, inbox <-chan storage.SecretValueChanged) {
	t.Helper()
	select {
	case got := <-inbox:
		t.Fatalf("unexpected secret inbox %#v", got)
	default:
	}
}

func assertSecretAccess(t *testing.T, manager *bboltKVManager, key, node, value string) {
	t.Helper()
	sec, err := manager.GetSecret(key, node)
	if err != nil {
		t.Fatalf("GetSecret(%q, %q): %v", key, node, err)
	}
	if sec.Value != value {
		t.Fatalf("GetSecret(%q, %q).Value = %q, want %q", key, node, sec.Value, value)
	}
}

func assertSecretDenied(t *testing.T, manager *bboltKVManager, key, node string) {
	t.Helper()
	if _, err := manager.GetSecret(key, node); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("GetSecret(%q, %q) error = %v, want ErrSecretAccessDenied", key, node, err)
	}
}
