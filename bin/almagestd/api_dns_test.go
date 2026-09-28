package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	gdns "github.com/asciimoth/gonnect/dns"
)

func TestDNSSnapshotAPIDispatch(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	storage.Set("example", testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 1}, 60), time.Now())
	session := (&AdminAPI{dns: manager}).newAPISession(nil, nil, nil)

	tests := []struct {
		method  api.DNSMethod
		payload any
	}{
		{api.MethodDNSCachesSnapshot, nil},
		{api.MethodDNSKeysSnapshot, api.DNSKeysRequest{Cache: "resolver"}},
		{api.MethodDNSValueSnapshot, api.DNSValueRequest{Cache: "resolver", Key: "example"}},
	}
	for n, tt := range tests {
		resp, callErr := session.handleMessage(mustRequest(t, string(rune('1'+n)), api.SurfaceDNS, string(tt.method), tt.payload))
		if callErr != nil || resp.Error != nil {
			t.Fatalf("%s response = %#v, error = %v", tt.method, resp, callErr)
		}
	}

	bad := []api.Message{
		mustRequest(t, "cache", api.SurfaceDNS, string(api.MethodDNSKeysSnapshot), api.DNSKeysRequest{Cache: ""}),
		mustRequest(t, "value", api.SurfaceDNS, string(api.MethodDNSValueSnapshot), api.DNSValueRequest{Cache: "resolver", Key: ""}),
		mustRequest(t, "method", api.SurfaceDNS, "unknown", nil),
		{Kind: api.MessageRequest, ID: "payload", Surface: api.SurfaceDNS, Method: string(api.MethodDNSKeysSnapshot), Payload: json.RawMessage(`{`)},
	}
	for _, req := range bad {
		resp, callErr := session.handleMessage(req)
		if callErr != nil || resp.Error == nil {
			t.Fatalf("bad request %s response = %#v, error = %v", req.ID, resp, callErr)
		}
	}
	unavailable := (&AdminAPI{}).newAPISession(nil, nil, nil)
	resp, err := unavailable.handleMessage(mustRequest(t, "unavailable", api.SurfaceDNS, string(api.MethodDNSCachesSnapshot), nil))
	if err != nil || resp.Error == nil || resp.Error.Code != api.ErrorUnavailable {
		t.Fatalf("unavailable response = %#v, error = %v", resp, err)
	}
}

func TestDNSRecordConversionsCoverSupportedTypes(t *testing.T) {
	tests := []struct {
		typ   string
		value string
	}{
		{"A", "192.0.2.1"}, {"AAAA", "2001:db8::1"},
		{"CNAME", "alias.example"}, {"NS", "ns.example."}, {"PTR", "ptr.example"},
		{"TXT", "text value"}, {"MX", "10 mail.example"}, {"SRV", "1 2 443 service.example"},
	}
	for _, tt := range tests {
		record, key, err := dnsStoredEntryFromAPI(api.DNSRecordInput{Name: "example.test", Type: tt.typ, Class: "IN", TTL: 60, Value: tt.value}, time.Unix(100, 0))
		if err != nil || key == "" || record.msg == nil || len(record.msg.Answers) != 1 {
			t.Errorf("%s conversion = %#v, %q, %v", tt.typ, record, key, err)
		}
	}
	for _, typ := range []string{"A", "AAA", "CNAME", "NS", "PTR", "MX", "TXT", "SRV"} {
		if _, err := dnsTypeFromAPI(typ); err != nil {
			t.Errorf("dnsTypeFromAPI(%q): %v", typ, err)
		}
	}
	for _, class := range []string{"", "IN", "CH", "HS", "255"} {
		if _, err := dnsClassFromAPI(class); err != nil {
			t.Errorf("dnsClassFromAPI(%q): %v", class, err)
		}
	}
	if value, err := parseDNSUint16API("65535", "value"); err != nil || value != 65535 {
		t.Fatalf("parseDNSUint16API = %d, %v", value, err)
	}
}

