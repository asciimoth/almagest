package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/asciimoth/almagest/bin/api"
)

func runDNS(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printDNSHelp(stdout)
		return nil
	}
	switch args[0] {
	case "caches":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceDNS, string(api.MethodDNSCachesSnapshot), nil, stdout, stderr)
	case "watch-caches":
		if err := parseNoArgs(args[0], args[1:], stderr); err != nil {
			return err
		}
		return subscribeAndWatchDNS(ctx, c, api.MethodDNSSubscribeCaches, nil, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceDNS && msg.Method == string(api.EventDNSCachesUpdate)
		})
	case "keys":
		req, err := parseDNSKeysRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceDNS, string(api.MethodDNSKeysSnapshot), req, stdout)
	case "watch-keys":
		req, err := parseDNSKeysRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return subscribeAndWatchDNS(ctx, c, api.MethodDNSSubscribeKeys, req, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceDNS && msg.Method == string(api.EventDNSKeysUpdate)
		})
	case "get":
		req, err := parseDNSValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceDNS, string(api.MethodDNSValueSnapshot), req, stdout)
	case "watch-value":
		req, err := parseDNSValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return subscribeAndWatchDNS(ctx, c, api.MethodDNSSubscribeValue, req, stdout, func(msg api.Message) bool {
			return msg.Surface == api.SurfaceDNS && msg.Method == string(api.EventDNSValueUpdate)
		})
	case "set":
		req, err := parseDNSSetRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceDNS, string(api.MethodDNSSet), req, stdout)
	case "delete":
		req, err := parseDNSValueRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceDNS, string(api.MethodDNSDelete), req, stdout)
	case "drop":
		req, err := parseDNSKeysRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceDNS, string(api.MethodDNSDrop), req, stdout)
	case "unsubscribe":
		fs := commandFlagSet("dns unsubscribe", stderr)
		id := fs.Uint64("subscription-id", 0, "DNS subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		return requestPretty(ctx, c, api.SurfaceDNS, string(api.MethodDNSUnsubscribe), api.DNSSubscriptionRequest{SubscriptionID: *id}, stdout)
	default:
		return fmt.Errorf("unknown dns command %q", args[0])
	}
}

// parseDNSKeysRequest builds the cache-scoped request shared by key snapshots,
// key subscriptions, and cache drops.
func parseDNSKeysRequest(name string, args []string, stderr io.Writer) (api.DNSKeysRequest, error) {
	fs := commandFlagSet("dns "+name, stderr)
	cache := fs.String("cache", "", "DNS cache name")
	if err := fs.Parse(args); err != nil {
		return api.DNSKeysRequest{}, err
	}
	if *cache == "" {
		return api.DNSKeysRequest{}, fmt.Errorf("--cache is required")
	}
	return api.DNSKeysRequest{Cache: *cache}, nil
}

// parseDNSValueRequest builds the cache/key request shared by value snapshots,
// value subscriptions, and deletes.
func parseDNSValueRequest(name string, args []string, stderr io.Writer) (api.DNSValueRequest, error) {
	fs := commandFlagSet("dns "+name, stderr)
	cache := fs.String("cache", "", "DNS cache name")
	key := fs.String("key", "", "DNS cache key")
	if err := fs.Parse(args); err != nil {
		return api.DNSValueRequest{}, err
	}
	if *cache == "" {
		return api.DNSValueRequest{}, fmt.Errorf("--cache is required")
	}
	if *key == "" {
		return api.DNSValueRequest{}, fmt.Errorf("--key is required")
	}
	return api.DNSValueRequest{Cache: *cache, Key: *key}, nil
}

// parseDNSSetRequest accepts a readable DNS record and lets the daemon compute
// the canonical cache key. When --key is present, the daemon removes that older
// entry if the canonical key changes.
func parseDNSSetRequest(name string, args []string, stderr io.Writer) (api.DNSSetRequest, error) {
	fs := commandFlagSet("dns "+name, stderr)
	cache := fs.String("cache", "", "DNS cache name")
	key := fs.String("key", "", "existing DNS cache key to replace")
	recordName := fs.String("name", "", "DNS record owner name")
	recordType := fs.String("type", "", "DNS record type: A, AAAA, CNAME, NS, PTR, MX, TXT, or SRV")
	recordClass := fs.String("class", "IN", "DNS record class")
	ttl := fs.Uint("ttl", 0, "DNS record TTL in seconds")
	value := fs.String("value", "", "DNS record value")
	if err := fs.Parse(args); err != nil {
		return api.DNSSetRequest{}, err
	}
	if *cache == "" {
		return api.DNSSetRequest{}, fmt.Errorf("--cache is required")
	}
	if *recordName == "" {
		return api.DNSSetRequest{}, fmt.Errorf("--name is required")
	}
	if *recordType == "" {
		return api.DNSSetRequest{}, fmt.Errorf("--type is required")
	}
	if *ttl == 0 {
		return api.DNSSetRequest{}, fmt.Errorf("--ttl is required")
	}
	if *value == "" {
		return api.DNSSetRequest{}, fmt.Errorf("--value is required")
	}
	if *ttl > uint(^uint32(0)) {
		return api.DNSSetRequest{}, fmt.Errorf("--ttl must fit in uint32")
	}
	return api.DNSSetRequest{
		Cache: *cache,
		Key:   *key,
		Record: api.DNSRecordInput{
			Name:  *recordName,
			Type:  strings.ToUpper(*recordType),
			Class: strings.ToUpper(*recordClass),
			TTL:   uint32(*ttl),
			Value: *value,
		},
	}, nil
}

func subscribeAndWatchDNS(
	ctx context.Context,
	c *client,
	method api.DNSMethod,
	payload any,
	stdout io.Writer,
	accept func(api.Message) bool,
) error {
	raw, err := c.request(ctx, api.SurfaceDNS, string(method), payload)
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

func printDNSHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli dns <command> [flags]

Snapshot commands:
  caches          list DNS caches
  keys            list keys in --cache
  get             fetch one cached DNS value by --key

Subscription commands:
  watch-caches    subscribe and print cache-list snapshots and updates
  watch-keys      subscribe and print key snapshots and updates for --cache
  watch-value     subscribe and print value snapshots and updates for --key
  unsubscribe     unsubscribe by --subscription-id

Mutation commands:
  set             set a readable DNS record in --cache
  delete          delete one cached value by --key
  drop            delete all cached values in --cache

Common flags:
  --cache NAME    DNS cache name
  --key KEY       DNS cache key; for set, replace this existing key if needed

Record flags for set:
  --name NAME     DNS record owner name
  --type TYPE     DNS record type: A, AAAA, CNAME, NS, PTR, MX, TXT, or SRV
  --class CLASS   DNS record class (default IN)
  --ttl SECONDS   DNS record TTL in seconds
  --value VALUE   DNS record value
`)
}
