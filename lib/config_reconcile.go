package almagest

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

const (
	configNodeClassKey    = "Class"
	configNodePrimaryKey  = "Primary"
	configNodePositionKey = "Pos"
	configNodeLinksKey    = "Links"
)

// ConfigReconcileResult describes the live graph work caused by a config
// update. Names are sorted so API clients and tests receive stable output.
type ConfigReconcileResult struct {
	Added               []string `json:"added,omitempty"`
	Removed             []string `json:"removed,omitempty"`
	Reinitialized       []string `json:"reinitialized,omitempty"`
	PositionsUpdated    []string `json:"positions_updated,omitempty"`
	PrimaryTypesUpdated []string `json:"primary_types_updated,omitempty"`
	LinksAdded          []string `json:"links_added,omitempty"`
	LinksRemoved        []string `json:"links_removed,omitempty"`
}

type configReconcileNode struct {
	name       string
	class      string
	comment    string
	primary    string
	position   string
	object     map[string]any
	config     configer.Config
	links      []configReconcileLink
	leftPorts  map[string]struct{}
	rightPorts map[string]struct{}
}

type configReconcileLink struct {
	sourceNode string
	sourcePort string
	targetNode string
	targetPort string
	raw        string
}

type configCommentPopup struct {
	text string
	id   uint64
}

func (l configReconcileLink) key() string {
	return strings.Join([]string{l.sourceNode, l.sourcePort, l.targetNode, l.targetPort}, "\x00")
}

