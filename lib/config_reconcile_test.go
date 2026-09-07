package almagest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
)

const configReconcileTestClassName = "test/ConfigReconcile"

type configReconcileTestTracker struct {
	mu     sync.Mutex
	inits  map[string]int
	stops  map[string]int
	values map[string]string
}

type configReconcileTestClass struct {
	tracker *configReconcileTestTracker
}

func (configReconcileTestClass) ClassName() string        { return configReconcileTestClassName }
func (configReconcileTestClass) ShortDescription() string { return "Config reconcile test" }
func (configReconcileTestClass) LongDescription() string {
	return "Tests partial config reconciliation."
}
func (configReconcileTestClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{
		{Direction: "left", Name: "In", Types: []string{pasta.AnyType}},
		{Direction: "right", Name: "Out", Types: []string{pasta.AnyType}},
	}}
}

func (c configReconcileTestClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &configReconcileTestNode{
		tracker: c.tracker,
		value:   configer.String(cfg, configer.Path{"value"}, ""),
	}, nil
}

type configReconcileTestNode struct {
	pasta.BasicNode
	tracker *configReconcileTestTracker
	name    string
	value   string
}

func (n *configReconcileTestNode) OnInit(_ *pasta.Workspace, _ pasta.Logger, _ uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.name = restored.Name
	n.tracker.mu.Lock()
	defer n.tracker.mu.Unlock()
	n.tracker.inits[n.name]++
	n.tracker.values[n.name] = n.value
	return nil
}

func (n *configReconcileTestNode) OnStop() {
	n.tracker.mu.Lock()
	defer n.tracker.mu.Unlock()
	n.tracker.stops[n.name]++
}

func (*configReconcileTestNode) PreLinkAdd(uint64, string, string) error { return nil }

func (n *configReconcileTestNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{"value"}, n.value)
}

func TestEngineReconcileConfigReinitializesOnlyAffectedGraphParts(t *testing.T) {
	tracker := &configReconcileTestTracker{
		inits:  map[string]int{},
		stops:  map[string]int{},
		values: map[string]string{},
	}
	initial := mustParseReconcileConfig(t, `{
		"nodes": {
			"A": {"Class": "test/ConfigReconcile", "Links": ["Out -> [B] In"], "value": "a"},
			"B": {"Class": "test/ConfigReconcile", "value": "before"},
			"Spare": {"Class": "test/ConfigReconcile", "Links": ["Out -> [Sink] In"], "value": "spare"},
			"Sink": {"Class": "test/ConfigReconcile", "value": "sink"},
		},
	}`)
	engineAPI, err := BuildEngine(
		context.Background(),
		initial,
		logging.NewDebugLogFactory(64),
		newEngineTestDebugSystem(),
		nil, nil, nil, nil,
		[]pasta.NodeClass{configReconcileTestClass{tracker: tracker}},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })

	before := engineAPI.(*engine).mainWorkspace.Snapshot()
	idsBefore := reconcileNodeIDsByName(before)
	spareLinkBefore := reconcileLinkIDByNames(t, before, "Spare", "Sink")

	desired := mustParseReconcileConfig(t, `{
		"nodes": {
			"A": {"Class": "test/ConfigReconcile", "Links": ["Out -> [B] In"], "value": "a"},
			"B": {"Class": "test/ConfigReconcile", "Pos": "edited", "value": "after"},
			"Spare": {"Class": "test/ConfigReconcile", "Links": ["Out -> [Sink] In"], "value": "spare"},
			"Sink": {"Class": "test/ConfigReconcile", "value": "sink"},
		},
	}`)
	result, err := engineAPI.ReconcileConfig(desired)
	if err != nil {
		t.Fatalf("ReconcileConfig: %v", err)
	}
	if !slices.Equal(result.Reinitialized, []string{"B"}) {
		t.Fatalf("reinitialized = %v, want [B]", result.Reinitialized)
	}
	if len(result.LinksAdded) != 1 || len(result.LinksRemoved) != 1 {
		t.Fatalf("link changes = +%v -%v, want only B incident link", result.LinksAdded, result.LinksRemoved)
	}

	after := engineAPI.(*engine).mainWorkspace.Snapshot()
	idsAfter := reconcileNodeIDsByName(after)
	for _, name := range []string{"A", "B", "Spare", "Sink"} {
		if idsAfter[name] != idsBefore[name] {
			t.Fatalf("node %s ID = %d, want preserved %d", name, idsAfter[name], idsBefore[name])
		}
	}
	if got := reconcileLinkIDByNames(t, after, "Spare", "Sink"); got != spareLinkBefore {
		t.Fatalf("unaffected Spare -> Sink link ID = %d, want %d", got, spareLinkBefore)
	}
	if node := after.Nodes[idsAfter["B"]]; node.Position != "edited" || node.Placeholder {
		t.Fatalf("B snapshot = %#v, want live node at edited position", node)
	}

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.stops["B"] != 1 || tracker.inits["B"] != 2 || tracker.values["B"] != "after" {
		t.Fatalf("B lifecycle = stops %d, inits %d, value %q", tracker.stops["B"], tracker.inits["B"], tracker.values["B"])
	}
	for _, name := range []string{"A", "Spare", "Sink"} {
		if tracker.stops[name] != 0 || tracker.inits[name] != 1 {
			t.Fatalf("unaffected %s lifecycle = stops %d, inits %d", name, tracker.stops[name], tracker.inits[name])
		}
	}
}

