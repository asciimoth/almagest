package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/asciimoth/almagest/bin/api"
)

var errHelp = errors.New("help requested")

const apiURLEnv = "ALMAGEST_API_URL"

type globalOptions struct {
	endpoint string
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	dial dialFunc,
) error {
	opts, rest, err := parseGlobal(args, stderr)
	if err != nil {
		if errors.Is(err, errHelp) {
			printTopHelp(stdout)
			return nil
		}
		return err
	}
	if len(rest) == 0 {
		printTopHelp(stdout)
		return nil
	}
	if rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		printTopHelp(stdout)
		return nil
	}

	ep, err := parseEndpoint(opts.endpoint)
	if err != nil {
		return err
	}
	transport, err := dial(ctx, ep)
	if err != nil {
		return err
	}
	defer transport.Close() //nolint:errcheck
	c := newClient(transport)

	var runErr error
	switch rest[0] {
	case "graph":
		runErr = runGraph(ctx, c, rest[1:], stdin, stdout, stderr)
	case "workers":
		runErr = runWorkers(ctx, c, rest[1:], stdout, stderr)
	case "admin":
		runErr = runAdmin(ctx, c, rest[1:], stdout, stderr)
	case "dns":
		runErr = runDNS(ctx, c, rest[1:], stdout, stderr)
	case "docs":
		runErr = runDocs(ctx, c, rest[1:], stdout, stderr)
	case "config":
		runErr = runConfig(ctx, c, rest[1:], stdin, stdout, stderr)
	case "engine":
		runErr = runEngine(ctx, c, rest[1:], stdin, stdout, stderr)
	case "kv":
		runErr = runKV(ctx, c, rest[1:], stdout, stderr)
	case "log":
		runErr = runLog(ctx, c, rest[1:], stdout, stderr)
	case "secrets":
		runErr = runSecrets(ctx, c, rest[1:], stdout, stderr)
	case "snapshots":
		runErr = runSnapshots(ctx, c, rest[1:], stdin, stdout, stderr)
	case "tls":
		runErr = runTLS(ctx, c, rest[1:], stdin, stdout, stderr)
	case "raw":
		runErr = runRaw(ctx, c, rest[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run almagest-cli --help", rest[0])
	}
	if errors.Is(runErr, flag.ErrHelp) || errors.Is(runErr, errHelp) {
		return nil
	}
	return runErr
}

func parseGlobal(args []string, stderr io.Writer) (globalOptions, []string, error) {
	endpoint := os.Getenv(apiURLEnv)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	opts := globalOptions{endpoint: endpoint}
	fs := flag.NewFlagSet("almagest-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.endpoint, "url", endpoint, "admin API endpoint URL")
	fs.StringVar(&opts.endpoint, "u", endpoint, "admin API endpoint URL")
	fs.Usage = func() { printTopHelp(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, nil, errHelp
		}
		return opts, nil, err
	}
	return opts, fs.Args(), nil
}

func runAdmin(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printAdminHelp(stdout)
		return nil
	}
	switch args[0] {
	case "hello":
		fs := commandFlagSet("admin hello", stderr)
		clientName := fs.String("client-name", "almagest-cli", "client name sent to daemon")
		version := fs.String("version", api.Version, "client API version")
		surfaces := fs.String("surfaces", "", "comma-separated requested surface names")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		payload := api.HelloRequest{ClientName: *clientName, Version: *version, Surfaces: splitCSV(*surfaces)}
		return requestPretty(ctx, c, api.SurfaceAdmin, string(api.MethodAdminHello), payload, stdout)
	case "ping":
		fs := commandFlagSet("admin ping", stderr)
		nonce := fs.String("nonce", "", "nonce echoed by daemon")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceAdmin, string(api.MethodAdminPing), api.PingRequest{Nonce: *nonce}, stdout)
	case "colormap":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceAdmin, string(api.MethodAdminColorMap), nil, stdout, stderr)
	case "acquire-mutation":
		fs := commandFlagSet("admin acquire-mutation", stderr)
		holder := fs.String("holder", "almagest-cli", "human-readable mutation holder")
		stealExpired := fs.Bool("steal-expired", false, "request takeover of an expired lease")
		ttl := fs.Int64("ttl-ms", 0, "lease TTL in milliseconds")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		payload := api.AcquireMutationRequest{Holder: *holder, StealExpired: *stealExpired, TTLMilliseconds: *ttl}
		return requestPretty(ctx, c, api.SurfaceAdmin, string(api.MethodAdminAcquireMutation), payload, stdout)
	case "renew-mutation":
		fs := commandFlagSet("admin renew-mutation", stderr)
		leaseID := fs.String("lease-id", "", "active mutation lease ID")
		ttl := fs.Int64("ttl-ms", 0, "lease TTL in milliseconds")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *leaseID == "" {
			return fmt.Errorf("--lease-id is required")
		}
		payload := api.RenewMutationRequest{LeaseID: *leaseID, TTLMilliseconds: *ttl}
		return requestPretty(ctx, c, api.SurfaceAdmin, string(api.MethodAdminRenewMutation), payload, stdout)
	case "release-mutation":
		fs := commandFlagSet("admin release-mutation", stderr)
		leaseID := fs.String("lease-id", "", "active mutation lease ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *leaseID == "" {
			return fmt.Errorf("--lease-id is required")
		}
		return requestPretty(ctx, c, api.SurfaceAdmin, string(api.MethodAdminReleaseMutation), api.ReleaseMutationRequest{LeaseID: *leaseID}, stdout)
	case "watch-version-status":
		return watchEvents(ctx, c, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceAdmin && msg.Method == string(api.EventAdminProjectVersionStatus)
		})
	default:
		return fmt.Errorf("unknown admin command %q", args[0])
	}
}