// ReconcileConfig applies only config changes that affect the live main
// workspace. It keeps unchanged node and link records intact. A node whose
// node-owned config changes is temporarily changed to a placeholder and then
// reconstructed by its registered class. This makes Pasta deliver the current
// ports and link lifecycle again without rebuilding the complete engine.
func (e *engine) ReconcileConfig(cfg configer.Config) (ConfigReconcileResult, error) {
	e.configReconcileMu.Lock()
	defer e.configReconcileMu.Unlock()

	if cfg == nil {
		return ConfigReconcileResult{}, errors.New("nil config")
	}
	if e.mainWorkspace == nil {
		return ConfigReconcileResult{}, errors.New("main workspace is not available")
	}

	desiredRoot := cfg.View(configer.Path{"nodes"})
	desired, desiredLinks, err := parseConfigReconcileNodes(desiredRoot)
	if err != nil {
		return ConfigReconcileResult{}, err
	}
	if err := validateConfigReconcileClasses(e.mainWorkspace, desired); err != nil {
		return ConfigReconcileResult{}, err
	}

	currentRoot := configer.NewMemory(nil)
	if err := e.mainWorkspace.SaveConfig(currentRoot); err != nil {
		return ConfigReconcileResult{}, fmt.Errorf("save current workspace config: %w", err)
	}
	current, _, err := parseConfigReconcileNodes(currentRoot)
	if err != nil {
		return ConfigReconcileResult{}, fmt.Errorf("read current workspace config: %w", err)
	}

	result := ConfigReconcileResult{}
	rebuild := make(map[string]struct{})
	remove := make(map[string]struct{})
	add := make(map[string]struct{})

	for name, oldNode := range current {
		newNode, present := desired[name]
		if !present {
			remove[name] = struct{}{}
			result.Removed = append(result.Removed, name)
			continue
		}
		if oldNode.class != newNode.class {
			remove[name] = struct{}{}
			add[name] = struct{}{}
			result.Removed = append(result.Removed, name)
			result.Added = append(result.Added, name)
			continue
		}
		if !reflect.DeepEqual(nodeOwnedConfig(oldNode.object), nodeOwnedConfig(newNode.object)) {
			rebuild[name] = struct{}{}
			result.Reinitialized = append(result.Reinitialized, name)
		}
	}
	for name := range desired {
		if _, present := current[name]; !present {
			add[name] = struct{}{}
			result.Added = append(result.Added, name)
		}
	}

	affected := make(map[string]struct{}, len(rebuild)+len(remove)+len(add))
	for name := range rebuild {
		affected[name] = struct{}{}
	}
	for name := range remove {
		affected[name] = struct{}{}
	}
	for name := range add {
		affected[name] = struct{}{}
	}

	snapshot := e.mainWorkspace.Snapshot()
	currentLinks := workspaceConfigLinks(snapshot)
	linksToRemove := make([]uint64, 0)
	for key, currentLink := range currentLinks {
		_, wanted := desiredLinks[key]
		_, sourceAffected := affected[currentLink.link.sourceNode]
		_, targetAffected := affected[currentLink.link.targetNode]
		if wanted && !sourceAffected && !targetAffected {
			continue
		}
		linksToRemove = append(linksToRemove, currentLink.id)
		result.LinksRemoved = append(result.LinksRemoved, currentLink.link.raw)
	}
	slices.Sort(linksToRemove)
	for _, id := range linksToRemove {
		e.mainWorkspace.RemoveLink(id)
	}

	for _, name := range sortedSet(remove) {
		id, ok := e.mainWorkspace.NodeIDByName(name)
		if ok {
			e.mainWorkspace.RemoveNode(id)
		}
	}

	configsByClass := make(map[string]map[string]configer.Config)
	for _, name := range sortedSet(rebuild) {
		id, ok := e.mainWorkspace.NodeIDByName(name)
		if !ok {
			return result, fmt.Errorf("reinitialize node %q: node not found", name)
		}
		if err := e.mainWorkspace.ReplaceNodeWithPlaceholder(id, nil); err != nil {
			return result, fmt.Errorf("reinitialize node %q: %w", name, err)
		}
		addReconcileClassConfig(configsByClass, desired[name])
	}

	for _, name := range sortedSet(add) {
		node := desired[name]
		class, classPresent := e.mainWorkspace.NodeClass(node.class)
		ports := configReconcileInitialPorts(class, classPresent, node)
		root := false
		if classPresent {
			root = class.DefaultNodeParams().Root
		}
		id, err := e.mainWorkspace.AddPlaceholderNodeWithRoot(node.class, root, ports, name)
		if err != nil {
			return result, fmt.Errorf("add node %q: %w", name, err)
		}
		if node.primary != "" {
			if err := e.mainWorkspace.SetNodePrimary(id, node.primary); err != nil {
				return result, fmt.Errorf("set node %q primary type: %w", name, err)
			}
		}
		if node.position != "" {
			if err := e.mainWorkspace.SetNodePosition(id, node.position); err != nil {
				return result, fmt.Errorf("set node %q position: %w", name, err)
			}
		}
		addReconcileClassConfig(configsByClass, node)
	}

	for _, className := range sortedConfigClasses(configsByClass) {
		class, ok := e.mainWorkspace.NodeClass(className)
		if !ok {
			// Unknown classes intentionally stay as placeholders, matching startup
			// restore behavior.
			continue
		}
		factory, ok := class.(pasta.NodeClassFactory)
		if !ok {
			continue
		}
		if err := e.mainWorkspace.RemoveNodeClass(className); err != nil {
			return result, fmt.Errorf("prepare class %q for config apply: %w", className, err)
		}
		wrapper := configReconcileNodeClass{
			NodeClass: class,
			factory:   factory,
			configs:   configsByClass[className],
		}
		applyErr := e.mainWorkspace.AddNodeClass(wrapper)
		restoreErr := e.mainWorkspace.AddNodeClass(class)
		if applyErr != nil {
			return result, fmt.Errorf("apply class %q config: %w", className, applyErr)
		}
		if restoreErr != nil {
			return result, fmt.Errorf("restore class %q after config apply: %w", className, restoreErr)
		}
	}

	for _, name := range sortedConfigNodeNames(desired) {
		node := desired[name]
		id, ok := e.mainWorkspace.NodeIDByName(name)
		if !ok {
			continue
		}
		nodeSnapshot, ok := e.mainWorkspace.NodeSnapshot(id)
		if !ok {
			continue
		}
		if nodeSnapshot.Position != node.position {
			if err := e.mainWorkspace.SetNodePosition(id, node.position); err != nil {
				return result, fmt.Errorf("set node %q position: %w", name, err)
			}
			result.PositionsUpdated = append(result.PositionsUpdated, name)
		}
		if nodeSnapshot.PrimaryType != node.primary {
			if err := e.mainWorkspace.SetNodePrimary(id, node.primary); err != nil {
				return result, fmt.Errorf("set node %q primary type: %w", name, err)
			}
			result.PrimaryTypesUpdated = append(result.PrimaryTypesUpdated, name)
		}
	}

	remainingLinks := workspaceConfigLinks(e.mainWorkspace.Snapshot())
	for _, key := range sortedConfigLinkKeys(desiredLinks) {
		if _, present := remainingLinks[key]; present {
			continue
		}
		link := desiredLinks[key]
		portA, portB, err := configReconcileLinkPorts(e.mainWorkspace.Snapshot(), link)
		if err != nil {
			return result, err
		}
		if _, _, err := e.mainWorkspace.AddLink(portA, portB); err != nil {
			return result, fmt.Errorf("add link %q: %w", link.raw, err)
		}
		result.LinksAdded = append(result.LinksAdded, link.raw)
	}

	if err := e.reconcileConfigCommentPopups(desired, affected); err != nil {
		return result, err
	}

	sortConfigReconcileResult(&result)
	return result, nil
}

