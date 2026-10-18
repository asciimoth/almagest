package almagest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/pasta/pasta"
)

type ownerTestService struct {
	mu          sync.Mutex
	next        int
	owners      map[string]sandbox.Owner
	renamed     []sandbox.OwnerRename
	released    []sandbox.Owner
	rebindErr   error
	restoredErr error
	closed      bool
	closeCalls  int
}

func newOwnerTestService() *ownerTestService {
	return &ownerTestService{owners: map[string]sandbox.Owner{}}
}
func (s *ownerTestService) IssueOwner(_ context.Context, proposed sandbox.Owner) (sandbox.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if proposed.Key != "" && s.restoredErr != nil {
		return sandbox.Owner{}, s.restoredErr
	}
	if proposed.Key == "" {
		s.next++
		proposed.Key = fmt.Sprintf("key-%d", s.next)
		proposed.Epoch = 1
		proposed.NameRevision = 1
	} else if current, ok := s.owners[proposed.Key]; ok && current != proposed {
		return sandbox.Owner{}, sandbox.ErrStale
	}
	s.owners[proposed.Key] = proposed
	return proposed, nil
}
func (s *ownerTestService) IssueAndBindOwner(ctx context.Context, proposed sandbox.Owner) (sandbox.Owner, sandbox.OwnerSandboxes, error) {
	owner, err := s.IssueOwner(ctx, proposed)
	if err != nil {
		return sandbox.Owner{}, nil, err
	}
	view, err := s.BindOwner(ctx, owner)
	return owner, view, err
}
func (s *ownerTestService) BindOwner(_ context.Context, owner sandbox.Owner) (sandbox.OwnerSandboxes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, sandbox.ErrClosed
	}
	if s.owners[owner.Key] != owner {
		return nil, sandbox.ErrDenied
	}
	return ownerTestSandboxes{}, nil
}
func (s *ownerTestService) RebindOwner(_ context.Context, request sandbox.OwnerRename) (sandbox.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rebindErr != nil {
		return sandbox.Owner{}, s.rebindErr
	}
	if s.owners[request.Owner.Key] != request.Owner {
		return sandbox.Owner{}, sandbox.ErrStale
	}
	updated := request.Owner
	updated.Name = request.NewName
	updated.NameRevision++
	s.owners[updated.Key] = updated
	s.renamed = append(s.renamed, request)
	return updated, nil
}

func TestEngineRenameDoesNotPublishFailedSandboxRebind(t *testing.T) {
	service := newOwnerTestService()
	bound := make(chan error, 1)
	cfg := configer.NewMemory(map[string]any{"nodes": map[string]any{"First": map[string]any{"Class": "test/SandboxOwner"}}})
	engineAPI, err := BuildEngine(context.Background(), cfg, logging.NewDebugLogFactory(8), newEngineTestDebugSystem(), nil, nil, nil, nil, []pasta.NodeClass{sandboxOwnerTestClass{bound: bound}}, nil, false, WithSandboxes(service))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engineAPI.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	snapshot, _ := engineAPI.WorkspaceSnapshot(MainWorkspace)
	var id uint64
	for nodeID := range snapshot.Nodes {
		id = nodeID
	}
	if _, err = engineAPI.(*engine).SandboxesForNode(context.Background(), id, "", ""); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.rebindErr = sandbox.ErrUnavailable
	service.mu.Unlock()
	if err = engineAPI.RenameNode(MainWorkspace, id, "Second"); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("rename error = %v", err)
	}
	snapshot, _ = engineAPI.WorkspaceSnapshot(MainWorkspace)
	if snapshot.Nodes[id].Name != "First" {
		t.Fatalf("failed rename published name %q", snapshot.Nodes[id].Name)
	}
}
func (s *ownerTestService) ReleaseOwner(_ context.Context, owner sandbox.Owner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, owner)
	return nil
}
func (s *ownerTestService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.closeCalls++
	return nil
}

type ownerTestSandboxes struct{}

func (ownerTestSandboxes) Acquire(context.Context, string, sandbox.TemplateID) (sandbox.Sandbox, error) {
	return nil, sandbox.ErrUnavailable
}
func (ownerTestSandboxes) Remove(context.Context, string) error { return nil }

type sandboxOwnerTestClass struct {
	runtime nodekit.Runtime
	bound   chan error
}

func (c sandboxOwnerTestClass) ClassName() string        { return "test/SandboxOwner" }
func (c sandboxOwnerTestClass) ShortDescription() string { return "sandbox owner" }
func (c sandboxOwnerTestClass) LongDescription() string  { return "sandbox owner test" }
func (c sandboxOwnerTestClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}
func (c sandboxOwnerTestClass) BindRuntime(runtime nodekit.Runtime) pasta.NodeClass {
	c.runtime = runtime
	return c
}
func (c sandboxOwnerTestClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &sandboxOwnerTestNode{runtime: c.runtime, bound: c.bound}, nil
}