func runEngine(ctx context.Context, c *client, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printEngineHelp(stdout)
		return nil
	}
	switch args[0] {
	case "watch":
		fs := commandFlagSet("engine watch", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		fullSnapshot := fs.Bool("full-snapshot", false, "request a full snapshot after subscribing")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		raw, err := c.request(ctx, api.SurfaceEngine, string(api.MethodEngineSubscribeNotifications), api.WorkspaceRequest{Workspace: *workspace})
		if err != nil {
			return err
		}
		if *fullSnapshot {
			resp, err := api.DecodePayload[api.SubscribeNotificationsResponse](raw)
			if err != nil {
				return err
			}
			_, err = c.request(ctx, api.SurfaceEngine, string(api.MethodEngineRequestFullSnapshot), api.SubscriptionRequest(resp))
			if err != nil {
				return err
			}
		}
		return watchEvents(ctx, c, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceEngine && msg.Method == string(api.EventEngineNotification)
		})
	case "subscribe-notifications":
		fs := commandFlagSet("engine subscribe-notifications", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineSubscribeNotifications), api.WorkspaceRequest{Workspace: *workspace}, stdout)
	case "unsubscribe-notifications", "request-full-snapshot":
		fs := commandFlagSet("engine "+args[0], stderr)
		id := fs.Uint64("subscription-id", 0, "notification subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		method := map[string]api.EngineMethod{
			"unsubscribe-notifications": api.MethodEngineUnsubscribeNotifications,
			"request-full-snapshot":     api.MethodEngineRequestFullSnapshot,
		}[args[0]]
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), api.SubscriptionRequest{SubscriptionID: *id}, stdout)
	case "subscribe-node-menu", "unsubscribe-node-menu":
		fs := commandFlagSet("engine "+args[0], stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		id := fs.Uint64("id", 0, "node ID")
		node := fs.Uint64("node", 0, "node ID (deprecated alias for --id)")
		name := fs.String("name", "", "node name")
		subID := fs.Uint64("subscription-id", 0, "notification subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		nodeID, err := resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, *id, *node, *name, true))
		if err != nil {
			return err
		}
		if *subID == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		method := api.MethodEngineSubscribeNodeMenu
		if args[0] == "unsubscribe-node-menu" {
			method = api.MethodEngineUnsubscribeNodeMenu
		}
		payload := api.NodeMenuSubscriptionRequest{Workspace: *workspace, Node: nodeID, SubscriptionID: *subID}
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), payload, stdout)
	case "workspace-snapshot":
		workspace, err := parseWorkspace(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineWorkspaceSnapshot), api.WorkspaceRequest{Workspace: workspace}, stdout)
	case "workers-snapshot":
		workspace, err := parseWorkspace(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineWorkersSnapshot), api.WorkspaceRequest{Workspace: workspace}, stdout)
	case "node-class-snapshot", "node-class-long-description", "remove-nodes-by-class":
		workspace, class, err := parseClass(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		method := map[string]api.EngineMethod{
			"node-class-snapshot":         api.MethodEngineNodeClassSnapshot,
			"node-class-long-description": api.MethodEngineNodeClassLongDescription,
			"remove-nodes-by-class":       api.MethodEngineRemoveNodesByClass,
		}[args[0]]
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), api.ClassRequest{Workspace: workspace, Class: class}, stdout)
	case "node-snapshot", "replace-node-with-placeholder", "remove-node":
		workspace, id, err := parseNodeID(ctx, c, args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		method := map[string]api.EngineMethod{
			"node-snapshot":                 api.MethodEngineNodeSnapshot,
			"replace-node-with-placeholder": api.MethodEngineReplaceNodeWithPlaceholder,
			"remove-node":                   api.MethodEngineRemoveNode,
		}[args[0]]
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), api.IDRequest{Workspace: workspace, ID: id}, stdout)
	case "port-snapshot", "link-snapshot", "remove-link":
		workspace, id, err := parseID(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		method := map[string]api.EngineMethod{
			"port-snapshot": api.MethodEnginePortSnapshot,
			"link-snapshot": api.MethodEngineLinkSnapshot,
			"remove-link":   api.MethodEngineRemoveLink,
		}[args[0]]
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), api.IDRequest{Workspace: workspace, ID: id}, stdout)
	case "remove-nodes":
		fs := commandFlagSet("engine remove-nodes", stderr)
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
	case "add-node":
		fs := commandFlagSet("engine add-node", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		class := fs.String("class", "", "node class")
		name := fs.String("name", "", "optional node name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *class == "" {
			return fmt.Errorf("--class is required")
		}
		payload := api.AddNodeRequest{Workspace: *workspace, Class: *class, Name: *name}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineAddNode), payload, stdout)
	case "add-link":
		fs := commandFlagSet("engine add-link", stderr)
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
	case "set-node-position":
		fs := commandFlagSet("engine set-node-position", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		id := fs.Uint64("id", 0, "node ID")
		name := fs.String("name", "", "node name")
		position := fs.String("position", "", "node position string")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		nodeID, err := resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, *id, 0, *name, false))
		if err != nil {
			return err
		}
		if *position == "" {
			return fmt.Errorf("--position is required")
		}
		payload := api.SetNodePositionRequest{Workspace: *workspace, ID: nodeID, Position: *position}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineSetNodePosition), payload, stdout)
	case "set-node-name":
		fs := commandFlagSet("engine set-node-name", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		id := fs.Uint64("id", 0, "node ID")
		name := fs.String("name", "", "node name")
		newName := fs.String("new-name", "", "new node name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var (
			nodeID uint64
			err    error
			next   string
		)
		if flagSet(fs, "id") {
			nodeID, err = resolveNodeSelector(ctx, c, *workspace, nodeSelector{id: *id, idSet: true})
			if flagSet(fs, "new-name") {
				next = *newName
			} else {
				next = *name
			}
		} else {
			nodeID, err = resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, 0, 0, *name, false))
			next = *newName
		}
		if err != nil {
			return err
		}
		if !flagSet(fs, "id") && !flagSet(fs, "new-name") {
			return fmt.Errorf("--new-name is required when selecting by --name")
		}
		payload := api.SetNodeNameRequest{Workspace: *workspace, ID: nodeID, Name: next}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineSetNodeName), payload, stdout)
	case "remove-node-popup", "remove-node-popups", "remove-node-popups-by-type":
		fs := commandFlagSet("engine "+args[0], stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		id := fs.Uint64("id", 0, "node ID")
		node := fs.Uint64("node", 0, "node ID (deprecated alias for --id)")
		name := fs.String("name", "", "node name")
		popup := fs.Uint64("popup", 0, "popup ID")
		typ := fs.String("type", "", "popup type")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		nodeID, err := resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, *id, *node, *name, true))
		if err != nil {
			return err
		}
		method := map[string]api.EngineMethod{
			"remove-node-popup":          api.MethodEngineRemoveNodePopup,
			"remove-node-popups":         api.MethodEngineRemoveNodePopups,
			"remove-node-popups-by-type": api.MethodEngineRemoveNodePopupsByType,
		}[args[0]]
		payload := api.NodePopupRequest{Workspace: *workspace, Node: nodeID, Popup: *popup, Type: *typ}
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), payload, stdout)
	case "copy":
		fs := commandFlagSet("engine copy", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		ids := fs.String("ids", "", "comma-separated node IDs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		parsed, err := parseUintList(*ids)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineCopy), api.CopyRequest{Workspace: *workspace, IDs: parsed}, stdout)
	case "paste":
		fs := commandFlagSet("engine paste", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		data := fs.String("data", "", "copy payload data")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEnginePaste), api.PasteRequest{Workspace: *workspace, Data: *data}, stdout)
	case "send-inbox":
		fs := commandFlagSet("engine send-inbox", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		id := fs.Uint64("id", 0, "node ID")
		node := fs.Uint64("node", 0, "node ID (deprecated alias for --id)")
		name := fs.String("name", "", "node name")
		payload := fs.String("payload", "null", "JSON inbox payload")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		nodeID, err := resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, *id, *node, *name, true))
		if err != nil {
			return err
		}
		raw, err := parseRawJSON(*payload, "--payload")
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineSendInbox), api.SendInboxRequest{Workspace: *workspace, Node: nodeID, Payload: raw}, stdout)
	case "send-node-formular-msg":
		fs := commandFlagSet("engine send-node-formular-msg", stderr)
		workspace := fs.String("workspace", "main", "workspace name")
		id := fs.Uint64("id", 0, "node ID")
		node := fs.Uint64("node", 0, "node ID (deprecated alias for --id)")
		name := fs.String("name", "", "node name")
		message := fs.String("message", "null", "JSON formular message")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		nodeID, err := resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, *id, *node, *name, true))
		if err != nil {
			return err
		}
		raw, err := parseRawJSON(*message, "--message")
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineSendNodeFormularMessage), api.SendNodeFormularMessageRequest{Workspace: *workspace, Node: nodeID, Message: raw}, stdout)
	case "undo", "redo":
		workspace, err := parseWorkspace(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		method := api.MethodEngineUndo
		if args[0] == "redo" {
			method = api.MethodEngineRedo
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(method), api.WorkspaceRequest{Workspace: workspace}, stdout)
	default:
		return fmt.Errorf("unknown engine command %q", args[0])
	}
}