func TestDNSRecordConversionsRejectInvalidValues(t *testing.T) {
	tests := []struct {
		typ   uint16
		value string
	}{
		{gdns.TypeA, "2001:db8::1"}, {gdns.TypeAAAA, "192.0.2.1"},
		{gdns.TypeCNAME, ""},
		{gdns.TypeMX, "mail.example"}, {gdns.TypeMX, "bad mail.example"}, {gdns.TypeMX, "1 bad name"},
		{gdns.TypeSRV, "1 2 3"}, {gdns.TypeSRV, "bad 2 3 host"},
		{gdns.TypeSRV, "1 bad 3 host"}, {gdns.TypeSRV, "1 2 bad host"}, {gdns.TypeSRV, "1 2 3 bad name"},
		{9999, "value"},
	}
	for _, tt := range tests {
		if _, err := dnsRecordDataFromAPI(tt.typ, tt.value); err == nil {
			t.Errorf("dnsRecordDataFromAPI(%d, %q) succeeded", tt.typ, tt.value)
		}
	}
	for _, value := range []string{"unknown", "-1", "65536"} {
		if _, err := dnsClassFromAPI(value); err == nil {
			t.Errorf("dnsClassFromAPI(%q) succeeded", value)
		}
		if _, err := parseDNSUint16API(value, "value"); err == nil {
			t.Errorf("parseDNSUint16API(%q) succeeded", value)
		}
	}
	for _, record := range []api.DNSRecordInput{
		{Name: "example", Type: "unknown", TTL: 1},
		{Name: "example", Type: "A", Class: "bad", TTL: 1, Value: "192.0.2.1"},
		{Name: "example", Type: "A", TTL: 0, Value: "192.0.2.1"},
		{Name: "", Type: "A", TTL: 1, Value: "192.0.2.1"},
	} {
		if _, _, err := dnsStoredEntryFromAPI(record, time.Now()); err == nil {
			t.Errorf("dnsStoredEntryFromAPI(%#v) succeeded", record)
		}
	}
}

func TestDNSRecordNameValidationBoundaries(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	maxName := strings.Join([]string{label63, label63, label63, strings.Repeat("a", 61)}, ".") + "."
	tooLongName := strings.Join([]string{label63, label63, label63, strings.Repeat("a", 62)}, ".") + "."

	valid := []string{
		".",
		"example",
		"example.test.",
		"_sip._tcp.example.test.",
		"*.example.test.",
		"dash-label.example.",
		label63 + ".example.",
		maxName,
	}
	for _, name := range valid {
		if !validDNSRecordName(name) {
			t.Errorf("validDNSRecordName(%q) = false", name)
		}
	}

	invalid := []string{
		"",
		"bad name",
		"bad\tname",
		"bad\nname",
		"bad\rname",
		"bad\x00name",
		"bad\x7fname",
		"bad\\name",
		"münchen.example",
		".example",
		"example..test",
		"example.test..",
		strings.Repeat("a", 64) + ".example.",
		tooLongName,
	}
	for _, name := range invalid {
		if validDNSRecordName(name) {
			t.Errorf("validDNSRecordName(%q) = true", name)
		}
	}

	// Cache names and composite cache keys are identifiers, not DNS names.
	// Their existing UTF-8 validation must remain independent.
	if !validDNSName("cache with spaces") || !validDNSName("example.test.|1|1") {
		t.Fatal("DNS cache identifier validation was made too restrictive")
	}
}

func TestDNSRecordNameRegressionAcrossNameBearingRecords(t *testing.T) {
	invalidNames := []string{
		"bad name",
		"bad..name",
		"bad\\name",
		"münchen.example",
		strings.Repeat("a", 64) + ".example",
	}
	types := []struct {
		name  string
		typ   uint16
		value func(string) string
	}{
		{"CNAME", gdns.TypeCNAME, func(name string) string { return name }},
		{"NS", gdns.TypeNS, func(name string) string { return name }},
		{"PTR", gdns.TypePTR, func(name string) string { return name }},
		{"MX", gdns.TypeMX, func(name string) string { return "10 " + name }},
		{"SRV", gdns.TypeSRV, func(name string) string { return "1 2 443 " + name }},
	}
	for _, recordType := range types {
		t.Run(recordType.name, func(t *testing.T) {
			for _, name := range invalidNames {
				_, err := dnsRecordDataFromAPI(recordType.typ, recordType.value(name))
				if err == nil {
					t.Errorf("accepted invalid name %q", name)
				}
			}
			if _, err := dnsRecordDataFromAPI(recordType.typ, recordType.value(".")); err != nil {
				t.Errorf("rejected root name: %v", err)
			}
			if _, err := dnsRecordDataFromAPI(recordType.typ, recordType.value("_service.example.")); err != nil {
				t.Errorf("rejected general DNS label: %v", err)
			}
		})
	}
}

func TestDNSRecordOwnerNameRegression(t *testing.T) {
	for _, name := range []string{
		"bad name",
		"bad..name",
		"bad\\name",
		"münchen.example",
		strings.Repeat("a", 64) + ".example",
	} {
		record := api.DNSRecordInput{Name: name, Type: "A", TTL: 60, Value: "192.0.2.1"}
		if _, _, err := dnsStoredEntryFromAPI(record, time.Now()); !errors.Is(err, errDNSInvalidName) {
			t.Errorf("owner %q error = %v, want invalid name", name, err)
		}
	}
	for _, name := range []string{".", "_service._tcp.example", "*.example"} {
		record := api.DNSRecordInput{Name: name, Type: "A", TTL: 60, Value: "192.0.2.1"}
		if _, _, err := dnsStoredEntryFromAPI(record, time.Now()); err != nil {
			t.Errorf("owner %q error = %v", name, err)
		}
	}
}

