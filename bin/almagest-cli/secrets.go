package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/bin/api"
)

func runSecrets(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSecretsHelp(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceSecrets, string(api.MethodSecretsSnapshot), nil, stdout, stderr)
	case "watch":
		if err := parseNoArgs(args[0], args[1:], stderr); err != nil {
			return err
		}
		return subscribeAndWatchSecrets(ctx, c, stdout)
	case "set":
		req, err := parseSecretsSetValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceSecrets, string(api.MethodSecretsSetValue), req, stdout)
	case "set-nodes":
		req, err := parseSecretsSetNodesRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceSecrets, string(api.MethodSecretsSetNodes), req, stdout)
	case "rename":
		req, err := parseSecretsRenameRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceSecrets, string(api.MethodSecretsRename), req, stdout)
	case "delete":
		req, err := parseSecretKeyRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceSecrets, string(api.MethodSecretsDelete), req, stdout)
	case "unsubscribe":
		fs := commandFlagSet("secrets unsubscribe", stderr)
		id := fs.Uint64("subscription-id", 0, "secrets subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		return requestPretty(ctx, c, api.SurfaceSecrets, string(api.MethodSecretsUnsubscribe), api.SecretsSubscriptionRequest{SubscriptionID: *id}, stdout)
	default:
		return fmt.Errorf("unknown secrets command %q", args[0])
	}
}

func parseSecretKeyRequest(name string, args []string, stderr io.Writer) (api.SecretKeyRequest, error) {
	fs := commandFlagSet("secrets "+name, stderr)
	key := fs.String("key", "", "secret key")
	if err := fs.Parse(args); err != nil {
		return api.SecretKeyRequest{}, err
	}
	if *key == "" {
		return api.SecretKeyRequest{}, fmt.Errorf("--key is required")
	}
	return api.SecretKeyRequest{Key: *key}, nil
}

func parseSecretsSetValueRequest(name string, args []string, stderr io.Writer) (api.SecretsSetValueRequest, error) {
	fs := commandFlagSet("secrets "+name, stderr)
	key := fs.String("key", "", "secret key")
	value := fs.String("value", "", "secret value")
	if err := fs.Parse(args); err != nil {
		return api.SecretsSetValueRequest{}, err
	}
	if *key == "" {
		return api.SecretsSetValueRequest{}, fmt.Errorf("--key is required")
	}
	return api.SecretsSetValueRequest{Key: *key, Value: *value}, nil
}

func parseSecretsSetNodesRequest(name string, args []string, stderr io.Writer) (api.SecretsSetNodesRequest, error) {
	fs := commandFlagSet("secrets "+name, stderr)
	key := fs.String("key", "", "secret key")
	clear := fs.Bool("clear-nodes", false, "clear all node access")
	var nodes repeatedStringFlag
	fs.Var(&nodes, "node", "allowed node name; may be repeated")
	if err := fs.Parse(args); err != nil {
		return api.SecretsSetNodesRequest{}, err
	}
	if *key == "" {
		return api.SecretsSetNodesRequest{}, fmt.Errorf("--key is required")
	}
	if len(nodes) == 0 && !*clear {
		return api.SecretsSetNodesRequest{}, fmt.Errorf("at least one --node or --clear-nodes is required")
	}
	if len(nodes) > 0 && *clear {
		return api.SecretsSetNodesRequest{}, fmt.Errorf("--node and --clear-nodes cannot be used together")
	}
	return api.SecretsSetNodesRequest{Key: *key, Nodes: []string(nodes)}, nil
}

func parseSecretsRenameRequest(name string, args []string, stderr io.Writer) (api.SecretsRenameRequest, error) {
	fs := commandFlagSet("secrets "+name, stderr)
	key := fs.String("key", "", "current secret key")
	newKey := fs.String("new-key", "", "new secret key")
	if err := fs.Parse(args); err != nil {
		return api.SecretsRenameRequest{}, err
	}
	if *key == "" {
		return api.SecretsRenameRequest{}, fmt.Errorf("--key is required")
	}
	if *newKey == "" {
		return api.SecretsRenameRequest{}, fmt.Errorf("--new-key is required")
	}
	return api.SecretsRenameRequest{Key: *key, NewKey: *newKey}, nil
}

func subscribeAndWatchSecrets(ctx context.Context, c *client, stdout io.Writer) error {
	raw, err := c.request(ctx, api.SurfaceSecrets, string(api.MethodSecretsSubscribe), nil)
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
	return watchEvents(ctx, c, stdout, func(msg api.Message) bool {
		return msg.Surface == api.SurfaceSecrets && msg.Method == string(api.EventSecretsUpdate)
	})
}

type repeatedStringFlag []string

func (f *repeatedStringFlag) String() string {
	if f == nil {
		return ""
	}
	data, err := json.Marshal([]string(*f))
	if err != nil {
		return ""
	}
	return string(data)
}

func (f *repeatedStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func printSecretsHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli secrets <command> [flags]

Snapshot commands:
  list            list effective secrets

Subscription commands:
  watch           subscribe and print secret snapshots and updates
  unsubscribe     unsubscribe by --subscription-id

Mutation commands:
  set             set --key to --value
  set-nodes       replace allowed nodes for --key
  rename          rename --key to --new-key
  delete          delete one mutable secret by --key

Common flags:
  --key KEY       secret key

Node flags for set-nodes:
  --node NAME     allowed node name; may be repeated and preserves commas
  --clear-nodes   clear all node access for the secret
`)
}
