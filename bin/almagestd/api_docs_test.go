package main

import (
	"context"
	"encoding/json"
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
		panel := `{}`
		if locale == "lorem" {
			panel = `{"docs.node.unavailable":"Nodus hic non adest","docs.node.deprecated":"Nodus obsoletus est"}`
		}
		writeLocaleTestFile(t, root, locale, "panel.json", panel)
		writeLocaleTestFile(t, root, locale, "menus.json", `{}`)
		shortName := "Admin API batch node"
		name := "Nodes"
		if locale == "lorem" {
			name = "Nodi"
			shortName = "Nodus administrativus"
		}
		writeLocaleTestFile(t, root, locale, "nodes.json", `{"test/AdminAPIBatchNode":"`+shortName+`","test/DocumentedOnly":"Documented only","test/DebugDocsNode":"DEBUG documentation test node"}`)
		writeLocaleTestFile(t, root, locale, "docs/index.json", `{"topics":[{"id":"nodes","name":"`+name+`","folded":false,"node_pages":true}]}`)
	}
	writeLocaleTestFile(t, root, "en", "docs/pages/nodes/test/AdminAPIBatchNode.md", "English documentation")
	writeLocaleTestFile(t, root, "lorem", "docs/pages/nodes/test/AdminAPIBatchNode.md", "Lorem documentation")
	writeLocaleTestFile(t, root, "en", "docs/pages/nodes/test/DocumentedOnly.md", "Documentation without a runtime class")
	writeLocaleTestFile(t, root, "en", "docs/pages/nodes/test/DebugDocsNode.md", "Hidden debug documentation")
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
	admin := &AdminAPI{
		localizedContent: reader,
		experimentalNodeTypes: map[string]bool{
			"test/AdminAPIBatchNode": true,
		},
		deprecatedNodeTypes: map[string]bool{
			"test/AdminAPIBatchNode": true,
		},
	}
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
	foundDocumentedOnly := false
	foundDeprecated := false
	availablePageIDs := make([]string, 0, len(tree.Topics[0].Pages))
	for _, page := range tree.Topics[0].Pages {
		foundTestClass = foundTestClass || page.ID == "node/test/AdminAPIBatchNode" && page.Name == "Nodus administrativus"
		foundDeprecated = foundDeprecated || page.ID == "node/test/AdminAPIBatchNode" && slices.Contains(page.Marks, api.DocsPageMarkDeprecated)
		foundDebugClass = foundDebugClass || page.ID == "node/test/DebugDocsNode"
		foundDocumentedOnly = foundDocumentedOnly || page.ID == "node/test/DocumentedOnly" && page.Name == "Documented only" && page.Available != nil && !*page.Available && slices.Contains(page.Marks, api.DocsPageMarkUnavailable)
		if page.Available != nil && *page.Available {
			availablePageIDs = append(availablePageIDs, page.ID)
		}
	}
	if err != nil || tree.Locale != "lorem" || tree.NodeUnavailableMessage != "Nodus hic non adest" ||
		tree.MarkMessages[api.DocsPageMarkUnavailable] != "Nodus hic non adest" ||
		tree.MarkMessages[api.DocsPageMarkDeprecated] != "Nodus obsoletus est" ||
		len(tree.Available) != 2 || tree.Available[1].Locale != "lorem" || len(tree.Topics) != 1 ||
		tree.Topics[0].Name != "Nodi" || !foundTestClass || !foundDeprecated || !foundDocumentedOnly || foundDebugClass {
		t.Fatalf("tree = %#v, %v", tree, err)
	}
	orderedClasses, _ := docsRuntimeClasses(engine)
	wantPageIDs := make([]string, 0, len(orderedClasses))
	for _, class := range orderedClasses {
		wantPageIDs = append(wantPageIDs, "node/"+class)
	}
	if !slices.Equal(availablePageIDs, wantPageIDs) {
		t.Fatalf("available node documentation page order = %v, want node picker order %v", availablePageIDs, wantPageIDs)
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

	previewResponse, err := http.Get(server.URL + "/api/docs/node-class?locale=lorem&id=node%2Ftest%2FAdminAPIBatchNode") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer previewResponse.Body.Close() //nolint:errcheck
	var preview api.DocsNodeClassResponse
	if err := json.NewDecoder(previewResponse.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if previewResponse.StatusCode != http.StatusOK ||
		preview.Snapshot.Class != "test/AdminAPIBatchNode" ||
		preview.Snapshot.ShortDescription != "Nodus administrativus" ||
		!preview.Snapshot.Experimental || len(preview.ColorMap) == 0 {
		t.Fatalf("node preview response = %s, %#v", previewResponse.Status, preview)
	}

	documentedResponse, err := http.Get(server.URL + "/api/docs/raw?locale=lorem&id=node%2Ftest%2FDocumentedOnly") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer documentedResponse.Body.Close() //nolint:errcheck
	documentedContent, err := io.ReadAll(documentedResponse.Body)
	if err != nil || documentedResponse.StatusCode != http.StatusOK || string(documentedContent) != "Documentation without a runtime class" {
		t.Fatalf("documented-only response = %s, %q, %v", documentedResponse.Status, documentedContent, err)
	}
	documentedPreviewResponse, err := http.Get(server.URL + "/api/docs/node-class?id=node%2Ftest%2FDocumentedOnly") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer documentedPreviewResponse.Body.Close() //nolint:errcheck
	if documentedPreviewResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("documented-only preview response = %s, want 404", documentedPreviewResponse.Status)
	}

	missingPreviewResponse, err := http.Get(server.URL + "/api/docs/node-class?id=guide%2Fstart") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer missingPreviewResponse.Body.Close() //nolint:errcheck
	if missingPreviewResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-node preview response = %s, want 400", missingPreviewResponse.Status)
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

func TestDocsAPIHandlesMissingLocalizedContent(t *testing.T) {
	admin := &AdminAPI{}
	session := admin.newAPISession(nil, nil, nil)

	tree := session.docsTree("en")
	if tree.Locale != "en" || len(tree.Topics) != 0 || len(tree.Available) != 1 || tree.Available[0].Locale != "en" {
		t.Fatalf("tree without localized content = %#v", tree)
	}
	if _, ok := session.docsPage("en", "guides/welcome"); ok {
		t.Fatal("missing documentation page was reported as available")
	}

	mux := http.NewServeMux()
	admin.registerDocsHTTPHandlers(mux, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/docs/tree", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET docs tree status = %d, want %d", response.Code, http.StatusOK)
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/docs/raw?id=guides%2Fwelcome", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET missing docs page status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

type debugDocsNodeClass struct{ adminAPIBatchNodeClass }

func (debugDocsNodeClass) ClassName() string { return "test/DebugDocsNode" }
func (debugDocsNodeClass) ShortDescription() string {
	return "DEBUG documentation test node"
}
