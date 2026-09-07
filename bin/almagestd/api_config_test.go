package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

type configAPITestWriter struct {
	writes     [][]byte
	failWrites int
}

func (w *configAPITestWriter) WriteConfig(data []byte) error {
	w.writes = append(w.writes, bytes.Clone(data))
	if w.failWrites > 0 {
		w.failWrites--
		return errors.New("test config write failure")
	}
	return nil
}

func TestConfigAPIExposesOnlyEditableTextAndAppliesRevisionControlledUpdate(t *testing.T) {
	conf, err := ParseConfig(
		[]byte("{\n\t// editable marker\n\t\"nodes\": {},\n\t// embedded-comment-secret\n\t\"Protected\": {\"embedded-token\": \"embedded-secret\"}\n}\n"),
		[]byte(`{"Secrets":{"protected-token":{"Value":"must-not-leak","Nodes":[]}}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAPITestWriter{}
	autosaver := newConfigAutosaver(engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), 0)
	admin := &AdminAPI{config: conf, configAutosaver: autosaver}
	session := admin.newAPISession(engine, nil, nil)

	snapshotReq := mustRequest(t, "1", api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{})
	snapshotResp, err := session.handleMessage(snapshotReq)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotResp.Error != nil {
		t.Fatalf("snapshot error = %#v", snapshotResp.Error)
	}
	snapshot, err := api.DecodePayload[api.ConfigSnapshot](snapshotResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot.Text, "editable marker") {
		t.Fatalf("snapshot text = %q, want editable marker", snapshot.Text)
	}
	for _, secret := range []string{"Protected", "embedded-comment-secret", "embedded-token", "embedded-secret", "protected-token", "must-not-leak"} {
		if strings.Contains(snapshot.Text, secret) {
			t.Fatalf("snapshot disclosed protected text %q: %s", secret, snapshot.Text)
		}
	}
	if snapshot.Revision == "" || snapshot.ReadOnly {
		t.Fatalf("snapshot = %#v, want writable revision", snapshot)
	}

	updatedText := `{
		// editable marker stays
		"nodes": {
			"api-added": {"Class": "test/AdminAPIBatchNode", "Pos": "from-config"}
		}
	}`
	updateReq := mustRequest(t, "2", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text: updatedText, Revision: snapshot.Revision,
	})
	updateResp, err := session.handleMessage(updateReq)
	if err != nil {
		t.Fatal(err)
	}
	if updateResp.Error != nil {
		t.Fatalf("update error = %#v", updateResp.Error)
	}
	updated, err := api.DecodePayload[api.ConfigUpdateResponse](updateResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesEqual(updated.Changes.Added, []string{"api-added"}) {
		t.Fatalf("added nodes = %v, want api-added", updated.Changes.Added)
	}
	id, ok := engine.NodeByName(almagest.MainWorkspace, "api-added")
	if !ok {
		t.Fatal("updated config did not add node")
	}
	node, ok := engine.NodeSnapshot(almagest.MainWorkspace, id)
	if !ok || node.Position != "from-config" {
		t.Fatalf("added node = %#v, %v", node, ok)
	}
	if len(writer.writes) != 1 || !bytes.Contains(writer.writes[0], []byte("editable marker stays")) {
		t.Fatalf("config writes = %q, want immediate updated document", writer.writes)
	}

	staleReq := mustRequest(t, "3", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text: `{}`, Revision: snapshot.Revision,
	})
	staleResp, err := session.handleMessage(staleReq)
	if err != nil {
		t.Fatal(err)
	}
	if staleResp.Error == nil || staleResp.Error.Code != api.ErrorConflict {
		t.Fatalf("stale update error = %#v, want conflict", staleResp.Error)
	}
	if len(writer.writes) != 1 {
		t.Fatalf("writes after stale update = %d, want 1", len(writer.writes))
	}
}

func TestConfigAPIUpdateRemovesDanglingLinksAndSynchronizesNodeComments(t *testing.T) {
	conf, err := ParseConfig([]byte(`{
		"nodes": {
			// Original note
			"A": {"Class": "test/ConfigAPIReconcileNode", "Links": ["Out -> [B] In"]},
			"B": {"Class": "test/ConfigAPIReconcileNode"}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{configAPIReconcileNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAPITestWriter{}
	autosaver := newConfigAutosaver(engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), 0)
	session := (&AdminAPI{config: conf, configAutosaver: autosaver}).newAPISession(engine, nil, nil)
	before, err := autosaver.Snapshot(false)
	if err != nil {
		t.Fatal(err)
	}

	updateReq := mustRequest(t, "1", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text: `{
			"nodes": {
				// Updated note
				"A": {"Class": "test/ConfigAPIReconcileNode", "Links": ["Out -> [B] In"]}
			}
		}`,
		Revision: before.Revision,
	})
	updateResp, err := session.handleMessage(updateReq)
	if err != nil {
		t.Fatal(err)
	}
	if updateResp.Error != nil {
		t.Fatalf("update error = %#v", updateResp.Error)
	}
	updated, err := api.DecodePayload[api.ConfigUpdateResponse](updateResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesEqual(updated.Changes.Removed, []string{"B"}) ||
		!slicesEqual(updated.Changes.LinksRemoved, []string{"Out -> [B] In"}) {
		t.Fatalf("update changes = %#v, want B and its incoming link removed", updated.Changes)
	}
	if strings.Contains(updated.Snapshot.Text, "Out -> [B] In") {
		t.Fatalf("canonical config retained dangling link: %s", updated.Snapshot.Text)
	}
	if _, ok := engine.NodeByName(almagest.MainWorkspace, "B"); ok {
		t.Fatal("deleted node B is still present")
	}
	nodeID, ok := engine.NodeByName(almagest.MainWorkspace, "A")
	if !ok {
		t.Fatal("node A is missing")
	}
	node, ok := engine.NodeSnapshot(almagest.MainWorkspace, nodeID)
	if !ok || len(node.Popups) != 1 || node.Popups[0].Type != pasta.NodePopupInfo || node.Popups[0].Text != "Updated note" {
		t.Fatalf("node A popups = %#v, want updated config note", node.Popups)
	}

	removeCommentReq := mustRequest(t, "2", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text:     `{"nodes":{"A":{"Class":"test/ConfigAPIReconcileNode"}}}`,
		Revision: updated.Snapshot.Revision,
	})
	removeCommentResp, err := session.handleMessage(removeCommentReq)
	if err != nil {
		t.Fatal(err)
	}
	if removeCommentResp.Error != nil {
		t.Fatalf("remove comment error = %#v", removeCommentResp.Error)
	}
	node, ok = engine.NodeSnapshot(almagest.MainWorkspace, nodeID)
	if !ok || len(node.Popups) != 0 {
		t.Fatalf("node A popups after comment removal = %#v, want none", node.Popups)
	}
}

type configAPIReconcileNodeClass struct{}

func (configAPIReconcileNodeClass) ClassName() string { return "test/ConfigAPIReconcileNode" }
func (configAPIReconcileNodeClass) ShortDescription() string {
	return "Config API reconcile test"
}
func (configAPIReconcileNodeClass) LongDescription() string {
	return "Tests config API graph reconciliation."
}
func (configAPIReconcileNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{
		{Direction: "left", Name: "In", Types: []string{pasta.AnyType}},
		{Direction: "right", Name: "Out", Types: []string{pasta.AnyType}},
	}}
}
func (configAPIReconcileNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &configAPIReconcileNode{}, nil
}

type configAPIReconcileNode struct {
	pasta.BasicNode
	_ byte
}

func (*configAPIReconcileNode) PreLinkAdd(uint64, string, string) error { return nil }

func TestConfigAPIRejectsProtectedContentAndReadOnlyUpdates(t *testing.T) {
	conf, err := ParseConfig([]byte(`{"nodes":{}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(16), &sysnetdebug.System{},
		nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	writer := &configAPITestWriter{}
	autosaver := newConfigAutosaver(engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), 0)
	admin := &AdminAPI{config: conf, configAutosaver: autosaver}
	session := admin.newAPISession(engine, nil, nil)

	protectedReq := mustRequest(t, "1", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text: `{"nodes":{},"Protected":{"Secrets":{"token":{"Value":"secret"}}}}`,
	})
	protectedResp, err := session.handleMessage(protectedReq)
	if err != nil {
		t.Fatal(err)
	}
	if protectedResp.Error == nil || protectedResp.Error.Code != api.ErrorInvalidPayload {
		t.Fatalf("protected update error = %#v, want invalid payload", protectedResp.Error)
	}
	if len(writer.writes) != 0 {
		t.Fatalf("protected update wrote %d configs", len(writer.writes))
	}

	admin.configReadOnly = true
	readOnlyReq := mustRequest(t, "2", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{Text: `{}`})
	readOnlyResp, err := session.handleMessage(readOnlyReq)
	if err != nil {
		t.Fatal(err)
	}
	if readOnlyResp.Error == nil || readOnlyResp.Error.Code != api.ErrorForbidden {
		t.Fatalf("read-only update error = %#v, want forbidden", readOnlyResp.Error)
	}
	snapshotReq := mustRequest(t, "3", api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{})
	snapshotResp, err := session.handleMessage(snapshotReq)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := api.DecodePayload[api.ConfigSnapshot](snapshotResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ReadOnly {
		t.Fatal("read-only snapshot is writable")
	}
}

func TestConfigOnlyBytesRemovesProtectedMemberTrivia(t *testing.T) {
	data, err := configOnlyBytes([]byte(`{
		// editable comment
		"nodes": {},
		// first-protected-marker
		"Protected": {"first-secret": true},
		// second-protected-marker
		"Protected": {"second-secret": true},
	}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "editable comment") {
		t.Fatalf("editable comment was removed: %s", text)
	}
	for _, marker := range []string{
		"Protected", "first-protected-marker", "first-secret", "second-protected-marker", "second-secret",
	} {
		if strings.Contains(text, marker) {
			t.Fatalf("config-only text disclosed %q: %s", marker, text)
		}
	}
}

func TestConfigAPIWriteFailureRollsBackGraphAndDocument(t *testing.T) {
	conf, err := ParseConfig([]byte("{\n\t// original marker\n\t\"nodes\": {}\n}\n"), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(16), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAPITestWriter{failWrites: 1}
	autosaver := newConfigAutosaver(engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), 0)
	session := (&AdminAPI{config: conf, configAutosaver: autosaver}).newAPISession(engine, nil, nil)
	snapshot, err := autosaver.Snapshot(false)
	if err != nil {
		t.Fatal(err)
	}

	request := mustRequest(t, "1", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text:     `{"nodes":{"temporary":{"Class":"test/AdminAPIBatchNode"}}}`,
		Revision: snapshot.Revision,
	})
	response, err := session.handleMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != api.ErrorMutationRejected {
		t.Fatalf("write failure response = %#v, want mutation_rejected", response.Error)
	}
	if _, ok := engine.NodeByName(almagest.MainWorkspace, "temporary"); ok {
		t.Fatal("temporary node remained after config write rollback")
	}
	current, err := conf.MainToBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(current, []byte("original marker")) || bytes.Contains(current, []byte("temporary")) {
		t.Fatalf("current config after rollback = %s", current)
	}
	if len(writer.writes) != 2 || !bytes.Contains(writer.writes[0], []byte("temporary")) ||
		!bytes.Contains(writer.writes[1], []byte("original marker")) {
		t.Fatalf("write attempts after rollback = %q", writer.writes)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