type sandboxOwnerTestNode struct {
	pasta.BasicNode
	runtime nodekit.Runtime
	base    nodekit.Base
	bound   chan error
}

func (n *sandboxOwnerTestNode) OnInit(w *pasta.Workspace, l pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.base.Runtime = n.runtime
	n.base.InitBaseNode(w, l, id)
	return nil
}
func (n *sandboxOwnerTestNode) OnReady() error {
	go func() {
		_, err := n.base.Sandboxes(context.Background())
		n.bound <- err
	}()
	return nil
}

func TestEngineSandboxOwnersPersistRenameAndRelease(t *testing.T) {
	service := newOwnerTestService()
	bound := make(chan error, 2)
	cfg := configer.NewMemory(map[string]any{"nodes": map[string]any{"First": map[string]any{"Class": "test/SandboxOwner"}}})
	engineAPI, err := BuildEngine(context.Background(), cfg, logging.NewDebugLogFactory(8), newEngineTestDebugSystem(), nil, nil, nil, nil, []pasta.NodeClass{sandboxOwnerTestClass{bound: bound}}, nil, false, WithSandboxes(service))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case bindErr := <-bound:
		if bindErr != nil {
			t.Fatalf("sandbox bind during engine startup: %v", bindErr)
		}
	case <-time.After(time.Second):
		t.Fatal("sandbox bind did not resume after workspace startup")
	}
	snapshot, _ := engineAPI.WorkspaceSnapshot(MainWorkspace)
	var id uint64
	for nodeID, node := range snapshot.Nodes {
		if node.Name == "First" {
			id = nodeID
		}
	}
	if id == 0 {
		t.Fatal("node missing")
	}
	if _, err = engineAPI.(*engine).SandboxesForNode(context.Background(), id, "", ""); err != nil {
		t.Fatal(err)
	}
	engineAPI.SetNodeName(MainWorkspace, id, "Second")
	saved := configer.NewMemory(nil)
	if err = engineAPI.SaveConfig(saved); err != nil {
		t.Fatal(err)
	}
	value, err := saved.Get(configer.Path{"nodes", "Second", sandboxOwnerConfigKey})
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok := value.(map[string]any)
	revision, revisionOK := configUint64(metadata["NameRevision"])
	if !ok || metadata["Key"] != "key-1" || !revisionOK || revision != 2 {
		t.Fatalf("metadata=%#v", value)
	}
	service.mu.Lock()
	if len(service.renamed) != 1 || service.renamed[0].NewName != "Second" {
		t.Fatalf("renames=%+v", service.renamed)
	}
	service.mu.Unlock()
	engineAPI.RemoveNode(MainWorkspace, id)
	deadline := time.Now().Add(time.Second)
	released := 0
	for released != 1 && time.Now().Before(deadline) {
		service.mu.Lock()
		released = len(service.released)
		service.mu.Unlock()
		if released != 1 {
			time.Sleep(time.Millisecond)
		}
	}
	if released != 1 {
		t.Fatalf("released=%d", released)
	}
	if err = engineAPI.Close(); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closeCalls != 1 {
		t.Fatalf("sandbox service close calls = %d, want 1", service.closeCalls)
	}
}