func TestDNSSetAPIRejectsInvalidRecordNamesWithoutStorageMutation(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	session := (&AdminAPI{dns: manager}).newAPISession(nil, nil, nil)

	requests := []api.DNSRecordInput{
		{Name: "bad owner", Type: "A", TTL: 60, Value: "192.0.2.1"},
		{Name: "example.test", Type: "CNAME", TTL: 60, Value: "bad target"},
		{Name: "example.test", Type: "MX", TTL: 60, Value: "10 bad..target"},
		{Name: "example.test", Type: "SRV", TTL: 60, Value: "1 2 443 bad\\target"},
	}
	for i, record := range requests {
		response, callErr := session.handleMessage(mustRequest(
			t,
			string(rune('1'+i)),
			api.SurfaceDNS,
			string(api.MethodDNSSet),
			api.DNSSetRequest{Cache: "resolver", Record: record},
		))
		if callErr != nil || response.Error == nil || response.Error.Code != api.ErrorBadRequest {
			t.Fatalf("record %#v response = %#v, err=%v", record, response, callErr)
		}
	}
	snapshot, err := manager.dnsKeysSnapshot("resolver")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Exists || len(snapshot.Keys) != 0 {
		t.Fatalf("invalid records mutated storage: %#v", snapshot)
	}
}

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

func TestDNSSubscriptionEventsDescribeTriggeringMutation(t *testing.T) {
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

	const (
		cache = "resolver"
		key   = "delayed.test.|16|1"
	)
	for id, request := range []api.Message{
		mustRequest(t, "keys", api.SurfaceDNS, string(api.MethodDNSSubscribeKeys), api.DNSKeysRequest{Cache: cache}),
		mustRequest(t, "value", api.SurfaceDNS, string(api.MethodDNSSubscribeValue), api.DNSValueRequest{Cache: cache, Key: key}),
	} {
		response, callErr := session.handleMessage(request)
		if callErr != nil || response.Error != nil {
			t.Fatalf("subscription %d response = %#v, error = %v", id, response, callErr)
		}
	}

	original, originalKey, err := dnsStoredEntryFromAPI(api.DNSRecordInput{
		Name: "delayed.test.", Type: "TXT", TTL: 1, Value: "original",
	}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if originalKey != key {
		t.Fatalf("original key = %q, want %q", originalKey, key)
	}
	originalRaw, err := marshalDNSCacheEntry(original)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw := append([]byte(nil), originalRaw...)
	upsert := newDNSKeyUpsertChange(cache, key, originalRaw, original.expires)
	for i := range originalRaw {
		originalRaw[i] = 'x'
	}

	replacement, replacementKey, err := dnsStoredEntryFromAPI(api.DNSRecordInput{
		Name: "delayed.test.", Type: "TXT", TTL: 3600, Value: "replacement",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if replacementKey != key {
		t.Fatalf("replacement key = %q, want %q", replacementKey, key)
	}
	replacementRaw, err := marshalDNSCacheEntry(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.putDNSCacheEntry(cache, key, replacementRaw); err != nil {
		t.Fatal(err)
	}

	manager.notifyDNSChange(upsert)
	keyEvent, valueEvent := decodeDNSMutationEvents(t, events)
	wantExpiry := original.expires.UTC().Format(time.RFC3339Nano)
	if keyEvent.Change.Deleted || keyEvent.Change.Entry == nil ||
		keyEvent.Change.Entry.Key != key ||
		keyEvent.Change.Entry.Size != len(wantRaw) ||
		keyEvent.Change.Entry.ExpiresAt != wantExpiry {
		t.Fatalf("delayed key upsert = %#v, want original committed metadata", keyEvent)
	}
	if !valueEvent.Found || valueEvent.Value == nil ||
		valueEvent.Value.JSON != string(wantRaw) ||
		valueEvent.Value.Size != len(wantRaw) ||
		valueEvent.Value.ExpiresAt != wantExpiry {
		t.Fatalf("delayed value upsert = %#v, want original committed value", valueEvent)
	}

	// A delete event must stay a delete even if the key was recreated before a
	// delayed subscriber handles it.
	events = nil
	manager.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: cache, key: key})
	keyEvent, valueEvent = decodeDNSMutationEvents(t, events)
	if !keyEvent.Change.Deleted || keyEvent.Change.Entry != nil {
		t.Fatalf("delayed key delete = %#v, want deleted key", keyEvent)
	}
	if valueEvent.Found || valueEvent.Value != nil {
		t.Fatalf("delayed value delete = %#v, want missing value", valueEvent)
	}

	// This is the failure mode from the E2E test: the short-lived committed
	// value is already absent when its delayed creation event is delivered.
	deleted, err := manager.deleteDNSCacheEntry(cache, key, replacementRaw)
	if err != nil || !deleted {
		t.Fatalf("delete replacement = %v, %v", deleted, err)
	}
	events = nil
	manager.notifyDNSChange(upsert)
	keyEvent, valueEvent = decodeDNSMutationEvents(t, events)
	if keyEvent.Change.Deleted || keyEvent.Change.Entry == nil {
		t.Fatalf("expired key upsert = %#v, want creation event", keyEvent)
	}
	if !valueEvent.Found || valueEvent.Value == nil || valueEvent.Value.JSON != string(wantRaw) {
		t.Fatalf("expired value upsert = %#v, want committed value", valueEvent)
	}
}

func decodeDNSMutationEvents(t *testing.T, events []api.Message) (api.DNSKeysEvent, api.DNSValueEvent) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("DNS mutation events = %d, want key and value events: %#v", len(events), events)
	}
	var (
		keyEvent   api.DNSKeysEvent
		valueEvent api.DNSValueEvent
		keyFound   bool
		valueFound bool
	)
	for _, event := range events {
		switch event.Method {
		case string(api.EventDNSKeysUpdate):
			if err := json.Unmarshal(event.Payload, &keyEvent); err != nil {
				t.Fatal(err)
			}
			keyFound = true
		case string(api.EventDNSValueUpdate):
			if err := json.Unmarshal(event.Payload, &valueEvent); err != nil {
				t.Fatal(err)
			}
			valueFound = true
		default:
			t.Fatalf("unexpected DNS event method %q", event.Method)
		}
	}
	if !keyFound || !valueFound {
		t.Fatalf("DNS mutation events missing key or value event: %#v", events)
	}
	return keyEvent, valueEvent
}

