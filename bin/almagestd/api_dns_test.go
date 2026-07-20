package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	gdns "github.com/asciimoth/gonnect/dns"
)

func TestDNSSubscriptionSendsIncrementalEvents(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	var events []api.Message
	session := (&AdminAPI{dns: manager}).newAPISession(nil, nil, func(msg api.Message) error {
		if msg.Kind == api.MessageEvent && msg.Surface == api.SurfaceDNS {
			events = append(events, msg)
		}
		return nil
	})
	defer session.close()

	subCaches := mustRequest(t, "1", api.SurfaceDNS, string(api.MethodDNSSubscribeCaches), nil)
	resp, err := session.handleMessage(subCaches)
	if err != nil {
		t.Fatal(err)
	}
	cachesResp, err := api.DecodePayload[api.DNSSubscribeCachesResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if cachesResp.SubscriptionID == 0 || len(cachesResp.Snapshot.Caches) != 0 {
		t.Fatalf("caches subscribe response = %#v, want live empty subscription", cachesResp)
	}

	subKeys := mustRequest(t, "2", api.SurfaceDNS, string(api.MethodDNSSubscribeKeys), api.DNSKeysRequest{
		Cache: "resolver",
	})
	resp, err = session.handleMessage(subKeys)
	if err != nil {
		t.Fatal(err)
	}
	keysResp, err := api.DecodePayload[api.DNSSubscribeKeysResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if keysResp.SubscriptionID == 0 || keysResp.Snapshot.Exists {
		t.Fatalf("keys subscribe response = %#v, want live missing-cache subscription", keysResp)
	}

	subValue := mustRequest(t, "3", api.SurfaceDNS, string(api.MethodDNSSubscribeValue), api.DNSValueRequest{
		Cache: "resolver",
		Key:   "example",
	})
	resp, err = session.handleMessage(subValue)
	if err != nil {
		t.Fatal(err)
	}
	valueResp, err := api.DecodePayload[api.DNSSubscribeValueResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if valueResp.SubscriptionID == 0 || valueResp.Snapshot.Found {
		t.Fatalf("value subscribe response = %#v, want live missing-value subscription", valueResp)
	}

	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	if len(events) != 1 || events[0].Method != string(api.EventDNSCachesUpdate) {
		t.Fatalf("events after cache creation = %#v, want one cache update", events)
	}
	var cacheEvent api.DNSCachesEvent
	if err := json.Unmarshal(events[0].Payload, &cacheEvent); err != nil {
		t.Fatal(err)
	}
	if cacheEvent.SubscriptionID != cachesResp.SubscriptionID || cacheEvent.Change.Name != "resolver" || cacheEvent.Change.Deleted {
		t.Fatalf("cache event = %#v, want resolver upsert", cacheEvent)
	}

	events = nil
	storage.Set("example", testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 40}, 60), time.Now())
	if len(events) != 2 {
		t.Fatalf("events after set = %d, want key and value changes", len(events))
	}
	var (
		keyEvent   api.DNSKeysEvent
		valueEvent api.DNSValueEvent
	)
	for _, event := range events {
		switch event.Method {
		case string(api.EventDNSKeysUpdate):
			if err := json.Unmarshal(event.Payload, &keyEvent); err != nil {
				t.Fatal(err)
			}
		case string(api.EventDNSValueUpdate):
			if err := json.Unmarshal(event.Payload, &valueEvent); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected DNS event method %q", event.Method)
		}
	}
	if keyEvent.SubscriptionID != keysResp.SubscriptionID ||
		keyEvent.Cache != "resolver" ||
		keyEvent.Change.Key != "example" ||
		keyEvent.Change.Deleted ||
		keyEvent.Change.Entry == nil ||
		keyEvent.Change.Entry.Size == 0 {
		t.Fatalf("key event = %#v, want incremental key upsert", keyEvent)
	}
	if valueEvent.SubscriptionID != valueResp.SubscriptionID ||
		valueEvent.Cache != "resolver" ||
		valueEvent.Key != "example" ||
		!valueEvent.Found ||
		valueEvent.Value == nil ||
		valueEvent.Value.JSON == "" {
		t.Fatalf("value event = %#v, want incremental value update", valueEvent)
	}

	events = nil
	storage.Delete("example")
	if len(events) != 2 {
		t.Fatalf("events after delete = %d, want key delete and missing value", len(events))
	}
	keyEvent = api.DNSKeysEvent{}
	valueEvent = api.DNSValueEvent{}
	for _, event := range events {
		switch event.Method {
		case string(api.EventDNSKeysUpdate):
			if err := json.Unmarshal(event.Payload, &keyEvent); err != nil {
				t.Fatal(err)
			}
		case string(api.EventDNSValueUpdate):
			if err := json.Unmarshal(event.Payload, &valueEvent); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !keyEvent.Change.Deleted || keyEvent.Change.Key != "example" {
		t.Fatalf("key delete event = %#v, want deleted example key", keyEvent)
	}
	if valueEvent.Found || valueEvent.Value != nil {
		t.Fatalf("value delete event = %#v, want missing value update", valueEvent)
	}

	unsubKeys := mustRequest(t, "4", api.SurfaceDNS, string(api.MethodDNSUnsubscribe), api.DNSSubscriptionRequest{
		SubscriptionID: keysResp.SubscriptionID,
	})
	if _, err := session.handleMessage(unsubKeys); err != nil {
		t.Fatal(err)
	}
	events = nil
	storage.Set("example", testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 41}, 60), time.Now())
	if len(events) != 0 {
		t.Fatalf("events after keys unsubscribe = %d, want descendant value subscription removed", len(events))
	}
}