func runLog(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printLogHelp(stdout)
		return nil
	}
	switch args[0] {
	case "watch":
		_, err := c.request(ctx, api.SurfaceLog, string(api.MethodLogSubscribe), nil)
		if err != nil {
			return err
		}
		return watchEvents(ctx, c, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceLog && msg.Method == string(api.EventLogEntry)
		})
	case "subscribe":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceLog, string(api.MethodLogSubscribe), nil, stdout, stderr)
	case "unsubscribe":
		fs := commandFlagSet("log unsubscribe", stderr)
		id := fs.Uint64("subscription-id", 0, "log subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		return requestPretty(ctx, c, api.SurfaceLog, string(api.MethodLogUnsubscribe), api.SubscriptionRequest{SubscriptionID: *id}, stdout)
	default:
		return fmt.Errorf("unknown log command %q", args[0])
	}
}

func runRaw(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	fs := commandFlagSet("raw", stderr)
	surface := fs.String("surface", "", "API surface")
	method := fs.String("method", "", "API method")
	payload := fs.String("payload", "{}", "JSON request payload")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *surface == "" || *method == "" {
		return fmt.Errorf("--surface and --method are required")
	}
	raw, err := parseRawJSON(*payload, "--payload")
	if err != nil {
		return err
	}
	return requestPretty(ctx, c, api.Surface(*surface), *method, raw, stdout)
}