type configReconcileNodeClass struct {
	pasta.NodeClass
	factory pasta.NodeClassFactory
	configs map[string]configer.Config
}

func (c configReconcileNodeClass) NewNode(_ configer.Config, previous ...*pasta.NodeClassState) (pasta.Node, error) {
	if len(previous) == 0 || previous[0] == nil {
		return nil, nil
	}
	cfg, ok := c.configs[previous[0].Name]
	if !ok {
		// Do not revive unrelated failed placeholders when the class is
		// temporarily re-registered for an edited node.
		return nil, nil
	}
	return c.factory.NewNode(cfg, previous...)
}

func parseConfigReconcileNodes(cfg configer.Config) (map[string]*configReconcileNode, map[string]configReconcileLink, error) {
	nodes := map[string]*configReconcileNode{}
	links := map[string]configReconcileLink{}
	parsedLinks := []configReconcileLink{}
	if cfg == nil {
		return nodes, links, nil
	}
	raw, err := cfg.Get(nil)
	if err != nil {
		if errors.Is(err, configer.ErrNotFound) {
			return nodes, links, nil
		}
		return nil, nil, err
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New("config nodes must be an object")
	}
	for name, rawNode := range root {
		if err := pasta.ValidateNodeName(name); err != nil {
			return nil, nil, fmt.Errorf("invalid node name %q: %w", name, err)
		}
		object, ok := rawNode.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("node %q config must be an object", name)
		}
		class, ok := object[configNodeClassKey].(string)
		if !ok || class == "" {
			return nil, nil, fmt.Errorf("node %q Class must be a non-empty string", name)
		}
		if err := pasta.ValidateClassName(class); err != nil {
			return nil, nil, fmt.Errorf("node %q Class: %w", name, err)
		}
		primary, primaryOK := object[configNodePrimaryKey].(string)
		if _, present := object[configNodePrimaryKey]; present && !primaryOK {
			return nil, nil, fmt.Errorf("node %q Primary must be a string", name)
		}
		if primary != "" {
			if err := pasta.ValidateTypeName(primary); err != nil {
				return nil, nil, fmt.Errorf("node %q Primary: %w", name, err)
			}
		}
		position, positionOK := object[configNodePositionKey].(string)
		if _, present := object[configNodePositionKey]; present && !positionOK {
			return nil, nil, fmt.Errorf("node %q Pos must be a string", name)
		}
		node := &configReconcileNode{
			name:       name,
			class:      class,
			primary:    primary,
			position:   position,
			object:     object,
			config:     cfg.View(configer.Path{name}),
			leftPorts:  map[string]struct{}{},
			rightPorts: map[string]struct{}{},
		}
		if rawLinks, present := object[configNodeLinksKey]; present {
			list, ok := rawLinks.([]any)
			if !ok {
				return nil, nil, fmt.Errorf("node %q Links must be an array of strings", name)
			}
			for _, rawLink := range list {
				text, ok := rawLink.(string)
				if !ok {
					return nil, nil, fmt.Errorf("node %q Links must contain only strings", name)
				}
				link, err := parseConfigReconcileLink(name, text)
				if err != nil {
					return nil, nil, err
				}
				node.links = append(node.links, link)
				parsedLinks = append(parsedLinks, link)
			}
		}
		nodes[name] = node
	}
	restoreConfigReconcileComments(cfg, nodes)
	for _, link := range parsedLinks {
		source := nodes[link.sourceNode]
		target := nodes[link.targetNode]
		if source == nil {
			return nil, nil, fmt.Errorf("link %q source node does not exist", link.raw)
		}
		if target == nil {
			// A complete config update can remove a node before callers remove
			// stale link entries that point to it. Match workspace restore behavior:
			// keep the node document and omit the dangling link. SaveConfig removes
			// the stale entry from the canonical document after reconciliation.
			continue
		}
		if _, duplicate := links[link.key()]; duplicate {
			return nil, nil, fmt.Errorf("duplicate link %q", link.raw)
		}
		links[link.key()] = link
		source.rightPorts[link.sourcePort] = struct{}{}
		target.leftPorts[link.targetPort] = struct{}{}
	}
	return nodes, links, nil
}

