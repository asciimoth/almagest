package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/asciimoth/almagest/bin/api"
)

func runKV(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printKVHelp(stdout)
		return nil
	}
	switch args[0] {
	case "stores":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceKV, string(api.MethodKVStoresSnapshot), nil, stdout, stderr)
	case "watch-stores":
		if err := parseNoArgs(args[0], args[1:], stderr); err != nil {
			return err
		}
		return subscribeAndWatchKV(ctx, c, api.MethodKVSubscribeStores, nil, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceKV && msg.Method == string(api.EventKVStoresUpdate)
		})
	case "entries":
		req, err := parseKVEntriesRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVEntriesSnapshot), req, stdout)
	case "watch-entries":
		req, err := parseKVEntriesRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return subscribeAndWatchKV(ctx, c, api.MethodKVSubscribeEntries, req, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceKV && msg.Method == string(api.EventKVEntriesUpdate)
		})
	case "get":
		req, err := parseKVValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVValueSnapshot), req, stdout)
	case "watch-value":
		req, err := parseKVValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return subscribeAndWatchKV(ctx, c, api.MethodKVSubscribeValue, req, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceKV && msg.Method == string(api.EventKVValueUpdate)
		})
	case "ensure":
		req, err := parseKVEntriesRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVEnsure), req, stdout)
	case "set":
		req, err := parseKVSetRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVSet), req, stdout)
	case "delete":
		req, err := parseKVValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVDelete), req, stdout)
	case "drop":
		req, err := parseKVEntriesRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVDrop), req, stdout)
	case "unsubscribe":
		fs := commandFlagSet("kv unsubscribe", stderr)
		id := fs.Uint64("subscription-id", 0, "KV subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		return requestPretty(ctx, c, api.SurfaceKV, string(api.MethodKVUnsubscribe), api.KVSubscriptionRequest{SubscriptionID: *id}, stdout)
	default:
		return fmt.Errorf("unknown kv command %q", args[0])
	}
}

func parseKVEntriesRequest(name string, args []string, stderr io.Writer) (api.KVEntriesRequest, error) {
	fs := commandFlagSet("kv "+name, stderr)
	plugin := fs.String("plugin", "", "plugin store name; omitted selects the default store")
	path := fs.String("path", "", "slash-separated subtree path")
	if err := fs.Parse(args); err != nil {
		return api.KVEntriesRequest{}, err
	}
	segments, err := parseKVPath(*path)
	if err != nil {
		return api.KVEntriesRequest{}, err
	}
	return api.KVEntriesRequest{
		Store: kvStoreSelector(*plugin),
		Path:  segments,
	}, nil
}

func parseKVValueRequest(name string, args []string, stderr io.Writer) (api.KVValueRequest, error) {
	fs := commandFlagSet("kv "+name, stderr)
	plugin := fs.String("plugin", "", "plugin store name; omitted selects the default store")
	path := fs.String("path", "", "slash-separated subtree path")
	key := fs.String("key", "", "key name")
	if err := fs.Parse(args); err != nil {
		return api.KVValueRequest{}, err
	}
	if *key == "" {
		return api.KVValueRequest{}, fmt.Errorf("--key is required")
	}
	segments, err := parseKVPath(*path)
	if err != nil {
		return api.KVValueRequest{}, err
	}
	return api.KVValueRequest{
		Store: kvStoreSelector(*plugin),
		Path:  segments,
		Key:   *key,
	}, nil
}

func parseKVSetRequest(name string, args []string, stderr io.Writer) (api.KVSetRequest, error) {
	fs := commandFlagSet("kv "+name, stderr)
	plugin := fs.String("plugin", "", "plugin store name; omitted selects the default store")
	path := fs.String("path", "", "slash-separated subtree path")
	key := fs.String("key", "", "key name")
	data := fs.String("data", "", "value data")
	encoding := fs.String("encoding", "string", "value data encoding: string or base64")
	stringValue := fs.Bool("string", true, "mark value as displayable string")
	hidden := fs.Bool("hidden", false, "mark value as hidden")
	if err := fs.Parse(args); err != nil {
		return api.KVSetRequest{}, err
	}
	if *key == "" {
		return api.KVSetRequest{}, fmt.Errorf("--key is required")
	}
	segments, err := parseKVPath(*path)
	if err != nil {
		return api.KVSetRequest{}, err
	}
	return api.KVSetRequest{
		Store: kvStoreSelector(*plugin),
		Path:  segments,
		Key:   *key,
		Value: api.KVValue{
			Data:     *data,
			Encoding: strings.ToLower(*encoding),
			Hidden:   *hidden,
			String:   *stringValue,
		},
	}, nil
}

func subscribeAndWatchKV(
	ctx context.Context,
	c *client,
	method api.KVMethod,
	payload any,
	stdout io.Writer,
	accept func(api.Message) bool,
) error {
	raw, err := c.request(ctx, api.SurfaceKV, string(method), payload)
	if err != nil {
		return err
	}
	var value any
	if len(raw) == 0 {
		value = map[string]any{}
	} else if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if err := writePrettyJSON(stdout, value); err != nil {
		return err
	}
	return watchEvents(ctx, c, stdout, accept)
}

func parseNoArgs(name string, args []string, stderr io.Writer) error {
	fs := commandFlagSet(name, stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", name)
	}
	return nil
}

func kvStoreSelector(plugin string) api.KVStoreSelector {
	if plugin == "" {
		return api.KVStoreSelector{Kind: api.KVStoreDefault}
	}
	return api.KVStoreSelector{Kind: api.KVStorePlugin, Name: plugin}
}

func parseKVPath(path string) ([]string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil, nil
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("--path contains an empty segment")
		}
	}
	return parts, nil
}

func printKVHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli kv <command> [flags]

Snapshot commands:
  stores          list default and plugin KV stores
  entries         list immediate keys and buckets at --path
  get             fetch one key by --key

Subscription commands:
  watch-stores    subscribe and print store-list snapshots and updates
  watch-entries   subscribe and print subtree snapshots and updates
  watch-value     subscribe and print key snapshots and updates
  unsubscribe     unsubscribe by --subscription-id

Mutation commands:
  ensure          create the subtree at --path
  set             set --key to --data
  delete          delete one value by --key
  drop            recursively delete the subtree at --path

Common flags:
  --plugin NAME   select plugin store NAME instead of the default store
  --path PATH     slash-separated subtree path inside the selected store
`)
}