func requestPretty(ctx context.Context, c *client, surface api.Surface, method string, payload any, stdout io.Writer) error {
	raw, err := c.request(ctx, surface, method, payload)
	if err != nil {
		return err
	}
	var value any
	if len(raw) == 0 {
		value = map[string]any{}
	} else if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return writePrettyJSON(stdout, value)
}

func watchEvents(ctx context.Context, c *client, stdout io.Writer, accept func(api.Message) bool) error {
	for {
		msg, err := c.nextEvent(ctx)
		if err != nil {
			return err
		}
		if !accept(msg) {
			continue
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
			return err
		}
	}
}

func parseNoArgsAndRequest(ctx context.Context, c *client, args []string, surface api.Surface, method string, payload any, stdout, stderr io.Writer) error {
	fs := commandFlagSet(method, stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", method)
	}
	return requestPretty(ctx, c, surface, method, payload, stdout)
}

func commandFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parseWorkspace(name string, args []string, stderr io.Writer) (string, error) {
	fs := commandFlagSet("engine "+name, stderr)
	workspace := fs.String("workspace", "main", "workspace name")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	return *workspace, nil
}

func parseClass(name string, args []string, stderr io.Writer) (string, string, error) {
	fs := commandFlagSet("engine "+name, stderr)
	workspace := fs.String("workspace", "main", "workspace name")
	class := fs.String("class", "", "node class")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if *class == "" {
		return "", "", fmt.Errorf("--class is required")
	}
	return *workspace, *class, nil
}