func TestEngineReconcileConfigRejectsInvalidDocumentsBeforeMutation(t *testing.T) {
	tracker := &configReconcileTestTracker{inits: map[string]int{}, stops: map[string]int{}, values: map[string]string{}}
	initial := mustParseReconcileConfig(t, `{"nodes": {"A": {"Class": "test/ConfigReconcile", "value": "a"}}}`)
	engineAPI, err := BuildEngine(
		context.Background(), initial, logging.NewDebugLogFactory(16), newEngineTestDebugSystem(),
		nil, nil, nil, nil, []pasta.NodeClass{configReconcileTestClass{tracker: tracker}}, nil, false,
	)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	id, _ := engineAPI.NodeByName(MainWorkspace, "A")

	invalidDocuments := map[string]string{
		"link syntax":   `{"nodes": {"A": {"Class": "test/ConfigReconcile", "Links": ["not a link"], "value": "changed"}}}`,
		"links type":    `{"nodes": {"A": {"Class": "test/ConfigReconcile", "Links": "not an array", "value": "changed"}}}`,
		"primary type":  `{"nodes": {"A": {"Class": "test/ConfigReconcile", "Primary": 7, "value": "changed"}}}`,
		"position type": `{"nodes": {"A": {"Class": "test/ConfigReconcile", "Pos": false, "value": "changed"}}}`,
	}
	for name, text := range invalidDocuments {
		t.Run(name, func(t *testing.T) {
			invalid := mustParseReconcileConfig(t, text)
			if _, err := engineAPI.ReconcileConfig(invalid); err == nil {
				t.Fatal("ReconcileConfig accepted invalid document")
			}
		})
	}
	afterID, ok := engineAPI.NodeByName(MainWorkspace, "A")
	if !ok || afterID != id {
		t.Fatalf("A ID after rejected config = %d, %v; want %d", afterID, ok, id)
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.inits["A"] != 1 || tracker.stops["A"] != 0 {
		t.Fatalf("A lifecycle after rejected config = inits %d, stops %d", tracker.inits["A"], tracker.stops["A"])
	}
}

func TestEngineReconcileConfigRemovesLinksToDeletedNodes(t *testing.T) {
	tracker := &configReconcileTestTracker{inits: map[string]int{}, stops: map[string]int{}, values: map[string]string{}}
	initial := mustParseReconcileConfig(t, `{
		"nodes": {
			"A": {"Class": "test/ConfigReconcile", "Links": ["Out -> [B] In"]},
			"B": {"Class": "test/ConfigReconcile"}
		}
	}`)
	engineAPI, err := BuildEngine(
		context.Background(), initial, logging.NewDebugLogFactory(16), newEngineTestDebugSystem(),
		nil, nil, nil, nil, []pasta.NodeClass{configReconcileTestClass{tracker: tracker}}, nil, false,
	)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })

	desired := mustParseReconcileConfig(t, `{
		"nodes": {
			"A": {"Class": "test/ConfigReconcile", "Links": ["Out -> [B] In"]}
		}
	}`)
	result, err := engineAPI.ReconcileConfig(desired)
	if err != nil {
		t.Fatalf("ReconcileConfig: %v", err)
	}
	if !slices.Equal(result.Removed, []string{"B"}) {
		t.Fatalf("removed nodes = %v, want [B]", result.Removed)
	}
	if !slices.Equal(result.LinksRemoved, []string{"Out -> [B] In"}) {
		t.Fatalf("removed links = %v, want stale link", result.LinksRemoved)
	}
	snapshot := engineAPI.(*engine).mainWorkspace.Snapshot()
	if len(snapshot.Nodes) != 1 || len(snapshot.Links) != 0 {
		t.Fatalf("workspace after deletion = %d nodes, %d links; want 1 node and 0 links", len(snapshot.Nodes), len(snapshot.Links))
	}
	if err := engineAPI.SaveConfig(desired); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if _, err := desired.Get(configer.Path{"nodes", "A", "Links"}); !errors.Is(err, configer.ErrNotFound) {
		t.Fatalf("canonical A Links error = %v, want %v", err, configer.ErrNotFound)
	}
}

