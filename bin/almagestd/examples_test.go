package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
)

func TestExampleStoreListsLocalizedMetadataAndPreservesText(t *testing.T) {
	root := t.TempDir()
	writeExampleTestFile(t, root, "hello/manifest.json", `{
		"format":1,
		"name":"<img src=x onerror=alert(1)>",
		"comment":"Default comment",
		"translations":{"ka":{"name":"გამარჯობა","comment":"ქართული აღწერა"}}
	}`)
	writeExampleTestFile(t, root, "hello/config.hujson", `{}`)
	store, err := openExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	listed, err := store.List("ka")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Example != "hello" || listed[0].Name != "გამარჯობა" || listed[0].Comment != "ქართული აღწერა" || listed[0].UUID != "" || listed[0].Timestamp != 0 {
		t.Fatalf("localized examples = %#v", listed)
	}
	listed, err = store.List("en")
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].Name != "<img src=x onerror=alert(1)>" || listed[0].Comment != "Default comment" {
		t.Fatalf("default examples = %#v", listed)
	}
}

func TestExampleStoreListsExamplesByDirectoryName(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"003-third", "001-first", "002-second"} {
		writeExampleTestFile(t, root, id+"/manifest.json", `{"format":1,"name":"Name","comment":"Comment"}`)
	}
	store, err := openExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	listed, err := store.List("en")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"001-first", "002-second", "003-third"}
	if len(listed) != len(want) {
		t.Fatalf("examples = %#v, want identifiers %q", listed, want)
	}
	for i := range want {
		if listed[i].Example != want[i] {
			t.Fatalf("examples[%d].Example = %q, want %q", i, listed[i].Example, want[i])
		}
	}
}

func TestExampleStoreRejectsUnsafeAndInvalidContent(t *testing.T) {
	for _, test := range []struct {
		name     string
		manifest string
		prepare  func(*testing.T, string)
	}{
		{name: "unknown manifest field", manifest: `{"format":1,"name":"Name","comment":"Comment","extra":true}`},
		{name: "noncanonical locale", manifest: `{"format":1,"name":"Name","comment":"Comment","translations":{"EN-us":{"name":"Bad"}}}`},
		{name: "blank translation", manifest: `{"format":1,"name":"Name","comment":"Comment","translations":{"en":{"name":" "}}}`},
		{name: "empty comment", manifest: `{"format":1,"name":"Name","comment":""}`},
		{name: "symlink file", manifest: `{"format":1,"name":"Name","comment":"Comment"}`, prepare: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "sample", "files"), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			makeExampleTestSymlink(t, target, filepath.Join(root, "sample", "files", "escape"))
		}},
		{name: "excluded snapshot storage", manifest: `{"format":1,"name":"Name","comment":"Comment"}`, prepare: func(t *testing.T, root string) {
			writeExampleTestFile(t, root, "sample/files/snapshots/host-state", "forbidden")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeExampleTestFile(t, root, "sample/manifest.json", test.manifest)
			writeExampleTestFile(t, root, "sample/config.hujson", `{}`)
			if test.prepare != nil {
				test.prepare(t, root)
			}
			store, err := openExampleStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close() //nolint:errcheck
			if test.prepare == nil {
				if _, err := store.List("en"); !errors.Is(err, errSnapshotInvalid) {
					t.Fatalf("List error = %v, want invalid snapshot", err)
				}
				return
			}
			service, _, _, _, _ := newSnapshotTestService(t, false)
			service.examples = store
			if _, err := service.loadExample(context.Background(), "sample", "en"); !errors.Is(err, errSnapshotInvalid) {
				t.Fatalf("loadExample error = %v, want invalid snapshot", err)
			}
		})
	}
	for _, id := range []string{"", "../escape", "Uppercase", "-leading", "with.dot", "con", "nul", "com1"} {
		if err := validateExampleID(id); !errors.Is(err, errSnapshotInvalid) {
			t.Errorf("validateExampleID(%q) = %v", id, err)
		}
	}
}

func makeExampleTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		// Ordinary Windows accounts need Developer Mode or
		// SeCreateSymbolicLinkPrivilege to construct this test fixture.
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
			t.Skipf("symlink rejection assertion is unavailable without Windows symlink privilege: %v", err)
		}
		t.Fatal(err)
	}
}

func TestExampleLoadUsesTransactionalSnapshotRestore(t *testing.T) {
	service, _, engine, _, storagePath := newSnapshotTestService(t, false)
	root := t.TempDir()
	writeExampleTestFile(t, root, "minimal/manifest.json", `{"format":1,"name":"Minimal","comment":"No stored state."}`)
	writeExampleTestFile(t, root, "minimal/config.hujson", `{"nodes":{"example-node":{"Class":"test/AdminAPIBatchNode"}}}`)
	store, err := openExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service.examples = store

	loaded, err := service.loadExample(context.Background(), "minimal", "en")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(loaded.filesPath, kvDBFileName)); err != nil {
		loaded.cleanup()
		t.Fatalf("staged empty database is absent: %v", err)
	}
	if entries, err := os.ReadDir(loaded.filesPath); err != nil || len(entries) != 1 || entries[0].Name() != kvDBFileName {
		loaded.cleanup()
		t.Fatalf("staged files = %#v, err=%v", entries, err)
	}
	loaded.cleanup()

	response, err := service.restore.RestoreExample(context.Background(), "minimal", "en")
	if err != nil {
		t.Fatal(err)
	}
	if response.Snapshot.Example != "minimal" || response.Snapshot.UUID != "" || response.Snapshot.Timestamp != 0 || !response.ConfigWritten {
		t.Fatalf("restore response = %#v", response)
	}
	if _, ok := engine.NodeByName(almagest.MainWorkspace, "example-node"); !ok {
		t.Fatal("example graph node is missing")
	}
	if _, ok := engine.NodeByName(almagest.MainWorkspace, "saved-node"); ok {
		t.Fatal("old graph node survived example load")
	}
	if _, err := os.Stat(filepath.Join(storagePath, "tls-builtin-ca", "ca.pem")); err != nil {
		t.Fatalf("immutable TLS state was not reconstructed: %v", err)
	}
}

func TestExamplesShareSnapshotListAndRestoreAPI(t *testing.T) {
	service, _, engine, _, _ := newSnapshotTestService(t, false)
	root := t.TempDir()
	writeExampleTestFile(t, root, "api-example/manifest.json", `{"format":1,"name":"API Example","comment":"API comment"}`)
	writeExampleTestFile(t, root, "api-example/config.hujson", `{"nodes":{"api-node":{"Class":"test/AdminAPIBatchNode"}}}`)
	store, err := openExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service.examples = store
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)

	list := mustRequest(t, "list", api.SurfaceSnapshots, string(api.MethodSnapshotsList), struct{}{})
	message, err := session.handleMessage(list)
	if err != nil || message.Error != nil {
		t.Fatalf("list response = %#v, err=%v", message, err)
	}
	var response api.SnapshotsListResponse
	if err := json.Unmarshal(message.Result, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Snapshots) != 1 || response.Snapshots[0].Example != "api-example" {
		t.Fatalf("list response = %#v", response)
	}

	for _, request := range []api.SnapshotsRestoreRequest{
		{},
		{UUID: "11111111-1111-4111-8111-111111111111", Example: "api-example"},
		{Example: "../escape"},
	} {
		message, err := session.handleMessage(mustRequest(t, "bad", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), request))
		if err != nil || message.Error == nil || message.Error.Code != api.ErrorInvalidPayload {
			t.Fatalf("invalid restore response = %#v, err=%v", message, err)
		}
	}
	message, err = session.handleMessage(mustRequest(t, "load", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotsRestoreRequest{Example: "api-example"}))
	if err != nil || message.Error != nil {
		t.Fatalf("load response = %#v, err=%v", message, err)
	}
}

