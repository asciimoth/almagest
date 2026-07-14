package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

type compactWorkspace struct {
	Nodes map[string]compactNode `json:"nodes"`
}

type compactNode struct {
	ID    uint64   `json:"ID"`
	Class string   `json:"Class"`
	Links []string `json:"Links,omitempty"`
}

func runGraph(ctx context.Context, c *client, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printGraphHelp(stdout)
		return nil
	}
	switch args[0] {
	case "map":
		workspace, err := parseWorkspace("graph map", args[1:], stderr)
		if err != nil {
			return err
		}
		snapshot, err := fetchWorkspaceSnapshot(ctx, c, workspace)
		if err != nil {
			return err
		}
		return printPretty(stdout, compactFromWorkspace(snapshot))
	case "node":
		workspace, id, err := parseGraphID("graph node", args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineNodeSnapshot), api.IDRequest{Workspace: workspace, ID: id}, stdout)
	case "delete-node":
		workspace, id, err := parseGraphID("graph delete-node", args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineRemoveNode), api.IDRequest{Workspace: workspace, ID: id}, stdout)
	case "delete-nodes":
		fs := commandFlagSet("graph delete-nodes", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		ids := fs.String("ids", "", "comma-separated node IDs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		parsed, err := parseUintList(*ids)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineRemoveNodes), api.IDsRequest{Workspace: *workspace, IDs: parsed}, stdout)
	case "add-link":
		fs := commandFlagSet("graph add-link", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		portA := fs.Uint64("port-a", 0, "first port ID")
		portB := fs.Uint64("port-b", 0, "second port ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *portA == 0 || *portB == 0 {
			return fmt.Errorf("--port-a and --port-b are required")
		}
		payload := api.AddLinkRequest{Workspace: *workspace, PortA: *portA, PortB: *portB}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineAddLink), payload, stdout)
	case "delete-link":
		workspace, id, err := parseGraphID("graph delete-link", args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineRemoveLink), api.IDRequest{Workspace: workspace, ID: id}, stdout)
	case "menu":
		workspace, node, err := parseGraphNode("graph menu", args[1:], stderr)
		if err != nil {
			return err
		}
		menu, err := fetchNodeMenu(ctx, c, workspace, node)
		if err != nil {
			return err
		}
		return printPretty(stdout, menu)
	case "apply-menu":
		fs := commandFlagSet("graph apply-menu", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		node := fs.Uint64("node", 0, "node ID")
		input := fs.String("json", "", "desired menu state JSON, keyed by block ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *node == 0 {
			return fmt.Errorf("--node is required")
		}
		raw, err := readJSONInput(*input, stdin)
		if err != nil {
			return err
		}
		var desired map[string]map[string]any
		if err := json.Unmarshal(raw, &desired); err != nil {
			return fmt.Errorf("decode desired menu state: %w", err)
		}
		return applyMenuState(ctx, c, *workspace, *node, desired, stdout)
	case "click-menu-button":
		fs := commandFlagSet("graph click-menu-button", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		node := fs.Uint64("node", 0, "node ID")
		blockID := fs.String("block", "", "block ID")
		buttonID := fs.String("button", "", "button ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *node == 0 || *blockID == "" || *buttonID == "" {
			return fmt.Errorf("--node, --block, and --button are required")
		}
		msg := formular.ButtonPressMessage{
			MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: nodeMenuID(*node)},
			BlockID:     *blockID,
			ButtonID:    *buttonID,
		}
		return sendFormularMessage(ctx, c, *workspace, *node, msg, stdout)
	case "class-description":
		workspace, class, err := parseClass("graph class-description", args[1:], stderr)
		if err != nil {
			return err
		}
		payload := api.ClassRequest{Workspace: workspace, Class: class}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineNodeClassLongDescription), payload, stdout)
	case "classes":
		workspace, err := parseWorkspace("graph classes", args[1:], stderr)
		if err != nil {
			return err
		}
		snapshot, err := fetchWorkspaceSnapshot(ctx, c, workspace)
		if err != nil {
			return err
		}
		classes := make([]pasta.NodeClassSnapshot, 0, len(snapshot.Classes))
		for _, cls := range snapshot.Classes {
			classes = append(classes, cls)
		}
		sort.Slice(classes, func(i, j int) bool { return classes[i].Class < classes[j].Class })
		return printPretty(stdout, map[string]any{"classes": classes})
	default:
		return fmt.Errorf("unknown graph command %q", args[0])
	}
}

