package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tlspolicy"
	"github.com/asciimoth/pasta/pasta"
	"go.etcd.io/bbolt"
)

type snapshotTestWriter struct {
	configPath    string
	protectedPath string
	writes        [][]byte
	err           error
}

type repeatingZeroReader struct{}

type snapshotErrorWriter struct{}

func (snapshotErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (repeatingZeroReader) Read(data []byte) (int, error) {
	clear(data)
	return len(data), nil
}

func TestSnapshotServiceUnavailableAndInputErrors(t *testing.T) {
	var service *snapshotService
	ctx := context.Background()
	if _, err := service.Create(ctx, "name"); err == nil {
		t.Fatal("nil Create succeeded")
	}
	if _, err := service.List(); err == nil {
		t.Fatal("nil List succeeded")
	}
	if err := service.Delete("id"); err == nil {
		t.Fatal("nil Delete succeeded")
	}
	if _, err := service.Export("id"); err == nil {
		t.Fatal("nil Export succeeded")
	}
	if _, err := service.Import(ctx, strings.NewReader("archive"), nil); err == nil {
		t.Fatal("nil Import succeeded")
	}
	if _, err := service.Rename("id", "name"); err == nil {
		t.Fatal("nil Rename succeeded")
	}
	if _, err := service.Restore(ctx, "id"); err == nil {
		t.Fatal("nil Restore succeeded")
	}

	empty := &snapshotService{}
	if _, err := empty.List(); err == nil {
		t.Fatal("empty List succeeded")
	}
	if err := empty.Delete("11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("empty Delete succeeded")
	}
	if _, err := empty.Export("11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("empty Export succeeded")
	}
	if _, err := empty.Import(ctx, nil, nil); err == nil {
		t.Fatal("empty Import succeeded")
	}
	if _, err := empty.Rename("11111111-1111-4111-8111-111111111111", "name"); err == nil {
		t.Fatal("empty Rename succeeded")
	}
	if _, err := empty.Restore(ctx, "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("empty Restore succeeded")
	}

	if _, _, err := restoreStartupSnapshot(ctx, "", "-", strings.NewReader("archive"), nil); err == nil {
		t.Fatal("startup restore without storage succeeded")
	}
	if _, _, err := restoreStartupSnapshot(ctx, t.TempDir(), "-", nil, nil); err == nil {
		t.Fatal("startup restore without stdin succeeded")
	}
	if _, _, err := restoreStartupSnapshot(ctx, t.TempDir(), filepath.Join(t.TempDir(), "missing.zip"), nil, nil); err == nil {
		t.Fatal("startup restore of missing archive succeeded")
	}
}

func TestSnapshotPathWithinStorageEdges(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "nested", "file")
	if got, ok := pathWithinStorage(root, inside); !ok || got != filepath.Join("nested", "file") {
		t.Fatalf("inside path = %q, %t", got, ok)
	}
	for _, path := range []string{"", root, filepath.Dir(root)} {
		if got, ok := pathWithinStorage(root, path); ok || got != "" {
			t.Fatalf("outside path %q = %q, %t", path, got, ok)
		}
	}
	if got, ok := pathWithinStorage("", inside); ok || got != "" {
		t.Fatalf("empty root = %q, %t", got, ok)
	}
}

func (w *snapshotTestWriter) WriteConfig(data []byte) error {
	w.writes = append(w.writes, bytes.Clone(data))
	if w.err != nil {
		return w.err
	}
	return os.WriteFile(w.configPath, data, 0o600)
}

func (w *snapshotTestWriter) ConfigFilePath() string {
	return w.configPath
}

func (w *snapshotTestWriter) ProtectedConfigFilePath() string {
	return w.protectedPath
}

