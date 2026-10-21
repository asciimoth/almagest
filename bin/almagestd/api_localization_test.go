package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	"github.com/asciimoth/almagest/bin/api"
)

func TestAPISessionLocaleIsIndependentAndFallsBackPerKey(t *testing.T) {
	root := t.TempDir()
	writeLocaleTestFile(t, root, "en", "manifest.json", `{"format":1,"locale":"en","name":"English","development_only":false}`)
	writeLocaleTestFile(t, root, "en", "panel.json", `{"common":"English","english":"Only English"}`)
	writeLocaleTestFile(t, root, "en", "menus.json", `{}`)
	writeLocaleTestFile(t, root, "lorem", "manifest.json", `{"format":1,"locale":"lorem","name":"Lorem","development_only":true}`)
	writeLocaleTestFile(t, root, "lorem", "panel.json", `{"common":"Lorem"}`)
	writeLocaleTestFile(t, root, "lorem", "menus.json", `{}`)
	reader, err := localizedcontent.New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := &AdminAPI{localizedContent: reader}
	english := admin.newAPISession(nil, nil, nil)
	lorem := admin.newAPISession(nil, nil, nil)

	request, err := api.NewRequest("1", api.SurfaceAdmin, string(api.MethodAdminSetLocale), api.SetLocaleRequest{Locale: "lorem"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := lorem.handleMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot api.LocaleSnapshot
	if err := json.Unmarshal(response.Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Requested != "lorem" || snapshot.Effective != "lorem" || snapshot.Panel["common"] != "Lorem" || snapshot.Panel["english"] != "Only English" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if english.currentLocale() != "en" || lorem.currentLocale() != "lorem" {
		t.Fatalf("session locales = %q, %q", english.currentLocale(), lorem.currentLocale())
	}

	bad, err := api.NewRequest("2", api.SurfaceAdmin, string(api.MethodAdminSetLocale), api.SetLocaleRequest{Locale: "../escape"})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := lorem.handleMessage(bad); err != nil || response.Error == nil || response.Error.Code != api.ErrorBadRequest {
		t.Fatalf("invalid locale response = %#v, %v", response, err)
	}
}

func TestPrepareLocalizedContentAllowsMissingOverride(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-installed")
	t.Setenv("ALMAGEST_LOCALES_DIR", root)
	admin := &AdminAPI{}
	if err := admin.prepareLocalizedContent(filepath.Join(t.TempDir(), "default")); err != nil {
		t.Fatalf("missing locale override stopped startup: %v", err)
	}
	if admin.localizedContent != nil {
		t.Fatal("missing locale override installed a reader")
	}
}

func TestPrepareLocalizedContentAllowsLookupFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALMAGEST_LOCALES_DIR", filepath.Join(parent, "locales"))
	admin := &AdminAPI{}
	if err := admin.prepareLocalizedContent(filepath.Join(t.TempDir(), "default")); err != nil {
		t.Fatalf("locale lookup failure stopped startup: %v", err)
	}
	if admin.localizedContent != nil {
		t.Fatal("failed locale lookup installed a reader")
	}
}

func writeLocaleTestFile(t *testing.T, root, locale, name, contents string) {
	t.Helper()
	path := filepath.Join(root, locale, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