func restoreConfigReconcileComments(cfg configer.Config, nodes map[string]*configReconcileNode) {
	commenter, ok := cfg.(configer.Commenter)
	if !ok {
		return
	}
	for name, node := range nodes {
		comment, err := commenter.GetComment(configer.Path{name})
		if err == nil {
			node.comment = comment
		}
	}
}

func initialConfigCommentPopups(cfg configer.Config, snapshot pasta.WorkspaceSnapshot) map[string]configCommentPopup {
	tracked := make(map[string]configCommentPopup)
	commenter, ok := cfg.(configer.Commenter)
	if !ok {
		return tracked
	}
	for _, node := range snapshot.Nodes {
		comment, err := commenter.GetComment(configer.Path{node.Name})
		if err != nil || comment == "" {
			continue
		}
		popupID := uint64(0)
		for _, popup := range node.Popups {
			if popup.Type == pasta.NodePopupInfo && popup.Text == comment {
				popupID = popup.ID
			}
		}
		tracked[node.Name] = configCommentPopup{text: comment, id: popupID}
	}
	return tracked
}

func (e *engine) reconcileConfigCommentPopups(
	desired map[string]*configReconcileNode,
	affected map[string]struct{},
) error {
	if e.configCommentPopups == nil {
		e.configCommentPopups = make(map[string]configCommentPopup)
	}
	for name := range e.configCommentPopups {
		if _, present := desired[name]; !present {
			delete(e.configCommentPopups, name)
		}
	}
	for _, name := range sortedConfigNodeNames(desired) {
		node := desired[name]
		previous, tracked := e.configCommentPopups[name]
		_, rebuilt := affected[name]
		if tracked && previous.text == node.comment && !rebuilt {
			continue
		}
		delete(e.configCommentPopups, name)

		id, present := e.mainWorkspace.NodeIDByName(name)
		if !present {
			continue
		}
		if tracked && previous.id != 0 && !rebuilt {
			if err := e.mainWorkspace.RemoveNodePopup(id, previous.id); err != nil {
				return fmt.Errorf("remove node %q config comment popup: %w", name, err)
			}
		}
		if node.comment == "" {
			continue
		}
		popupID, err := e.mainWorkspace.AddNodePopup(id, pasta.NodePopupInfo, node.comment, true)
		if err != nil {
			return fmt.Errorf("add node %q config comment popup: %w", name, err)
		}
		e.configCommentPopups[name] = configCommentPopup{text: node.comment, id: popupID}
	}
	return nil
}

func parseConfigReconcileLink(sourceNode, raw string) (configReconcileLink, error) {
	sourcePort, rest, ok := strings.Cut(raw, " -> [")
	if !ok {
		return configReconcileLink{}, fmt.Errorf("node %q has invalid link %q", sourceNode, raw)
	}
	targetNode, targetPort, ok := strings.Cut(rest, "] ")
	if !ok || sourcePort == "" || targetNode == "" || targetPort == "" {
		return configReconcileLink{}, fmt.Errorf("node %q has invalid link %q", sourceNode, raw)
	}
	if err := pasta.ValidatePortName(sourcePort); err != nil {
		return configReconcileLink{}, fmt.Errorf("link %q source port: %w", raw, err)
	}
	if err := pasta.ValidateNodeName(targetNode); err != nil {
		return configReconcileLink{}, fmt.Errorf("link %q target node: %w", raw, err)
	}
	if err := pasta.ValidatePortName(targetPort); err != nil {
		return configReconcileLink{}, fmt.Errorf("link %q target port: %w", raw, err)
	}
	return configReconcileLink{
		sourceNode: sourceNode,
		sourcePort: sourcePort,
		targetNode: targetNode,
		targetPort: targetPort,
		raw:        raw,
	}, nil
}

func validateConfigReconcileClasses(workspace *pasta.Workspace, nodes map[string]*configReconcileNode) error {
	unique := map[string]string{}
	for _, node := range nodes {
		class, ok := workspace.NodeClass(node.class)
		if !ok {
			continue
		}
		params := class.DefaultNodeParams()
		if node.primary == "" {
			node.primary = params.PrimaryType
		}
		if !params.Unique {
			continue
		}
		if previous, duplicate := unique[node.class]; duplicate {
			return fmt.Errorf("unique node class %q is used by %q and %q", node.class, previous, node.name)
		}
		unique[node.class] = node.name
	}
	return nil
}