func TestSnapshotCreateListAndRestoreCompleteDaemonState(t *testing.T) {
	service, manager, engine, writer, storagePath := newSnapshotTestService(t, false)
	ctx := context.Background()

	initialID, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node")
	if !ok {
		t.Fatal("initial saved-node is missing")
	}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("saved-kv", storage.KValue{Data: []byte("before"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("saved-secret", "before-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("saved-secret", []string{"saved-node"}); err != nil {
		t.Fatal(err)
	}
	dnsKey, err := manager.setDNSRecord("saved-cache", "", api.DNSRecordInput{
		Name: "snapshot.test.", Type: "A", Class: "IN", TTL: 3600, Value: "192.0.2.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(storagePath, "future-store", "nested.txt")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("before-file"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storagePath, snapshotDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(storagePath, snapshotDirectoryName, "must-survive")
	if err := os.WriteFile(markerPath, []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 9, 9, 12, 30, 0, 123456789, time.UTC)
	service.now = func() time.Time { return createdAt }
	first, err := service.Create(ctx, "duplicate name")
	if err != nil {
		t.Fatal(err)
	}
	if first.Timestamp != createdAt.UnixNano() || first.Name != "duplicate name" {
		t.Fatalf("created snapshot = %#v", first)
	}
	assertSnapshotArchiveIsSanitized(t, storagePath, first.UUID)

	service.now = func() time.Time { return createdAt.Add(time.Second) }
	second, err := service.Create(ctx, "duplicate name")
	if err != nil {
		t.Fatal(err)
	}
	if second.UUID == first.UUID {
		t.Fatal("snapshot UUIDs are not unique")
	}
	listed, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].UUID != second.UUID || listed[1].UUID != first.UUID {
		t.Fatalf("listed snapshots = %#v, want newest first", listed)
	}
	renamed, err := service.Rename(first.UUID, "renamed snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.UUID != first.UUID || renamed.Timestamp != first.Timestamp || renamed.Name != "renamed snapshot" {
		t.Fatalf("renamed snapshot = %#v", renamed)
	}
	listed, err = service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[1] != renamed {
		t.Fatalf("listed renamed snapshots = %#v", listed)
	}

	if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("saved-kv", storage.KValue{Data: []byte("after"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("saved-secret", "after-secret"); err != nil {
		t.Fatal(err)
	}
	if err := manager.dropDNSCache("saved-cache"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("after-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	unwantedPath := filepath.Join(storagePath, "not-in-snapshot.txt")
	if err := os.WriteFile(unwantedPath, []byte("remove me"), 0o600); err != nil {
		t.Fatal(err)
	}
	protectedBefore := []byte("protected-current-secret")
	if err := os.WriteFile(writer.protectedPath, protectedBefore, 0o600); err != nil {
		t.Fatal(err)
	}

	response, err := service.Restore(ctx, first.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if !response.ConfigWritten || response.ConfigWriteError != "" || response.Snapshot != renamed {
		t.Fatalf("restore response = %#v", response)
	}
	restoredID, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node")
	if !ok || restoredID == initialID {
		t.Fatalf("restored node ID = %d, found=%t; want a fully rebuilt node", restoredID, ok)
	}
	assertKVString(t, manager, "saved-kv", "before")
	secret, err := manager.GetSecret("saved-secret", "saved-node")
	if err != nil || secret.Value != "before-secret" {
		t.Fatalf("restored secret = %#v, err=%v", secret, err)
	}
	dns, err := manager.dnsKeysSnapshot("saved-cache")
	if err != nil || !dns.Exists || len(dns.Keys) != 1 || dns.Keys[0].Key != dnsKey {
		t.Fatalf("restored DNS cache = %#v, err=%v", dns, err)
	}
	if data, err := os.ReadFile(dataPath); err != nil || string(data) != "before-file" {
		t.Fatalf("restored future file = %q, err=%v", data, err)
	}
	if _, err := os.Stat(unwantedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file absent from snapshot still exists: %v", err)
	}
	if data, err := os.ReadFile(markerPath); err != nil || string(data) != "marker" {
		t.Fatalf("excluded snapshots content = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(writer.protectedPath); err != nil || !bytes.Equal(data, protectedBefore) {
		t.Fatalf("protected config changed: %q, err=%v", data, err)
	}
	if len(writer.writes) < 2 {
		t.Fatalf("config writes = %d, want mutation and restore writes", len(writer.writes))
	}
	lastWrite := writer.writes[len(writer.writes)-1]
	if bytes.Contains(lastWrite, []byte("Protected")) || bytes.Contains(lastWrite, []byte("embedded-secret")) {
		t.Fatalf("restored config write contains protected data: %s", lastWrite)
	}

	if err := service.Delete(second.UUID); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].UUID != first.UUID {
		t.Fatalf("snapshots after delete = %#v, want only first snapshot", listed)
	}
	if _, err := os.Stat(filepath.Join(storagePath, snapshotDirectoryName, second.UUID+".zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted snapshot archive still exists: %v", err)
	}
}

func TestSnapshotRestorePublishesOneAuthoritativeEngineState(t *testing.T) {
	tests := []struct {
		name       string
		failApply  bool
		finalNode  string
		restoreErr bool
	}{
		{name: "restored snapshot", finalNode: "saved-node"},
		{name: "rolled back state", failApply: true, finalNode: "live-node", restoreErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, engine, _, _ := newSnapshotTestService(t, false)
			ctx := context.Background()
			snapshot, err := service.Create(ctx, "saved graph")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.autosaver.Update(`{
				"nodes": {"live-node": {"Class": "test/AdminAPIBatchNode", "Pos": "live"}}
			}`, ""); err != nil {
				t.Fatal(err)
			}
			if test.failApply {
				var fetchMu sync.Mutex
				fetchCount := 0
				service.tlsFetcher = tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) {
					fetchMu.Lock()
					defer fetchMu.Unlock()
					fetchCount++
					if fetchCount == 1 {
						return nil, errors.New("injected restore failure")
					}
					return nil, nil
				})
			}

			var notificationMu sync.Mutex
			var notifications []almagest.EngineNotification
			published := make(chan struct{})
			var publishedOnce sync.Once
			awaitPublication := false
			subscription := engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
				if notification.Kind != almagest.NotificationWorkspace && notification.Kind != almagest.NotificationWorker {
					return
				}
				notificationMu.Lock()
				notifications = append(notifications, notification)
				if awaitPublication && notification.Worker != nil &&
					notification.Worker.Kind == almagest.WorkerNotificationSnapshot {
					publishedOnce.Do(func() { close(published) })
				}
				notificationMu.Unlock()
			})
			if subscription == 0 {
				t.Fatal("engine notification subscription failed")
			}
			t.Cleanup(func() { engine.UnsubscribeNotifications(subscription) })
			notificationMu.Lock()
			notifications = nil
			awaitPublication = true
			notificationMu.Unlock()

			_, restoreErr := service.Restore(ctx, snapshot.UUID)
			if (restoreErr != nil) != test.restoreErr {
				t.Fatalf("Restore error = %v, want error=%t", restoreErr, test.restoreErr)
			}
			select {
			case <-published:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for final restore publication")
			}

			notificationMu.Lock()
			got := append([]almagest.EngineNotification(nil), notifications...)
			notificationMu.Unlock()
			if len(got) != 2 {
				t.Fatalf("restore notifications = %#v, want one workspace and one worker snapshot", got)
			}
			if got[0].Workspace == nil || got[0].Workspace.Kind != pasta.NotificationWorkspaceSnapshot ||
				got[0].Workspace.Snapshot == nil {
				t.Fatalf("workspace restore publication = %#v, want full snapshot", got[0])
			}
			if len(got[0].Workspace.Snapshot.Nodes) != 1 {
				t.Fatalf("published nodes = %#v, want one final node", got[0].Workspace.Snapshot.Nodes)
			}
			for _, node := range got[0].Workspace.Snapshot.Nodes {
				if node.Name != test.finalNode {
					t.Fatalf("published final node = %q, want %q", node.Name, test.finalNode)
				}
			}
			if got[1].Worker == nil || got[1].Worker.Kind != almagest.WorkerNotificationSnapshot {
				t.Fatalf("worker restore publication = %#v, want full snapshot", got[1])
			}
		})
	}
}

func TestSnapshotRestoreIgnoresDelayedNodeLifecycleNotifications(t *testing.T) {
	tests := []struct {
		name  string
		queue func(almagest.Engine, uint64, uint64)
	}{
		{name: "node removal from restore"},
		{
			name: "node rename before restore",
			queue: func(engine almagest.Engine, nodeID, _ uint64) {
				engine.SetNodeName(almagest.MainWorkspace, nodeID, "temporary-name")
			},
		},
		{
			name: "batch removal before restore",
			queue: func(engine almagest.Engine, nodeID, _ uint64) {
				engine.RemoveNodes(almagest.MainWorkspace, []uint64{nodeID})
			},
		},
		{
			name: "renamed batch update before restore",
			queue: func(engine almagest.Engine, nodeID, _ uint64) {
				engine.SetNodeName(almagest.MainWorkspace, nodeID, "temporary-name")
				engine.MoveNodes(almagest.MainWorkspace, map[uint64]string{nodeID: "20 30"})
			},
		},
		{
			name: "renamed full snapshot before restore",
			queue: func(engine almagest.Engine, nodeID, syncID uint64) {
				engine.SetNodeName(almagest.MainWorkspace, nodeID, "temporary-name")
				engine.RequestFullSnapshot(syncID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testSnapshotRestoreWithDelayedNodeNotifications(t, test.queue)
		})
	}
}

func testSnapshotRestoreWithDelayedNodeNotifications(
	t *testing.T,
	queue func(almagest.Engine, uint64, uint64),
) {
	t.Helper()
	service, manager, engine, _, _ := newSnapshotTestService(t, false)
	ctx := context.Background()
	const storageID = "0000000000000000000000000000000000000000000000000000000000000000"

	if _, err := service.autosaver.Update(`{
		"nodes": {
			"saved-node": {
				"Class": "almagest/TailscaleCtrl",
				"enabled": false,
				"storageID": "`+storageID+`"
			}
		}
	}`, ""); err != nil {
		t.Fatal(err)
	}
	nodeID, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node")
	if !ok {
		t.Fatal("TailscaleCtrl node is missing")
	}

	admin := &AdminAPI{kv: manager, secrets: manager}
	syncID := admin.startSecretNodeNameSync(engine, nil)
	if syncID == 0 {
		t.Fatal("node name sync subscription failed")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(syncID) })

	storePath := []string{"nodes", "tailscale", storageID}
	store := manager.DefaultStore().Subtree(storePath...).OwnedBy("saved-node")
	if err := store.Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("client_cache", storage.KValue{Data: []byte("saved cache"), Hidden: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretValue("saved-secret", "saved value"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setSecretNodes("saved-secret", []string{"saved-node"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Create(ctx, "owned state")
	if err != nil {
		t.Fatal(err)
	}

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	blockingID := engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil ||
			notification.Workspace.Kind != pasta.NotificationNodeUpdated || notification.Workspace.ID != nodeID {
			return
		}
		select {
		case <-callbackEntered:
			return
		default:
		}
		close(callbackEntered)
		<-releaseCallback
	})
	if blockingID == 0 {
		t.Fatal("blocking notification subscription failed")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(blockingID) })

	mutationDone := make(chan struct{})
	go func() {
		engine.SetNodePosition(almagest.MainWorkspace, nodeID, "10 20")
		close(mutationDone)
	}()
	<-callbackEntered
	if queue != nil {
		queue(engine, nodeID, syncID)
	}

	if _, err := service.Restore(ctx, snapshot.UUID); err != nil {
		close(releaseCallback)
		<-mutationDone
		t.Fatal(err)
	}

	var restoredOwner *bboltKVOwner
	manager.kvOwnerMu.RLock()
	for owner := range manager.kvOwnerRefs {
		if owner.name == "saved-node" && owner.active {
			restoredOwner = owner
			break
		}
	}
	manager.kvOwnerMu.RUnlock()
	if restoredOwner == nil {
		close(releaseCallback)
		<-mutationDone
		t.Fatal("restored TailscaleCtrl owned store handle is missing")
	}
	restoredStore := &bboltKVStore{
		manager:        manager,
		path:           append([]string{kvDataBucketName}, storePath...),
		protectedDepth: 1,
		owner:          restoredOwner,
		ownershipDepth: 1 + len(storePath),
	}

	close(releaseCallback)
	<-mutationDone

	if err := restoredStore.Update(ctx, func(tx storage.KVWriteTx) error {
		value, found, err := tx.Get("client_cache")
		if err != nil {
			return err
		}
		if !found || string(value.Data) != "saved cache" {
			t.Fatalf("restored Tailscale cache = %#v, found=%t; want saved cache", value, found)
		}
		return tx.Set("client_cache", storage.KValue{Data: []byte("updated cache"), Hidden: true})
	}); err != nil {
		t.Fatalf("write through restored Tailscale store: %v", err)
	}
	secret, err := manager.GetSecret("saved-secret", "saved-node")
	if err != nil || secret.Value != "saved value" {
		t.Fatalf("restored node secret = %#v, err=%v; want saved value", secret, err)
	}
}

func TestSnapshotRestoreKeepsConfigInMemoryWhenWriteFailsOrIsReadOnly(t *testing.T) {
	for _, test := range []struct {
		name     string
		readOnly bool
		writeErr error
	}{
		{name: "write failure", writeErr: errors.New("disk is read-only")},
		{name: "stdin config", readOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, engine, writer, _ := newSnapshotTestService(t, test.readOnly)
			info, err := service.Create(context.Background(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
				t.Fatal(err)
			}
			writer.err = test.writeErr
			response, err := service.Restore(context.Background(), info.UUID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node"); !ok {
				t.Fatal("restored config is not active in memory")
			}
			if response.ConfigWritten {
				t.Fatalf("restore response = %#v, want no config write", response)
			}
			if test.writeErr != nil && !strings.Contains(response.ConfigWriteError, test.writeErr.Error()) {
				t.Fatalf("restore response = %#v, want write error", response)
			}
			if test.readOnly && response.ConfigWriteError != "" {
				t.Fatalf("read-only restore response = %#v, want no attempted write", response)
			}
		})
	}
}

func TestSnapshotCreateDoesNotReplaceExistingUUID(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	service.random = bytes.NewReader(make([]byte, 32))
	first, err := service.Create(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), "second"); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("duplicate UUID create error = %v, want invalid snapshot", err)
	}
	listed, err := service.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].UUID != first.UUID || listed[0].Name != "first" {
		t.Fatalf("snapshots after UUID collision = %#v", listed)
	}
}

func TestSnapshotExportImportPreservesMetadataAndSupportsNameOverride(t *testing.T) {
	service, _, _, _, storagePath := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "archive name")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Snapshot != created || len(exported.Archive) == 0 {
		t.Fatalf("exported snapshot = %#v, archive bytes = %d", exported.Snapshot, len(exported.Archive))
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}

	imported, err := service.Import(context.Background(), bytes.NewReader(exported.Archive), nil)
	if err != nil {
		t.Fatal(err)
	}
	if imported != created {
		t.Fatalf("imported snapshot = %#v, want %#v", imported, created)
	}
	if _, err := service.Import(context.Background(), bytes.NewReader(exported.Archive), nil); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("duplicate import error = %v, want invalid snapshot", err)
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}

	name := "import override"
	imported, err = service.Import(context.Background(), bytes.NewReader(exported.Archive), &name)
	if err != nil {
		t.Fatal(err)
	}
	if imported.UUID != created.UUID || imported.Timestamp != created.Timestamp || imported.Name != name {
		t.Fatalf("name-overridden import = %#v", imported)
	}
	manifest, err := readSnapshotManifest(filepath.Join(storagePath, snapshotDirectoryName, created.UUID+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Snapshot != imported {
		t.Fatalf("imported manifest = %#v, want %#v", manifest.Snapshot, imported)
	}
}

func TestSnapshotImportRejectsSymlinkThatEscapesStorage(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "unsafe link")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	archive := appendSnapshotArchiveEntry(
		t, exported.Archive, snapshotFilesPrefix+"escape", []byte(outside), os.ModeSymlink|0o777,
	)
	if _, err := service.Import(context.Background(), bytes.NewReader(archive), nil); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("escaping symlink import error = %v, want invalid snapshot", err)
	}
	if entries, err := os.ReadDir(filepath.Join(service.storagePath, snapshotDirectoryName)); err != nil {
		t.Fatal(err)
	} else {
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".zip" {
				t.Fatalf("unsafe imported archive was published as %s", entry.Name())
			}
		}
	}
}

func TestSnapshotImportRejectsWindowsPathAliases(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "path aliases")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		entries []string
	}{
		{name: "case collision", entries: []string{snapshotFilesPrefix + "Alias.txt", snapshotFilesPrefix + "alias.txt"}},
		{name: "reserved device", entries: []string{snapshotFilesPrefix + "nested/CON.txt"}},
		{name: "alternate stream", entries: []string{snapshotFilesPrefix + "secret.txt:stream"}},
		{name: "drive path", entries: []string{snapshotFilesPrefix + "C:/escape.txt"}},
		{name: "trailing dot", entries: []string{snapshotFilesPrefix + "alias./value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := exported.Archive
			for _, entry := range test.entries {
				archive = appendSnapshotArchiveEntry(t, archive, entry, []byte("value"), 0o600)
			}
			if _, err := service.Import(context.Background(), bytes.NewReader(archive), nil); !errors.Is(err, errSnapshotInvalid) {
				t.Fatalf("Import error = %v, want invalid snapshot", err)
			}
		})
	}
}