func TestEngineRejectsInvalidRestoredSandboxOwner(t *testing.T) {
	service := newOwnerTestService()
	cfg := configer.NewMemory(map[string]any{"nodes": map[string]any{"First": map[string]any{"Class": "test/SandboxOwner", "SandboxOwner": map[string]any{"Key": "../escape", "Epoch": 1, "NameRevision": 1}}}})
	_, err := BuildEngine(context.Background(), cfg, logging.NewDebugLogFactory(1), newEngineTestDebugSystem(), nil, nil, nil, nil, nil, nil, false, WithSandboxes(service))
	if err == nil {
		t.Fatal("invalid restored owner accepted")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closeCalls != 1 {
		t.Fatalf("sandbox service close calls = %d, want 1", service.closeCalls)
	}
}

func TestBuildEngineClosesSandboxServiceOnceAfterWorkspaceFailure(t *testing.T) {
	service := newOwnerTestService()
	cfg := configer.NewMemory(map[string]any{"nodes": map[string]any{"Invalid": map[string]any{"Class": "../invalid"}}})
	_, err := BuildEngine(context.Background(), cfg, logging.NewDebugLogFactory(1), newEngineTestDebugSystem(), nil, nil, nil, nil, nil, nil, false, WithSandboxes(service))
	if err == nil {
		t.Fatal("invalid node class accepted")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closeCalls != 1 {
		t.Fatalf("sandbox service close calls = %d, want 1", service.closeCalls)
	}
}

func TestEngineResolvesCurrentNodeIdentityAtFirstSandboxBind(t *testing.T) {
	service := newOwnerTestService()
	cfg := configer.NewMemory(map[string]any{"nodes": map[string]any{
		"First": map[string]any{"Class": "debug/Discard"},
	}})
	engineAPI, err := BuildEngine(context.Background(), cfg, logging.NewDebugLogFactory(8), newEngineTestDebugSystem(),
		nil, nil, nil, nil, nil, nil, false, WithSandboxes(service))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	snapshot, _ := engineAPI.WorkspaceSnapshot(MainWorkspace)
	var id uint64
	for nodeID := range snapshot.Nodes {
		id = nodeID
	}
	if err = engineAPI.RenameNode(MainWorkspace, id, "Second"); err != nil {
		t.Fatal(err)
	}
	engine := engineAPI.(*engine)
	if _, err = engine.SandboxesForNode(context.Background(), id, "forged/Class", "First"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if owner := service.owners["key-1"]; owner.Class != "debug/Discard" || owner.Name != "Second" {
		t.Fatalf("owner identity = %+v", owner)
	}
}

func TestCopiedSandboxOwnerMetadataGetsNewIdentity(t *testing.T) {
	metadata := map[string]any{"Key": "durable", "Epoch": 1, "NameRevision": 1}
	nodes := configer.NewMemory(map[string]any{
		"First":  map[string]any{"SandboxOwner": metadata},
		"Second": map[string]any{"SandboxOwner": metadata},
	})
	service := newOwnerTestService()
	service.owners["durable"] = sandbox.Owner{Workspace: MainWorkspace, Key: "durable", Epoch: 1, Class: "test/SandboxOwner", Name: "First", NameRevision: 1}
	coordinator, err := newSandboxOwners(service, nodes)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := coordinator.bind(context.Background(), 1, "test/SandboxOwner", "First")
	if err != nil || first == nil {
		t.Fatalf("first bind: %v", err)
	}
	second, _, err := coordinator.bind(context.Background(), 2, "test/SandboxOwner", "Second")
	if err != nil || second == nil {
		t.Fatalf("copied bind: %v", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.owners) != 2 || service.owners["durable"].Name != "First" || service.owners["key-1"].Name != "Second" {
		t.Fatalf("owners = %+v", service.owners)
	}
}

func TestRestoredSandboxOwnerSurvivesRenameBeforeBind(t *testing.T) {
	metadata := map[string]any{"Key": "durable", "Epoch": 3, "NameRevision": 4}
	nodes := configer.NewMemory(map[string]any{"First": map[string]any{"SandboxOwner": metadata}})
	service := newOwnerTestService()
	service.owners["durable"] = sandbox.Owner{Workspace: MainWorkspace, Key: "durable", Epoch: 3,
		Class: "test/SandboxOwner", Name: "Second", NameRevision: 4}
	coordinator, err := newSandboxOwners(service, nodes)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.attachRestored(pasta.WorkspaceSnapshot{Nodes: map[uint64]pasta.NodeSnapshot{7: {Name: "First"}}})
	if err = coordinator.rename(7, "Second"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = coordinator.bind(context.Background(), 7, "test/SandboxOwner", "Second"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.next != 0 || service.owners["durable"].Name != "Second" {
		t.Fatalf("restored owner was replaced: next=%d owners=%+v", service.next, service.owners)
	}
}

func TestQuarantinedRestoredSandboxOwnerGetsNewIdentity(t *testing.T) {
	metadata := map[string]any{"Key": "quarantined", "Epoch": 1, "NameRevision": 1}
	nodes := configer.NewMemory(map[string]any{
		"First": map[string]any{"SandboxOwner": metadata},
	})
	service := newOwnerTestService()
	service.restoredErr = errors.Join(sandbox.ErrUnavailable, sandbox.ErrStale)
	coordinator, err := newSandboxOwners(service, nodes)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.attachRestored(pasta.WorkspaceSnapshot{Nodes: map[uint64]pasta.NodeSnapshot{7: {Name: "First"}}})
	if _, _, err = coordinator.bind(context.Background(), 7, "test/SandboxOwner", "First"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.next != 1 || service.owners["key-1"].Name != "First" {
		t.Fatalf("replacement owner was not issued: next=%d owners=%+v", service.next, service.owners)
	}
}