func TestEngineReconcileConfigSynchronizesNodeCommentPopups(t *testing.T) {
	tracker := &configReconcileTestTracker{inits: map[string]int{}, stops: map[string]int{}, values: map[string]string{}}
	initial := mustParseReconcileConfig(t, `{
		"nodes": {
			// Original config note
			"A": {"Class": "test/ConfigReconcile", "value": ""}
		}
	}`)
	engineAPI, err := BuildEngine(
		context.Background(), initial, logging.NewDebugLogFactory(16), newEngineTestDebugSystem(),
		nil, nil, nil, nil, []pasta.NodeClass{configReconcileTestClass{tracker: tracker}}, nil, false,
	)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	engine := engineAPI.(*engine)
	nodeID, ok := engine.mainWorkspace.NodeIDByName("A")
	if !ok {
		t.Fatal("node A not found")
	}
	if _, err := engine.mainWorkspace.AddNodePopup(nodeID, pasta.NodePopupInfo, "runtime note", false); err != nil {
		t.Fatalf("AddNodePopup: %v", err)
	}

	assertPopups := func(want ...string) {
		t.Helper()
		node, ok := engine.mainWorkspace.NodeSnapshot(nodeID)
		if !ok {
			t.Fatal("node A snapshot not found")
		}
		got := make([]string, 0, len(node.Popups))
		for _, popup := range node.Popups {
			if popup.Type != pasta.NodePopupInfo {
				t.Fatalf("popup = %#v, want info popup", popup)
			}
			got = append(got, popup.Text)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("popup texts = %v, want %v", got, want)
		}
	}
	assertPopups("Original config note", "runtime note")

	changed := mustParseReconcileConfig(t, `{
		"nodes": {
			// Changed config note
			"A": {"Class": "test/ConfigReconcile", "value": ""}
		}
	}`)
	if _, err := engineAPI.ReconcileConfig(changed); err != nil {
		t.Fatalf("ReconcileConfig changed comment: %v", err)
	}
	assertPopups("Changed config note", "runtime note")

	removed := mustParseReconcileConfig(t, `{"nodes":{"A":{"Class":"test/ConfigReconcile","value":""}}}`)
	if _, err := engineAPI.ReconcileConfig(removed); err != nil {
		t.Fatalf("ReconcileConfig removed comment: %v", err)
	}
	assertPopups("runtime note")

	added := mustParseReconcileConfig(t, `{
		"nodes": {
			// Added config note
			"A": {"Class": "test/ConfigReconcile", "value": ""}
		}
	}`)
	if _, err := engineAPI.ReconcileConfig(added); err != nil {
		t.Fatalf("ReconcileConfig added comment: %v", err)
	}
	assertPopups("Added config note", "runtime note")

	rebuilt := mustParseReconcileConfig(t, `{
		"nodes": {
			// Added config note
			"A": {"Class": "test/ConfigReconcile", "value": "reinitialized"}
		}
	}`)
	result, err := engineAPI.ReconcileConfig(rebuilt)
	if err != nil {
		t.Fatalf("ReconcileConfig with unchanged comment and rebuilt node: %v", err)
	}
	if !slices.Equal(result.Reinitialized, []string{"A"}) {
		t.Fatalf("reinitialized nodes = %v, want [A]", result.Reinitialized)
	}
	assertPopups("Added config note")
}

func mustParseReconcileConfig(t *testing.T, text string) *hujson.Config {
	t.Helper()
	cfg, err := hujson.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	return cfg
}

func reconcileNodeIDsByName(snapshot pasta.WorkspaceSnapshot) map[string]uint64 {
	out := make(map[string]uint64, len(snapshot.Nodes))
	for id, node := range snapshot.Nodes {
		out[node.Name] = id
	}
	return out
}

func reconcileLinkIDByNames(t *testing.T, snapshot pasta.WorkspaceSnapshot, source, target string) uint64 {
	t.Helper()
	for id, link := range snapshot.Links {
		if snapshot.Nodes[link.RightPortNode].Name == source && snapshot.Nodes[link.LeftPortNode].Name == target {
			return id
		}
	}
	t.Fatalf("link %s -> %s not found", source, target)
	return 0
}