func TestSnapshotRestorePreservesSafeInternalSymlink(t *testing.T) {
	if !snapshotSymlinkRestoreSupported() {
		t.Skip("snapshot symlink restoration is not supported on this platform")
	}
	service, _, _, _, storagePath := newSnapshotTestService(t, false)
	targetPath := filepath.Join(storagePath, "linked", "target.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("snapshot value"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(storagePath, "link.txt")
	if err := os.Symlink(filepath.Join("linked", "target.txt"), linkPath); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), "safe link")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("changed value"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Restore(context.Background(), created.UUID); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(linkPath); err != nil || target != filepath.Join("linked", "target.txt") {
		t.Fatalf("restored symlink target = %q, err=%v", target, err)
	}
	if data, err := os.ReadFile(linkPath); err != nil || string(data) != "snapshot value" {
		t.Fatalf("restored symlink data = %q, err=%v", data, err)
	}
}

func TestSnapshotRestorePreservesConfigDirectoryWhenArchiveOmitsIt(t *testing.T) {
	service, _, _, writer, storagePath := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "no config directory")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	archive := filterSnapshotArchivePrefix(t, exported.Archive, snapshotFilesPrefix+"config")
	archivePath := filepath.Join(storagePath, snapshotDirectoryName, created.UUID+".zip")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	protected := []byte("protected after archive creation")
	if err := os.WriteFile(writer.protectedPath, protected, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Restore(context.Background(), created.UUID); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(writer.protectedPath); err != nil || !bytes.Equal(data, protected) {
		t.Fatalf("protected config after restore = %q, err=%v", data, err)
	}
	if info, err := os.Stat(filepath.Dir(writer.configPath)); err != nil || !info.IsDir() {
		t.Fatalf("config parent after restore = %#v, err=%v", info, err)
	}
}