func TestSnapshotListWorksWithoutExamplesStore(t *testing.T) {
	service, _, engine, _, _ := newSnapshotTestService(t, false)
	session := (&AdminAPI{snapshots: service}).newAPISession(engine, nil, nil)
	message, err := session.handleMessage(mustRequest(t, "list", api.SurfaceSnapshots, string(api.MethodSnapshotsList), struct{}{}))
	if err != nil || message.Error != nil {
		t.Fatalf("list response = %#v, err=%v", message, err)
	}
	var response api.SnapshotsListResponse
	if err := json.Unmarshal(message.Result, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Snapshots) != 0 {
		t.Fatalf("list response = %#v", response)
	}
}

func TestExampleStoreRejectsLinkedMetadataAndConfig(t *testing.T) {
	for _, name := range []string{exampleManifestName, snapshotConfigName} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeExampleTestFile(t, root, "shared", `{"format":1,"name":"Name","comment":"Comment"}`)
			writeExampleTestFile(t, root, "sample/manifest.json", `{"format":1,"name":"Name","comment":"Comment"}`)
			writeExampleTestFile(t, root, "sample/config.hujson", `{}`)
			if err := os.Remove(filepath.Join(root, "sample", name)); err != nil {
				t.Fatal(err)
			}
			makeExampleTestSymlink(t, filepath.Join(root, "shared"), filepath.Join(root, "sample", name))
			store, err := openExampleStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close() //nolint:errcheck
			if name == exampleManifestName {
				if _, err := store.List("en"); !errors.Is(err, errSnapshotInvalid) {
					t.Fatalf("List error = %v, want invalid snapshot", err)
				}
				return
			}
			service, _, _, _, _ := newSnapshotTestService(t, false)
			service.examples = store
			if _, err := service.loadExample(context.Background(), "sample", "en"); !errors.Is(err, errSnapshotInvalid) {
				t.Fatalf("loadExample error = %v, want invalid snapshot", err)
			}
		})
	}
}

func TestExampleStoreRejectsLinkedExampleAndFilesDirectories(t *testing.T) {
	for _, name := range []string{"example", "files"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeExampleTestFile(t, root, "shared/manifest.json", `{"format":1,"name":"Name","comment":"Comment"}`)
			writeExampleTestFile(t, root, "shared/config.hujson", `{}`)
			if name == "example" {
				makeExampleTestSymlink(t, filepath.Join(root, "shared"), filepath.Join(root, "sample"))
			} else {
				writeExampleTestFile(t, root, "sample/manifest.json", `{"format":1,"name":"Name","comment":"Comment"}`)
				writeExampleTestFile(t, root, "sample/config.hujson", `{}`)
				makeExampleTestSymlink(t, filepath.Join(root, "shared"), filepath.Join(root, "sample", "files"))
			}
			store, err := openExampleStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close() //nolint:errcheck
			service, _, _, _, _ := newSnapshotTestService(t, false)
			service.examples = store
			if _, err := service.loadExample(context.Background(), "sample", "en"); !errors.Is(err, errSnapshotInvalid) {
				t.Fatalf("loadExample error = %v, want invalid snapshot", err)
			}
		})
	}
}

func TestExamplesRootOverrideValidation(t *testing.T) {
	t.Setenv("ALMAGEST_EXAMPLES_DIR", "relative")
	if _, err := resolveExamplesRoot(t.TempDir()); err == nil {
		t.Fatal("relative override succeeded")
	}
	root := t.TempDir()
	t.Setenv("ALMAGEST_EXAMPLES_DIR", root)
	if resolved, err := resolveExamplesRoot("/default"); err != nil || resolved != root {
		t.Fatalf("resolved root = %q, err=%v", resolved, err)
	}
}

