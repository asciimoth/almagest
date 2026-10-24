package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
)

func TestWriteWebdemoNodeClassCache(t *testing.T) {
	var output bytes.Buffer
	if err := writeWebdemoNodeClassCache(&output); err != nil {
		t.Fatal(err)
	}

	var cache webdemoNodeClassCache
	if err := json.Unmarshal(output.Bytes(), &cache); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if cache.Version != webdemoNodeClassCacheVersion {
		t.Fatalf("cache version = %d, want %d", cache.Version, webdemoNodeClassCacheVersion)
	}
	if len(cache.ColorMap) == 0 {
		t.Fatal("cache color map is empty")
	}
	if _, ok := cache.Classes[tunnodes.NodeTypeTun]; !ok {
		t.Fatalf("cache does not contain production class %q", tunnodes.NodeTypeTun)
	}
	if _, ok := cache.Classes[NodeTypeProtectedCommand]; !ok {
		t.Fatalf("cache does not contain daemon class %q", NodeTypeProtectedCommand)
	}
	if len(cache.Order) != len(cache.Classes) {
		t.Fatalf("cache order contains %d classes, want %d", len(cache.Order), len(cache.Classes))
	}
	seen := make(map[string]bool, len(cache.Order))
	for index, name := range cache.Order {
		_, exists := cache.Classes[name]
		if seen[name] || !exists {
			t.Fatalf("cache order entry %d is invalid or repeated: %q", index, name)
		}
		seen[name] = true
		if index > 0 {
			left := cache.Classes[cache.Order[index-1]]
			right := cache.Classes[name]
			if api.CompareNodeClassSnapshots(
				api.NodeClassSnapshot{Class: cache.Order[index-1], PrimaryType: left.PrimaryType},
				api.NodeClassSnapshot{Class: name, PrimaryType: right.PrimaryType},
			) > 0 {
				t.Fatalf("cache order is not canonical at %q and %q", cache.Order[index-1], name)
			}
		}
	}
	for name := range cache.Classes {
		if name == "" {
			t.Fatal("cache contains an empty class name")
		}
	}

	var raw struct {
		Classes map[string]map[string]json.RawMessage `json:"classes"`
	}
	if err := json.Unmarshal(output.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw cache: %v", err)
	}
	for name, class := range raw.Classes {
		if len(class) != 2 || class["primary_type"] == nil || class["initial_ports"] == nil {
			t.Fatalf("cache class %q has non-preview fields: %v", name, class)
		}
	}
}

func TestTrackedWebdemoNodeClassCacheIsCurrent(t *testing.T) {
	var output bytes.Buffer
	if err := writeWebdemoNodeClassCache(&output); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../webdemo/node-classes.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatal("webdemo/node-classes.json is stale; run just update-webdemo-node-classes")
	}
}