func TestRestoreStartupSnapshotLoadsConfigDatabaseAndFilesFromStdin(t *testing.T) {
	service, manager, _, _, sourceStorage := newSnapshotTestService(t, false)
	if err := manager.DefaultStore().Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set("startup-kv", storage.KValue{Data: []byte("from-archive"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(sourceStorage, "startup-files", "value.txt")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("from-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), "startup")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	configPath := filepath.Join(destination, "config", "alm.hujson")
	protectedPath := filepath.Join(destination, "config", "protected_alm.hujson")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("old config"), 0o600); err != nil {
		t.Fatal(err)
	}
	protected := []byte("host protected config")
	if err := os.WriteFile(protectedPath, protected, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "remove.txt"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	locator := &snapshotTestWriter{configPath: configPath, protectedPath: protectedPath}
	archive := filterSnapshotArchivePrefix(t, exported.Archive, snapshotFilesPrefix+"config")
	configData, info, err := restoreStartupSnapshot(
		context.Background(), destination, "-", bytes.NewReader(archive), locator,
	)
	if err != nil {
		t.Fatal(err)
	}
	if info != created || !bytes.Contains(configData, []byte("saved-node")) || bytes.Contains(configData, []byte("Protected")) {
		t.Fatalf("startup snapshot info = %#v, config = %s", info, configData)
	}
	if data, err := os.ReadFile(protectedPath); err != nil || !bytes.Equal(data, protected) {
		t.Fatalf("protected config = %q, err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "remove.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file absent from startup snapshot still exists: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "startup-files", "value.txt")); err != nil || string(data) != "from-archive" {
		t.Fatalf("startup file = %q, err=%v", data, err)
	}
	if err := validateBboltDatabase(filepath.Join(destination, kvDBFileName)); err != nil {
		t.Fatalf("restored startup database: %v", err)
	}
}

func TestRestoreStartupSnapshotRollsBackPartialStorageChanges(t *testing.T) {
	service, _, _, _, sourceStorage := newSnapshotTestService(t, false)
	firstPath := filepath.Join(sourceStorage, "a-first.txt")
	if err := os.WriteFile(firstPath, []byte("snapshot value"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), "rollback")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	archive := filterSnapshotArchivePrefix(t, exported.Archive, snapshotFilesPrefix+"config")
	archive = appendSnapshotArchiveEntry(t, archive, snapshotFilesPrefix+"config", []byte("type conflict"), 0o600)

	destination := t.TempDir()
	configPath := filepath.Join(destination, "config", "alm.hujson")
	protectedPath := filepath.Join(destination, "config", "protected_alm.hujson")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("original config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protectedPath, []byte("original protected config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "a-first.txt"), []byte("original value"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(destination, kvDBFileName)
	database, err := bbolt.Open(databasePath, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("rollback"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("key"), []byte("original database"))
	}); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	locator := &snapshotTestWriter{configPath: configPath, protectedPath: protectedPath}
	if _, _, err := restoreStartupSnapshot(
		context.Background(), destination, "-", bytes.NewReader(archive), locator,
	); err == nil || !strings.Contains(err.Error(), "would replace a directory") {
		t.Fatalf("startup restore error = %v, want config directory conflict", err)
	}
	for path, expected := range map[string]string{
		configPath:    "original config",
		protectedPath: "original protected config",
		filepath.Join(destination, "a-first.txt"): "original value",
	} {
		if data, err := os.ReadFile(path); err != nil || string(data) != expected {
			t.Fatalf("rolled-back file %s = %q, err=%v; want %q", path, data, err, expected)
		}
	}
	database, err = bbolt.Open(databasePath, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close() //nolint:errcheck
	if err := database.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("rollback"))
		if bucket == nil || string(bucket.Get([]byte("key"))) != "original database" {
			return errors.New("startup rollback did not restore the original database")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotArchiveSizeLimits(t *testing.T) {
	if _, err := copySnapshotArchive(io.Discard, repeatingZeroReader{}); !errors.Is(err, errSnapshotTooLarge) {
		t.Fatalf("stream size error = %v, want snapshot too large", err)
	}
	path := filepath.Join(t.TempDir(), "oversized.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(api.SnapshotArchiveMaxBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotArchiveSize(path); !errors.Is(err, errSnapshotTooLarge) {
		t.Fatalf("stored archive size error = %v, want snapshot too large", err)
	}

	service, _, _, _, _ := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "oversized extracted data")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}
	archive := appendSnapshotArchive(t, exported.Archive, func(writer *zip.Writer) error {
		header := &zip.FileHeader{
			Name:               snapshotFilesPrefix + "oversized.bin",
			Method:             zip.Store,
			UncompressedSize64: snapshotExtractedMaxBytes + 1,
		}
		header.SetMode(0o600)
		_, err := writer.CreateRaw(header)
		return err
	})
	if _, err := service.Import(context.Background(), bytes.NewReader(archive), nil); !errors.Is(err, errSnapshotTooLarge) {
		t.Fatalf("extracted size error = %v, want snapshot too large", err)
	}
}

func TestSnapshotAdminAPIRejectsUnknownAndMalformedUUIDs(t *testing.T) {
	service, _, engine, _, _ := newSnapshotTestService(t, false)
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)

	malformed := mustRequest(t, "1", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotUUIDRequest{UUID: "../escape"})
	response, err := session.handleMessage(malformed)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("malformed UUID response = %#v, err=%v", response, err)
	}
	unknown := mustRequest(t, "2", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotUUIDRequest{UUID: "11111111-1111-4111-8111-111111111111"})
	response, err = session.handleMessage(unknown)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("unknown UUID response = %#v, err=%v", response, err)
	}

	malformed = mustRequest(t, "3", api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: "../escape"})
	response, err = session.handleMessage(malformed)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("malformed delete UUID response = %#v, err=%v", response, err)
	}
	unknown = mustRequest(t, "4", api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: "11111111-1111-4111-8111-111111111111"})
	response, err = session.handleMessage(unknown)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("unknown delete UUID response = %#v, err=%v", response, err)
	}

	malformed = mustRequest(t, "5", api.SurfaceSnapshots, string(api.MethodSnapshotsRename), api.SnapshotsRenameRequest{UUID: "../escape", Name: "name"})
	response, err = session.handleMessage(malformed)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("malformed rename UUID response = %#v, err=%v", response, err)
	}
	unknown = mustRequest(t, "6", api.SurfaceSnapshots, string(api.MethodSnapshotsRename), api.SnapshotsRenameRequest{
		UUID: "11111111-1111-4111-8111-111111111111",
		Name: "name",
	})
	response, err = session.handleMessage(unknown)
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("unknown rename UUID response = %#v, err=%v", response, err)
	}
}