func TestRepositoryExamplesAreMinimalAndValid(t *testing.T) {
	exampleIDs := []string{
		"001-hello-network",
		"002-mock-network",
		"003-select-networks",
		"004-remap-service",
		"005-route-network-operations",
		"006-filter-network-operations",
		"007-local-socks-access",
		"008-socks-client-and-server",
		"009-inspect-and-route-traffic",
		"010-terminate-tls",
		"011-wrap-connection-in-tls",
		"012-intercept-selected-tls",
		"013-static-dns-records",
		"014-dns-fallback-and-cache",
		"015-dns-client-and-server",
		"016-route-dns-questions",
		"017-inject-and-export-dns",
		"018-mesh-names-with-fallback",
		"019-virtual-ip-stacks",
		"020-tun-to-network",
		"021-filter-ip-packets",
		"022-split-ip-packets",
		"023-join-tun-paths",
		"024-strip-tun-configuration",
		"025-route-host-through-almagest",
		"026-create-host-tun",
		"027-handle-tun-dns",
		"028-wireguard-isolated-lab",
		"029-manage-wireguard-peers",
		"030-split-tun-by-advertised-routes",
		"031-join-tailscale-network",
		"032-browse-through-tor",
		"033-check-public-address",
		"034-complete-dns-policy-chain",
		"035-wireguard-through-local-socks",
		"036-wireguard-over-socks",
		"037-switch-wireguard-destination",
		"038-nested-wireguard",
		"039-route-host-tun-egress",
	}
	root := filepath.Join("..", "..", "examples")
	store, err := openExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	listed, err := store.List("en")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(exampleIDs) {
		t.Fatalf("repository examples = %#v", listed)
	}
	for i, exampleID := range exampleIDs {
		if listed[i].Example != exampleID {
			t.Fatalf("repository examples = %#v", listed)
		}
		entries, err := os.ReadDir(filepath.Join(root, exampleID))
		if err != nil {
			t.Fatal(err)
		}
		wantEntries := []string{snapshotConfigName, exampleManifestName}
		if exampleID == "028-wireguard-isolated-lab" ||
			exampleID == "029-manage-wireguard-peers" ||
			exampleID == "030-split-tun-by-advertised-routes" ||
			exampleID == "035-wireguard-through-local-socks" ||
			exampleID == "036-wireguard-over-socks" ||
			exampleID == "037-switch-wireguard-destination" ||
			exampleID == "038-nested-wireguard" ||
			exampleID == "039-route-host-tun-egress" {
			wantEntries = []string{snapshotConfigName, "files", exampleManifestName}
			files, err := os.ReadDir(filepath.Join(root, exampleID, "files"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || files[0].Name() != kvDBFileName {
				t.Fatalf("%s stored files = %#v", exampleID, files)
			}
			if err := validateBboltDatabase(filepath.Join(root, exampleID, "files", kvDBFileName)); err != nil {
				t.Fatalf("%s bbolt database: %v", exampleID, err)
			}
		}
		if len(entries) != len(wantEntries) {
			t.Fatalf("%s files = %#v", exampleID, entries)
		}
		for entryIndex, wantEntry := range wantEntries {
			if entries[entryIndex].Name() != wantEntry {
				t.Fatalf("%s files = %#v", exampleID, entries)
			}
		}
		configData, err := readExampleFile(store.root, exampleID+"/config.hujson", snapshotConfigMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		for _, generatedDefault := range [][]byte{[]byte(`"body"`), []byte(`"method"`), []byte(`"tlsConfig"`)} {
			if bytes.Contains(configData, generatedDefault) {
				t.Fatalf("%s config contains generated default %s", exampleID, generatedDefault)
			}
		}
		if _, err := parseEditableConfig(configData); err != nil {
			t.Fatalf("%s config: %v", exampleID, err)
		}
	}
}

func writeExampleTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