func fetchWorkspaceSnapshot(ctx context.Context, c *client, workspace string) (pasta.WorkspaceSnapshot, error) {
	raw, err := c.request(ctx, api.SurfaceEngine, string(api.MethodEngineWorkspaceSnapshot), api.WorkspaceRequest{Workspace: workspace})
	if err != nil {
		return pasta.WorkspaceSnapshot{}, err
	}
	resp, err := api.DecodePayload[api.WorkspaceSnapshotResponse](raw)
	if err != nil {
		return pasta.WorkspaceSnapshot{}, err
	}
	return resp.Snapshot, nil
}

func compactFromWorkspace(snapshot pasta.WorkspaceSnapshot) compactWorkspace {
	nodeIDs := make([]uint64, 0, len(snapshot.Nodes))
	for id := range snapshot.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	out := compactWorkspace{Nodes: make(map[string]compactNode, len(nodeIDs))}
	for _, id := range nodeIDs {
		node := snapshot.Nodes[id]
		out.Nodes[node.Name] = compactNode{ID: id, Class: node.Class}
	}
	for _, link := range snapshot.Links {
		sourceNode := snapshot.Nodes[link.RightPortNode]
		targetNode := snapshot.Nodes[link.LeftPortNode]
		sourcePort := snapshot.Ports[link.RightPort]
		targetPort := snapshot.Ports[link.LeftPort]
		if sourceNode.Name == "" || targetNode.Name == "" {
			continue
		}
		node := out.Nodes[sourceNode.Name]
		node.Links = append(node.Links, fmt.Sprintf("%s -> [%s] %s", sourcePort.Name, targetNode.Name, targetPort.Name))
		sort.Strings(node.Links)
		out.Nodes[sourceNode.Name] = node
	}
	return out
}

func fetchNodeMenu(ctx context.Context, c *client, workspace string, node uint64) (formular.MenuSnapshotMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	raw, err := c.request(ctx, api.SurfaceEngine, string(api.MethodEngineSubscribeNotifications), api.WorkspaceRequest{Workspace: workspace})
	if err != nil {
		return formular.MenuSnapshotMessage{}, err
	}
	sub, err := api.DecodePayload[api.SubscribeNotificationsResponse](raw)
	if err != nil {
		return formular.MenuSnapshotMessage{}, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = c.request(ctx, api.SurfaceEngine, string(api.MethodEngineUnsubscribeNotifications), api.SubscriptionRequest(sub))
	}()

	req := api.NodeMenuSubscriptionRequest{Workspace: workspace, Node: node, SubscriptionID: sub.SubscriptionID}
	if _, err := c.request(ctx, api.SurfaceEngine, string(api.MethodEngineSubscribeNodeMenu), req); err != nil {
		return formular.MenuSnapshotMessage{}, err
	}

	state := formular.NewMenuSnapshotState()
	menuID := nodeMenuID(node)
	for {
		event, err := c.nextEvent(ctx)
		if err != nil {
			return formular.MenuSnapshotMessage{}, err
		}
		var engineEvent api.EngineNotificationEvent
		if event.Surface != api.SurfaceEngine || event.Method != string(api.EventEngineNotification) {
			continue
		}
		if err := json.Unmarshal(event.Payload, &engineEvent); err != nil {
			return formular.MenuSnapshotMessage{}, err
		}
		notification := engineEvent.Notification.Workspace
		if notification == nil || notification.ID != node || notification.Formular == nil {
			continue
		}
		state.Apply(notification.Formular)
		if snapshot, ok := state.Snapshot(menuID); ok {
			return snapshot, nil
		}
	}
}