func TestSnapshotAdminAPIDeletesArchive(t *testing.T) {
	service, _, engine, _, storagePath := newSnapshotTestService(t, false)
	info, err := service.Create(context.Background(), "delete through API")
	if err != nil {
		t.Fatal(err)
	}
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)
	renameRequest := mustRequest(t, "1", api.SurfaceSnapshots, string(api.MethodSnapshotsRename), api.SnapshotsRenameRequest{
		UUID: info.UUID,
		Name: "renamed through API",
	})
	renameResponse, err := session.handleMessage(renameRequest)
	if err != nil || renameResponse.Error != nil {
		t.Fatalf("rename response = %#v, err=%v", renameResponse, err)
	}
	renamed, err := api.DecodePayload[api.SnapshotInfo](renameResponse.Result)
	if err != nil || renamed.Name != "renamed through API" || renamed.UUID != info.UUID {
		t.Fatalf("renamed snapshot = %#v, err=%v", renamed, err)
	}
	request := mustRequest(t, "2", api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: info.UUID})
	response, err := session.handleMessage(request)
	if err != nil || response.Error != nil {
		t.Fatalf("delete response = %#v, err=%v", response, err)
	}
	if _, err := os.Stat(filepath.Join(storagePath, snapshotDirectoryName, info.UUID+".zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted snapshot archive still exists: %v", err)
	}
}