func TestDNSMutationMethodsUseReadableRecords(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	session := (&AdminAPI{dns: manager}).newAPISession(nil, nil, nil)
	defer session.close()

	setReq := mustRequest(t, "1", api.SurfaceDNS, string(api.MethodDNSSet), api.DNSSetRequest{
		Cache: "manual",
		Record: api.DNSRecordInput{
			Name:  "example.test.",
			Type:  "A",
			Class: "IN",
			TTL:   60,
			Value: "192.0.2.44",
		},
	})
	resp, err := session.handleMessage(setReq)
	if err != nil {
		t.Fatal(err)
	}
	setResp, err := api.DecodePayload[api.DNSSetResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if setResp.Key != "example.test.|1|1" || !setResp.Snapshot.Found || setResp.Snapshot.Value == nil {
		t.Fatalf("set response = %#v, want created A record", setResp)
	}

	snapshot, err := manager.dnsKeysSnapshot("manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Keys) != 1 || snapshot.Keys[0].Key != "example.test.|1|1" {
		t.Fatalf("keys after set = %#v, want A key", snapshot.Keys)
	}

	editReq := mustRequest(t, "2", api.SurfaceDNS, string(api.MethodDNSSet), api.DNSSetRequest{
		Cache: "manual",
		Key:   setResp.Key,
		Record: api.DNSRecordInput{
			Name:  "example.test.",
			Type:  "AAAA",
			Class: "IN",
			TTL:   120,
			Value: "2001:db8::44",
		},
	})
	resp, err = session.handleMessage(editReq)
	if err != nil {
		t.Fatal(err)
	}
	editResp, err := api.DecodePayload[api.DNSSetResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if editResp.Key != "example.test.|28|1" {
		t.Fatalf("edit key = %q, want AAAA key", editResp.Key)
	}
	snapshot, err = manager.dnsKeysSnapshot("manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Keys) != 1 || snapshot.Keys[0].Key != editResp.Key {
		t.Fatalf("keys after edit = %#v, want only new key", snapshot.Keys)
	}

	deleteReq := mustRequest(t, "3", api.SurfaceDNS, string(api.MethodDNSDelete), api.DNSValueRequest{
		Cache: "manual",
		Key:   editResp.Key,
	})
	if _, err := session.handleMessage(deleteReq); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.dnsKeysSnapshot("manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Keys) != 0 {
		t.Fatalf("keys after delete = %#v, want empty cache", snapshot.Keys)
	}

	if _, err := session.handleMessage(setReq); err != nil {
		t.Fatal(err)
	}
	dropReq := mustRequest(t, "4", api.SurfaceDNS, string(api.MethodDNSDrop), api.DNSKeysRequest{Cache: "manual"})
	if _, err := session.handleMessage(dropReq); err != nil {
		t.Fatal(err)
	}
	caches, err := manager.dnsCachesSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(caches.Caches) != 0 {
		t.Fatalf("caches after drop = %#v, want none", caches.Caches)
	}
}
