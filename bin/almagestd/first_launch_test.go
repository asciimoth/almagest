package main

import (
	"context"
	"path/filepath"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
)

func TestFirstLaunchExample(t *testing.T) {
	for _, test := range []struct {
		name        string
		empty       bool
		launched    bool
		example     bool
		invalid     bool
		wantExample bool
	}{
		{name: "empty workspace", empty: true, example: true, wantExample: true},
		{name: "existing workspace", example: true},
		{name: "already launched", empty: true, launched: true, example: true},
		{name: "missing example", empty: true},
		{name: "invalid example", empty: true, example: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, manager, engine, _, storagePath := newSnapshotTestService(t, false)
			if test.empty {
				empty, err := parseEditableConfig([]byte(`{"nodes":{}}`))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := engine.ReconcileConfig(empty); err != nil {
					t.Fatal(err)
				}
				service.config.Main = empty
			}
			if test.launched {
				if err := manager.markLaunched(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if test.example {
				root := t.TempDir()
				writeExampleTestFile(t, root, firstLaunchExample+"/manifest.json", `{"format":1,"name":"Hello","comment":"Example"}`)
				config := `{"nodes":{"example-node":{"Class":"test/AdminAPIBatchNode"}}}`
				if test.invalid {
					config = `{"nodes":`
				}
				writeExampleTestFile(t, root, firstLaunchExample+"/config.hujson", config)
				if test.wantExample {
					writeExampleTestFile(t, root, firstLaunchExample+"/files/flags.json", `{"docs_opened":true}`)
				}
				store, err := openExampleStore(root)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				service.examples = store
			}
			if err := loadFirstLaunchExample(context.Background(), service, manager); err != nil {
				t.Fatal(err)
			}
			if _, got := engine.NodeByName(almagest.MainWorkspace, "example-node"); got != test.wantExample {
				t.Fatalf("example node present = %t, want %t", got, test.wantExample)
			}
			flags, err := readFlags(filepath.Join(storagePath, flagsFileName))
			if err != nil || !flags.LaunchedBefore {
				t.Fatalf("stored launch flag = %#v, err=%v", flags, err)
			}
			if test.wantExample && !flags.DocsOpened {
				t.Fatal("first-launch flag write lost restored flags")
			}
			if err := manager.reloadFlags(); err != nil || !manager.flagsSnapshot().LaunchedBefore {
				t.Fatalf("reloaded launch flag = %#v, err=%v", manager.flagsSnapshot(), err)
			}
		})
	}
}