func TestSnapshotAdminAPIExportsAndImportsArchive(t *testing.T) {
	service, _, engine, _, _ := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "API archive")
	if err != nil {
		t.Fatal(err)
	}
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)
	exportRequest := mustRequest(t, "1", api.SurfaceSnapshots, string(api.MethodSnapshotsExport), api.SnapshotUUIDRequest{UUID: created.UUID})
	exportResponse, err := session.handleMessage(exportRequest)
	if err != nil || exportResponse.Error != nil {
		t.Fatalf("export response = %#v, err=%v", exportResponse, err)
	}
	exported, err := api.DecodePayload[api.SnapshotsExportResponse](exportResponse.Result)
	if err != nil || exported.Snapshot != created || len(exported.Archive) == 0 {
		t.Fatalf("exported snapshot = %#v, err=%v", exported, err)
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}

	name := "API import"
	importRequest := mustRequest(t, "2", api.SurfaceSnapshots, string(api.MethodSnapshotsImport), api.SnapshotsImportRequest{
		Archive: exported.Archive,
		Name:    &name,
	})
	importResponse, err := session.handleMessage(importRequest)
	if err != nil || importResponse.Error != nil {
		t.Fatalf("import response = %#v, err=%v", importResponse, err)
	}
	imported, err := api.DecodePayload[api.SnapshotInfo](importResponse.Result)
	if err != nil || imported.UUID != created.UUID || imported.Name != name {
		t.Fatalf("imported snapshot = %#v, err=%v", imported, err)
	}

	invalidRequest := mustRequest(t, "3", api.SurfaceSnapshots, string(api.MethodSnapshotsImport), api.SnapshotsImportRequest{Archive: []byte("not a ZIP")})
	invalidResponse, err := session.handleMessage(invalidRequest)
	if err != nil || invalidResponse.Error == nil || invalidResponse.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("invalid import response = %#v, err=%v", invalidResponse, err)
	}
}