func applyMenuState(ctx context.Context, c *client, workspace string, node uint64, desired map[string]map[string]any, stdout io.Writer) error {
	menu, err := fetchNodeMenu(ctx, c, workspace, node)
	if err != nil {
		return err
	}
	applied := make([]string, 0, len(desired))
	for _, block := range menu.Blocks {
		values, ok := desired[block.ID]
		if !ok {
			continue
		}
		if !block.Form {
			return fmt.Errorf("block %q is not a form block", block.ID)
		}
		msg := formular.FormApplyMessage{
			MessageBase: formular.MessageBase{
				Type:            formular.MessageFormApply,
				MenuID:          menu.MenuID,
				MenuGeneration:  menu.MenuGeneration,
				BlockGeneration: block.Generation,
			},
			BlockID: block.ID,
			Values:  values,
		}
		if _, err := c.request(ctx, api.SurfaceEngine, string(api.MethodEngineSendNodeFormularMessage), api.SendNodeFormularMessageRequest{
			Workspace: workspace,
			Node:      node,
			Message:   mustRawJSON(msg),
		}); err != nil {
			return err
		}
		applied = append(applied, block.ID)
	}
	for blockID := range desired {
		if !containsString(applied, blockID) {
			return fmt.Errorf("menu block %q was not found", blockID)
		}
	}
	return printPretty(stdout, map[string]any{"applied_blocks": applied})
}

func sendFormularMessage(ctx context.Context, c *client, workspace string, node uint64, message any, stdout io.Writer) error {
	return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineSendNodeFormularMessage), api.SendNodeFormularMessageRequest{
		Workspace: workspace,
		Node:      node,
		Message:   mustRawJSON(message),
	}, stdout)
}

func parseGraphID(name string, args []string, stderr io.Writer) (string, uint64, error) {
	return parseID(name, args, stderr)
}

func parseGraphNode(name string, args []string, stderr io.Writer) (string, uint64, error) {
	fs := commandFlagSet(name, stderr)
	workspace := fs.String("workspace", "main", "workspace name")
	node := fs.Uint64("node", 0, "node ID")
	if err := fs.Parse(args); err != nil {
		return "", 0, err
	}
	if *node == 0 {
		return "", 0, fmt.Errorf("--node is required")
	}
	return *workspace, *node, nil
}

func readJSONInput(value string, stdin io.Reader) (json.RawMessage, error) {
	if value == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		value = string(data)
	}
	raw := json.RawMessage(value)
	if !json.Valid(raw) {
		return nil, fmt.Errorf("input must be valid JSON")
	}
	return raw, nil
}

func mustRawJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func nodeMenuID(node uint64) string {
	return pasta.NodeMenuID(node)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func printPretty(stdout io.Writer, value any) error {
	return writePrettyJSON(stdout, value)
}

func writePrettyJSON(stdout io.Writer, value any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return err
	}
	_, err := stdout.Write(buf.Bytes())
	return err
}

func printGraphHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli graph <command> [flags]

Commands:
  map                 compact node map: ids, classes, config-format links
  node                fetch one node snapshot with all node metadata
  delete-node         remove a node by --id
  delete-nodes        remove multiple nodes by --ids
  add-link            create a link from --port-a to --port-b
  delete-link         remove a link by --id
  menu                fetch one node's current Formular menu snapshot
  apply-menu          apply desired form block values from --json or stdin
  click-menu-button   press one button in a node menu
  class-description   fetch one node class long description
  classes             list node classes without long descriptions

Examples:
  almagest-cli graph map
  almagest-cli graph node --id 12
  almagest-cli graph add-link --port-a 4 --port-b 9
  almagest-cli graph menu --node 12
  almagest-cli graph apply-menu --node 12 --json '{"config":{"host":"127.0.0.1","port":8080}}'
  almagest-cli graph click-menu-button --node 12 --block request --button send
`)
}