func parseID(name string, args []string, stderr io.Writer) (string, uint64, error) {
	fs := commandFlagSet("engine "+name, stderr)
	workspace := fs.String("workspace", "main", "workspace name")
	id := fs.Uint64("id", 0, "object ID")
	if err := fs.Parse(args); err != nil {
		return "", 0, err
	}
	if *id == 0 {
		return "", 0, fmt.Errorf("--id is required")
	}
	return *workspace, *id, nil
}

type nodeSelector struct {
	id        uint64
	node      uint64
	name      string
	idSet     bool
	nodeSet   bool
	nameSet   bool
	allowNode bool
}

func parseNodeID(ctx context.Context, c *client, name string, args []string, stderr io.Writer) (string, uint64, error) {
	fs := commandFlagSet("engine "+name, stderr)
	workspace := fs.String("workspace", "main", "workspace name")
	id := fs.Uint64("id", 0, "node ID")
	nodeName := fs.String("name", "", "node name")
	if err := fs.Parse(args); err != nil {
		return "", 0, err
	}
	nodeID, err := resolveNodeSelector(ctx, c, *workspace, nodeSelectorFromFlags(fs, *id, 0, *nodeName, false))
	if err != nil {
		return "", 0, err
	}
	return *workspace, nodeID, nil
}

func nodeSelectorFromFlags(fs *flag.FlagSet, id, node uint64, name string, allowNode bool) nodeSelector {
	return nodeSelector{
		id:        id,
		node:      node,
		name:      name,
		idSet:     flagSet(fs, "id"),
		nodeSet:   allowNode && flagSet(fs, "node"),
		nameSet:   flagSet(fs, "name"),
		allowNode: allowNode,
	}
}

func resolveNodeSelector(ctx context.Context, c *client, workspace string, selector nodeSelector) (uint64, error) {
	set := 0
	for _, ok := range []bool{selector.idSet, selector.nodeSet, selector.nameSet} {
		if ok {
			set++
		}
	}
	if set == 0 {
		if selector.allowNode {
			return 0, fmt.Errorf("--id or --name is required (--node is also accepted for compatibility)")
		}
		return 0, fmt.Errorf("--id or --name is required")
	}
	if set > 1 {
		if selector.allowNode {
			return 0, fmt.Errorf("only one of --id, --name, or --node may be specified")
		}
		return 0, fmt.Errorf("only one of --id or --name may be specified")
	}
	if selector.idSet || selector.nodeSet {
		if selector.idSet && selector.id == 0 {
			return 0, fmt.Errorf("--id is required")
		}
		if selector.nodeSet && selector.node == 0 {
			return 0, fmt.Errorf("--node is required")
		}
		if selector.nodeSet {
			return selector.node, nil
		}
		return selector.id, nil
	}
	if selector.name == "" {
		return 0, fmt.Errorf("--name is required")
	}
	return resolveNodeName(ctx, c, workspace, selector.name)
}