func TestSnapshotExtractionRejectsSymlinkParent(t *testing.T) {
	if !snapshotSymlinkRestoreSupported() {
		t.Skip("snapshot symlink restoration is not supported on this platform")
	}
	outside := t.TempDir()
	stage := t.TempDir()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	if err := writeZipBytes(writer, "escape", []byte(outside), os.ModeSymlink|0o777, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writeZipBytes(writer, "escape/written.txt", []byte("unsafe"), 0o600, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if err := extractSnapshotFile(reader.File[0], stage, reader.File[0].Name); err != nil {
		t.Fatalf("extract symlink: %v", err)
	}
	err = extractSnapshotFile(reader.File[1], stage, reader.File[1].Name)
	if !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("extract through symlink error = %v, want invalid snapshot", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "written.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive extraction escaped staging: %v", err)
	}
}

func TestValidateBboltDatabaseRejectsNonDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), kvDBFileName)
	if err := os.WriteFile(path, []byte("not a bbolt database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateBboltDatabase(path); err == nil {
		t.Fatal("invalid bbolt database was accepted")
	}
}

func TestSnapshotValidationHelperBranches(t *testing.T) {
	for _, name := range []string{"", "/absolute", `back\\slash`, "../escape", "a/../b"} {
		if _, err := cleanSnapshotEntryName(name); !errors.Is(err, errSnapshotInvalid) {
			t.Errorf("cleanSnapshotEntryName(%q) error = %v", name, err)
		}
	}
	if got, err := cleanSnapshotEntryName("dir/file"); err != nil || got != "dir/file" {
		t.Fatalf("clean entry = %q, %v", got, err)
	}

	uuid, err := newSnapshotUUID(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := normalizeSnapshotUUID("  " + strings.ToUpper(uuid) + "  "); err != nil || got != uuid {
		t.Fatalf("normalized UUID = %q, %v", got, err)
	}
	if _, err := newSnapshotUUID(strings.NewReader("short")); err == nil {
		t.Fatal("short UUID randomness was accepted")
	}
	for _, value := range []string{"", "11111111-1111-1111-1111-11111111111z"} {
		if _, err := normalizeSnapshotUUID(value); !errors.Is(err, errSnapshotInvalid) {
			t.Errorf("normalizeSnapshotUUID(%q) error = %v", value, err)
		}
	}
	if err := validateSnapshotName(string([]byte{0xff})); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("invalid UTF-8 name error = %v", err)
	}
	if err := validateSnapshotName(strings.Repeat("x", snapshotNameMaxBytes+1)); !errors.Is(err, errSnapshotTooLarge) {
		t.Fatalf("long name error = %v", err)
	}

	temp := t.TempDir()
	if err := validateSnapshotArchiveSize(filepath.Join(temp, "missing")); !errors.Is(err, errSnapshotNotFound) {
		t.Fatalf("missing archive error = %v", err)
	}
	if err := validateSnapshotArchiveSize(temp); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("directory archive error = %v", err)
	}
	archive := filepath.Join(temp, "small.zip")
	if err := os.WriteFile(archive, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotArchiveSize(archive); err != nil {
		t.Fatal(err)
	}
	if _, err := copySnapshotArchive(snapshotErrorWriter{}, strings.NewReader("data")); err == nil {
		t.Fatal("copy writer error was ignored")
	}
	if written, err := copySnapshotArchive(io.Discard, strings.NewReader("data")); err != nil || written != 4 {
		t.Fatalf("copy = %d, %v", written, err)
	}
}

func TestSnapshotAdminAPICreateAndList(t *testing.T) {
	service, _, engine, _, _ := newSnapshotTestService(t, false)
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)
	create := mustRequest(t, "create", api.SurfaceSnapshots, string(api.MethodSnapshotsCreate), api.SnapshotsCreateRequest{Name: "from API"})
	response, err := session.handleMessage(create)
	if err != nil || response.Error != nil {
		t.Fatalf("create response = %#v, err=%v", response, err)
	}
	created, err := api.DecodePayload[api.SnapshotInfo](response.Result)
	if err != nil || created.Name != "from API" {
		t.Fatalf("created snapshot = %#v, err=%v", created, err)
	}
	list := mustRequest(t, "list", api.SurfaceSnapshots, string(api.MethodSnapshotsList), struct{}{})
	response, err = session.handleMessage(list)
	if err != nil || response.Error != nil {
		t.Fatalf("list response = %#v, err=%v", response, err)
	}
	listed, err := api.DecodePayload[api.SnapshotsListResponse](response.Result)
	if err != nil || len(listed.Snapshots) != 1 || listed.Snapshots[0].UUID != created.UUID {
		t.Fatalf("listed snapshots = %#v, err=%v", listed, err)
	}
}

