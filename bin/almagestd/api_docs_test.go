package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestDocsAPIFiltersRuntimeClassesAndServesLocalizedHTTP(t *testing.T) {
	root := t.TempDir()
	for _, locale := range []string{"en", "lorem"} {
		development := "false"
		if locale == "lorem" {
			development = "true"
		}
		writeLocaleTestFile(t, root, locale, "manifest.json", `{"format":1,"locale":"`+locale+`","name":"`+locale+`","development_only":`+development+`}`)
		writeLocaleTestFile(t, root, locale, "panel.json", `{}`)
		writeLocaleTestFile(t, root, locale, "menus.json", `{}`)
		shortName := "Admin API batch node"
		name := "Nodes"
		if locale == "lorem" {
			name = "Nodi"
			shortName = "Nodus administrativus"
		}
		writeLocaleTestFile(t, root, locale, "nodes.json", `{"test/AdminAPIBatchNode":"`+shortName+`"}`)
		writeLocaleTestFile(t, root, locale, "docs/index.json", `{"topics":[{"id":"nodes","name":"`+name+`","folded":false,"node_pages":true}]}`)
	}
	writeLocaleTestFile(t, root, "en", "docs/pages/nodes/test/AdminAPIBatchNode.md", "English documentation")
	writeLocaleTestFile(t, root, "lorem", "docs/pages/nodes/test/AdminAPIBatchNode.md", "Lorem documentation")
	reader, err := localizedcontent.New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(16), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{
			adminAPIBatchNodeClass{},
			adminAPINodeClass{name: "test/Alpha", primaryType: "test/z"},
			adminAPINodeClass{name: "test/Zeta", primaryType: "test/a"},
			debugDocsNodeClass{},
		}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	admin := &AdminAPI{localizedContent: reader}
	session := admin.newAPISession(engine, nil, nil)
	setLocale := mustRequest(t, "locale", api.SurfaceAdmin, string(api.MethodAdminSetLocale), api.SetLocaleRequest{Locale: "lorem"})
	if response, err := session.handleMessage(setLocale); err != nil || response.Error != nil {
		t.Fatalf("set locale response = %#v, %v", response, err)
	}
	treeMessage := mustRequest(t, "tree", api.SurfaceDocs, string(api.MethodDocsTree), nil)
	treeResponse, err := session.handleMessage(treeMessage)
	if err != nil || treeResponse.Error != nil {
		t.Fatalf("tree response = %#v, %v", treeResponse, err)
	}
	tree, err := api.DecodePayload[api.DocsTreeResponse](treeResponse.Result)
	foundTestClass := false
	foundDebugClass := false
	pageIDs := make([]string, 0, len(tree.Topics[0].Pages))
	for _, page := range tree.Topics[0].Pages {
		pageIDs = append(pageIDs, page.ID)
		foundTestClass = foundTestClass || page.ID == "node/test/AdminAPIBatchNode" && page.Name == "Nodus administrativus"
		foundDebugClass = foundDebugClass || page.ID == "node/test/DebugDocsNode"
	}
	if err != nil || tree.Locale != "lorem" || len(tree.Available) != 2 || tree.Available[1].Locale != "lorem" || len(tree.Topics) != 1 || tree.Topics[0].Name != "Nodi" || !foundTestClass || foundDebugClass {
		t.Fatalf("tree = %#v, %v", tree, err)
	}
	orderedClasses, _ := docsRuntimeClasses(engine)
	wantPageIDs := make([]string, 0, len(orderedClasses))
	for _, class := range orderedClasses {
		wantPageIDs = append(wantPageIDs, "node/"+class)
	}
	if !slices.Equal(pageIDs, wantPageIDs) {
		t.Fatalf("node documentation page order = %v, want node picker order %v", pageIDs, wantPageIDs)
	}
	wantTestPageIDs := []string{"node/test/Zeta", "node/test/Alpha", "node/test/AdminAPIBatchNode"}
	if len(pageIDs) < len(wantTestPageIDs) || !slices.Equal(pageIDs[len(pageIDs)-len(wantTestPageIDs):], wantTestPageIDs) {
		t.Fatalf("test node documentation page order = %v, want %v", pageIDs, wantTestPageIDs)
	}

	mux := http.NewServeMux()
	admin.registerAPIHandlers(t.Context(), mux, engine, nil, nil)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/docs/raw?locale=lorem&id=node%2Ftest%2FAdminAPIBatchNode", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: docsLocaleCookieName, Value: "en"})
	response, err := http.DefaultClient.Do(request) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close() //nolint:errcheck
	content, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/markdown") || string(content) != "Lorem documentation" {
		t.Fatalf("raw response = %s, %q, %q, %v", response.Status, response.Header.Get("Content-Type"), content, err)
	}
	localeCookie := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == docsLocaleCookieName {
			localeCookie = cookie.Value
		}
	}
	if localeCookie != "lorem" {
		t.Fatalf("locale cookie = %q, want lorem", localeCookie)
	}

	debugResponse, err := http.Get(server.URL + "/api/docs/raw?id=node%2Ftest%2FDebugDocsNode") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer debugResponse.Body.Close() //nolint:errcheck
	if debugResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("debug node documentation response = %s, want 404", debugResponse.Status)
	}

	cookieRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/docs/raw?id=node%2Ftest%2FAdminAPIBatchNode", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookieRequest.AddCookie(&http.Cookie{Name: docsLocaleCookieName, Value: "lorem"})
	cookieResponse, err := http.DefaultClient.Do(cookieRequest) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer cookieResponse.Body.Close() //nolint:errcheck
	cookieContent, err := io.ReadAll(cookieResponse.Body)
	if err != nil || cookieResponse.StatusCode != http.StatusOK || string(cookieContent) != "Lorem documentation" {
		t.Fatalf("cookie raw response = %s, %q, %v", cookieResponse.Status, cookieContent, err)
	}
}

type debugDocsNodeClass struct{ adminAPIBatchNodeClass }

func (debugDocsNodeClass) ClassName() string { return "test/DebugDocsNode" }
func (debugDocsNodeClass) ShortDescription() string {
	return "DEBUG documentation test node"
}