func nodeOwnedConfig(object map[string]any) map[string]any {
	copy := make(map[string]any, len(object))
	for key, value := range object {
		switch key {
		case configNodeClassKey, configNodePrimaryKey, configNodePositionKey, configNodeLinksKey:
			continue
		default:
			copy[key] = value
		}
	}
	return copy
}

type currentConfigLink struct {
	id   uint64
	link configReconcileLink
}

func workspaceConfigLinks(snapshot pasta.WorkspaceSnapshot) map[string]currentConfigLink {
	out := make(map[string]currentConfigLink, len(snapshot.Links))
	for id, link := range snapshot.Links {
		sourceNode, sourceOK := snapshot.Nodes[link.RightPortNode]
		targetNode, targetOK := snapshot.Nodes[link.LeftPortNode]
		sourcePort, sourcePortOK := snapshot.Ports[link.RightPort]
		targetPort, targetPortOK := snapshot.Ports[link.LeftPort]
		if !sourceOK || !targetOK || !sourcePortOK || !targetPortOK {
			continue
		}
		configLink := configReconcileLink{
			sourceNode: sourceNode.Name,
			sourcePort: sourcePort.Name,
			targetNode: targetNode.Name,
			targetPort: targetPort.Name,
			raw:        fmt.Sprintf("%s -> [%s] %s", sourcePort.Name, targetNode.Name, targetPort.Name),
		}
		out[configLink.key()] = currentConfigLink{id: id, link: configLink}
	}
	return out
}

func configReconcileInitialPorts(class pasta.NodeClass, present bool, node *configReconcileNode) []pasta.Port {
	ports := []pasta.Port{}
	seen := map[string]map[string]struct{}{
		"left":  {},
		"right": {},
	}
	if present {
		for _, port := range class.DefaultNodeParams().InitialPorts {
			port = port.Copy()
			port.ID = 0
			port.Node = 0
			port.Links = nil
			ports = append(ports, port)
			seen[port.Direction][port.Name] = struct{}{}
		}
	}
	addGuessed := func(direction string, names map[string]struct{}) {
		sorted := make([]string, 0, len(names))
		for name := range names {
			if _, exists := seen[direction][name]; !exists {
				sorted = append(sorted, name)
			}
		}
		slices.Sort(sorted)
		for _, name := range sorted {
			ports = append(ports, pasta.Port{Direction: direction, Name: name, Types: []string{pasta.AnyType}})
		}
	}
	addGuessed("left", node.leftPorts)
	addGuessed("right", node.rightPorts)
	return ports
}

func configReconcileLinkPorts(snapshot pasta.WorkspaceSnapshot, link configReconcileLink) (uint64, uint64, error) {
	var sourceID, targetID uint64
	for id, node := range snapshot.Nodes {
		switch node.Name {
		case link.sourceNode:
			sourceID = id
		case link.targetNode:
			targetID = id
		}
	}
	if sourceID == 0 || targetID == 0 {
		return 0, 0, fmt.Errorf("add link %q: endpoint node is not available", link.raw)
	}
	var sourcePort, targetPort uint64
	for id, port := range snapshot.Ports {
		if port.Node == sourceID && port.Direction == "right" && port.Name == link.sourcePort {
			sourcePort = id
		}
		if port.Node == targetID && port.Direction == "left" && port.Name == link.targetPort {
			targetPort = id
		}
	}
	if sourcePort == 0 || targetPort == 0 {
		return 0, 0, fmt.Errorf("add link %q: endpoint port is not available", link.raw)
	}
	return sourcePort, targetPort, nil
}

func addReconcileClassConfig(byClass map[string]map[string]configer.Config, node *configReconcileNode) {
	if byClass[node.class] == nil {
		byClass[node.class] = map[string]configer.Config{}
	}
	byClass[node.class][node.name] = node.config
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func sortedConfigClasses(values map[string]map[string]configer.Config) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func sortedConfigNodeNames(values map[string]*configReconcileNode) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func sortedConfigLinkKeys(values map[string]configReconcileLink) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func sortConfigReconcileResult(result *ConfigReconcileResult) {
	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	sort.Strings(result.Reinitialized)
	sort.Strings(result.PositionsUpdated)
	sort.Strings(result.PrimaryTypesUpdated)
	sort.Strings(result.LinksAdded)
	sort.Strings(result.LinksRemoved)
}