func TestSnapshotMalformedArchiveBranches(t *testing.T) {
	service, _, _, _, storagePath := newSnapshotTestService(t, false)
	if err := os.MkdirAll(filepath.Join(storagePath, snapshotDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	validManifest := []byte(`{"version":1,"snapshot":{"uuid":"11111111-1111-4111-8111-111111111111","timestamp":1}}`)
	tests := []struct {
		name    string
		entries []struct {
			name string
			data []byte
		}
	}{
		{name: "empty"},
		{name: "unsafe", entries: []struct {
			name string
			data []byte
		}{{name: "../escape", data: nil}}},
		{name: "unknown", entries: []struct {
			name string
			data []byte
		}{{name: "unknown", data: nil}}},
		{name: "invalid manifest JSON", entries: []struct {
			name string
			data []byte
		}{{name: snapshotManifestName, data: []byte(`{`)}}},
		{name: "unsupported version", entries: []struct {
			name string
			data []byte
		}{{name: snapshotManifestName, data: []byte(`{"version":2,"snapshot":{"uuid":"11111111-1111-4111-8111-111111111111","timestamp":1}}`)}}},
		{name: "duplicate", entries: []struct {
			name string
			data []byte
		}{{name: snapshotManifestName, data: validManifest}, {name: snapshotManifestName, data: validManifest}}},
		{name: "invalid config", entries: []struct {
			name string
			data []byte
		}{{name: snapshotManifestName, data: validManifest}, {name: snapshotConfigName, data: []byte(`{`)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(storagePath, snapshotDirectoryName, strings.ReplaceAll(test.name, " ", "-")+".zip")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			for _, entry := range test.entries {
				if err := writeZipBytes(writer, entry.name, entry.data, 0o600, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := service.loadArchive(context.Background(), path, ""); !errors.Is(err, errSnapshotInvalid) {
				t.Fatalf("loadArchive error = %v", err)
			}
			if test.name != "unsafe" && test.name != "unknown" && test.name != "duplicate" && test.name != "invalid config" {
				if _, err := readSnapshotManifest(path); !errors.Is(err, errSnapshotInvalid) {
					t.Fatalf("readSnapshotManifest error = %v", err)
				}
			}
		})
	}
}

func newSnapshotTestService(t *testing.T, readOnly bool) (*snapshotService, *bboltKVManager, almagest.Engine, *snapshotTestWriter, string) {
	t.Helper()
	storagePath := t.TempDir()
	configPath := filepath.Join(storagePath, "config", "alm.hujson")
	protectedPath := filepath.Join(storagePath, "config", "protected_alm.hujson")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	mainData := []byte(`{
		"nodes": {"saved-node": {"Class": "test/AdminAPIBatchNode"}},
		"Protected": {"inline": "embedded-secret"}
	}`)
	protectedData := []byte(`{"Secrets":{"protected-only":{"Value":"never-archive","Nodes":["saved-node"]}}}`)
	if err := os.WriteFile(configPath, mainData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protectedPath, protectedData, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := ParseConfig(mainData, protectedData)
	if err != nil {
		t.Fatal(err)
	}
	builtinCA, err := loadOrCreateTLSBuiltinCA(storagePath, defaultTLSBuiltinCAOptions(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	immutableSecrets, err := config.Protected.ImmutableSecrets()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := openKVManagerInStorage(storagePath, bboltKVManagerOptions{
		ImmutableSecrets: immutableSecrets,
		BuiltinCA:        builtinCA,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	engine, err := almagest.BuildEngine(
		context.Background(), config.Main, logging.NewDebugLogFactory(128), &sysnetdebug.System{},
		manager, manager, manager, manager, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	writer := &snapshotTestWriter{configPath: configPath, protectedPath: protectedPath}
	autosaver := newConfigAutosaver(
		engine, config, newTestConfigPersistence(t, config, writer),
		logging.NewDebugLogFactory(32).EngineLogger(), time.Millisecond,
	)
	service := newSnapshotService(
		storagePath, autosaver, engine, manager, config, writer, readOnly,
		logging.NewDebugLogFactory(32).EngineLogger(),
		tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) { return nil, nil }),
	)
	return service, manager, engine, writer, storagePath
}

func assertKVString(t *testing.T, manager *bboltKVManager, key, expected string) {
	t.Helper()
	err := manager.DefaultStore().View(context.Background(), func(tx storage.KVReadTx) error {
		value, found, err := tx.Get(key)
		if err != nil {
			return err
		}
		if !found || string(value.Data) != expected {
			t.Fatalf("KV %q = %#v, found=%t; want %q", key, value, found, expected)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotArchiveIsSanitized(t *testing.T, storagePath, uuid string) {
	t.Helper()
	reader, err := zip.OpenReader(filepath.Join(storagePath, snapshotDirectoryName, uuid+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close() //nolint:errcheck
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, snapshotFilesPrefix+snapshotDirectoryName+"/") {
			t.Fatalf("snapshot contains its own snapshot storage: %s", file.Name)
		}
		if file.Name == snapshotFilesPrefix+"config/protected_alm.hujson" {
			t.Fatalf("snapshot contains protected config: %s", file.Name)
		}
		if file.Name != snapshotConfigName {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(entry)
		_ = entry.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("Protected")) || bytes.Contains(data, []byte("embedded-secret")) {
			t.Fatalf("snapshot config contains protected content: %s", data)
		}
	}
}

func filterSnapshotArchivePrefix(t *testing.T, archive []byte, excludedPrefix string) []byte {
	t.Helper()
	return rewriteSnapshotArchive(t, archive, func(writer *zip.Writer, file *zip.File) error {
		if strings.HasPrefix(file.Name, excludedPrefix) {
			return nil
		}
		return writer.Copy(file)
	})
}

func appendSnapshotArchiveEntry(t *testing.T, archive []byte, name string, data []byte, mode fs.FileMode) []byte {
	t.Helper()
	return appendSnapshotArchive(t, archive, func(writer *zip.Writer) error {
		return writeZipBytes(writer, name, data, mode, time.Now())
	})
}

func appendSnapshotArchive(t *testing.T, archive []byte, appendEntry func(*zip.Writer) error) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		if err := writer.Copy(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendEntry(writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func rewriteSnapshotArchive(
	t *testing.T,
	archive []byte,
	copyEntry func(*zip.Writer, *zip.File) error,
) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		if err := copyEntry(writer, file); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