func resolveNodeName(ctx context.Context, c *client, workspace, name string) (uint64, error) {
	snapshot, err := fetchWorkspaceSnapshot(ctx, c, workspace)
	if err != nil {
		return 0, err
	}
	var matched uint64
	for id, node := range snapshot.Nodes {
		if node.Name != name {
			continue
		}
		if matched != 0 {
			return 0, fmt.Errorf("node name %q is ambiguous", name)
		}
		matched = id
	}
	if matched == 0 {
		return 0, fmt.Errorf("node name %q was not found", name)
	}
	return matched, nil
}

func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func parseRawJSON(value, flagName string) (json.RawMessage, error) {
	raw := json.RawMessage(value)
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%s must be valid JSON", flagName)
	}
	return raw, nil
}

func parseUintList(value string) ([]uint64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("--ids is required")
	}
	parts := strings.Split(value, ",")
	out := make([]uint64, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse --ids: %w", err)
		}
		out = append(out, n)
	}
	return out, nil
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func printTopHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli [--url ENDPOINT] <surface> <command> [flags]
  almagest-cli [--url ENDPOINT] raw --surface SURFACE --method METHOD --payload JSON

Endpoint URLs:
  unix://run/almagest/admin.sock   Unix socket at /run/almagest/admin.sock (default)
  unix:///absolute/path.sock       Unix socket at an explicit absolute path
  web://127.0.0.1:8090             Web admin API via ws://127.0.0.1:8090/api/ws

Surfaces:
  admin    hello, ping, colormap, mutation lease commands, version status watch
  config   read and update the editable HuJSON daemon config
  dns      DNS cache snapshots, subscriptions, and mutations
  docs     fetch the documentation tree and pages
  engine   workspace snapshots, graph mutations, notification subscriptions
  kv       persistent key/value store snapshots, subscriptions, and mutations
  log      log subscription and watch commands
  secrets  secret snapshots, subscriptions, and mutations
  snapshots create, delete, export, import, list, load, rename, and restore daemon snapshots
  tls      TLS certificate, policy, and built-in CA commands
  graph    high-level node, link, class, and node menu commands
  workers  worker-system snapshots and tree rendering
  raw      direct request for newly added or experimental methods

Global flags:
  -u, --url ENDPOINT   admin API endpoint URL (unix://, pipe://user, pipe://machine, or web://)
  -h, --help           show help

Environment:
  ALMAGEST_API_URL     admin API endpoint URL; overridden by -u or --url

Run "almagest-cli <surface> --help" for command-specific help.
`)
}

func printAdminHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli admin <command> [flags]

Commands:
  hello                  show daemon API version and available surfaces
  ping                   round-trip a nonce through the daemon
  colormap               print default type color map
  acquire-mutation       acquire an advisory mutation lease
  renew-mutation         renew an advisory mutation lease
  release-mutation       release an advisory mutation lease
  watch-version-status   print project_version_status events as one-line JSON
`)
}

func printEngineHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli engine <command> [flags]

Subscription commands:
  watch                         subscribe to workspace notifications and print events
  subscribe-notifications       subscribe to engine notifications
  unsubscribe-notifications     unsubscribe from engine notifications
  request-full-snapshot         request a full snapshot for a subscription
  subscribe-node-menu           subscribe a node menu through a notification subscription
  unsubscribe-node-menu         unsubscribe a node menu

Snapshot commands:
  workspace-snapshot
  workers-snapshot
  node-class-snapshot
  node-snapshot
  port-snapshot
  link-snapshot
  node-class-long-description

Mutation commands:
  add-node, replace-node-with-placeholder, add-link, remove-node, remove-nodes
  remove-nodes-by-class, remove-link, set-node-position, set-node-name
  remove-node-popup, remove-node-popups, remove-node-popups-by-type
  copy, paste, send-inbox, undo, redo, send-node-formular-msg

Node selectors:
  Node-specific commands accept --id or --name. Commands that historically used
  --node still accept it as an alias for --id. For set-node-name with --name as
  the selector, pass the new value with --new-name.
`)
}

func printLogHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli log <command> [flags]

Commands:
  watch         subscribe and print log entry events as one-line JSON
  subscribe     create a log subscription and print its cached response
  unsubscribe   unsubscribe by --subscription-id
`)
}
