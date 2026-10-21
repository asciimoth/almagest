package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/typecolor"
)

const (
	docsLocaleCookieName   = "almagest_locale"
	docsLocaleCookieMaxAge = 365 * 24 * 60 * 60
	docsNodeUnavailable    = "This node is not available in the current daemon environment."
	docsNodeNoDescription  = "Detailed documentation is not available for this node class."
)

func (s *apiSession) handleDocsMessage(msg api.Message) (api.Message, error) {
	switch api.DocsMethod(msg.Method) {
	case api.MethodDocsTree:
		if _, err := decodePayload[struct{}](msg); err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		return api.NewResponse(msg.ID, s.docsTree(s.currentLocale()))
	case api.MethodDocsPage:
		req, err := decodePayload[api.DocsPageRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		page, ok := s.docsPage(s.currentLocale(), req.ID)
		if !ok {
			return newRequestError(msg.ID, api.ErrorNotFound, "documentation page was not found")
		}
		return api.NewResponse(msg.ID, page)
	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (s *apiSession) docsTree(locale string) api.DocsTreeResponse {
	classes, names, available := s.docsClasses(locale)
	content := s.admin.localizedContent
	topics, effective := content.DocsTree(locale, classes, names)
	message := docsNodeUnavailable
	if panel := content.Panel(effective); panel["docs.node.unavailable"] != "" {
		message = panel["docs.node.unavailable"]
	}
	result := api.DocsTreeResponse{
		Locale: effective, Topics: apiDocsTopics(topics, available), NodeUnavailableMessage: message,
	}
	if content == nil {
		result.Available = []api.AvailableLocale{{Locale: "en", Name: "English"}}
		return result
	}
	for _, locale := range content.AvailableLocales() {
		result.Available = append(result.Available, api.AvailableLocale{Locale: locale.Locale, Name: locale.Name})
	}
	return result
}

func (s *apiSession) docsPage(locale, id string) (api.DocsPageResponse, bool) {
	contentReader := s.admin.localizedContent
	builtin := ""
	if class, isNode := strings.CutPrefix(id, "node/"); isNode {
		if s.engine == nil {
			return api.DocsPageResponse{}, false
		}
		snapshot, ok := s.engine.NodeClassSnapshot(almagest.MainWorkspace, class)
		if ok && isDebugDocsClass(snapshot.ShortDescription) {
			return api.DocsPageResponse{}, false
		}
		if !ok && s.isDebugDocumentedClass(locale, class) {
			return api.DocsPageResponse{}, false
		}
		if ok {
			builtin, _ = s.engine.NodeClassLongDescription(almagest.MainWorkspace, class)
			if builtin == "" {
				builtin = docsNodeNoDescription
				if panel := contentReader.Panel(locale); panel["docs.node.description_unavailable"] != "" {
					builtin = panel["docs.node.description_unavailable"]
				}
			}
		}
	}
	page, content, effective, ok := contentReader.DocPage(locale, id, builtin)
	if !ok {
		return api.DocsPageResponse{}, false
	}
	return api.DocsPageResponse{ID: page.ID, Name: page.Name, Content: content, Locale: effective}, true
}

func (s *apiSession) docsNodeClass(locale, class string) (api.DocsNodeClassResponse, bool) {
	if s.engine == nil {
		return api.DocsNodeClassResponse{}, false
	}
	snapshot, ok := s.engine.NodeClassSnapshot(almagest.MainWorkspace, class)
	if !ok || isDebugDocsClass(snapshot.ShortDescription) {
		return api.DocsNodeClassResponse{}, false
	}
	result := api.DocsNodeClassResponse{
		Snapshot: api.NewNodeClassSnapshot(snapshot, s.admin.experimentalNodeTypes[snapshot.Class]),
		ColorMap: make(map[string]typecolor.TypePalette, len(typecolor.DefaultColorMap)),
	}
	s.localizeClass(locale, &result.Snapshot)
	maps.Copy(result.ColorMap, typecolor.DefaultColorMap)
	return result, true
}

func apiDocsTopics(values []localizedcontent.DocTopic, available map[string]bool) []api.DocsTopic {
	result := make([]api.DocsTopic, 0, len(values))
	for _, value := range values {
		topic := api.DocsTopic{
			ID: value.ID, Name: value.Name, Folded: value.Folded, Order: value.Order,
			Topics: apiDocsTopics(value.Topics, available), Pages: make([]api.DocsPageSummary, 0, len(value.Pages)),
		}
		for _, page := range value.Pages {
			summary := api.DocsPageSummary{ID: page.ID, Name: page.Name, Folded: page.Folded, Order: page.Order, Default: page.Default}
			if class, isNode := strings.CutPrefix(page.ID, "node/"); isNode {
				value := available[class]
				summary.Available = &value
			}
			topic.Pages = append(topic.Pages, summary)
		}
		result = append(result, topic)
	}
	return result
}

func (s *apiSession) docsClasses(locale string) ([]string, map[string]string, map[string]bool) {
	runtimeClasses, names := docsRuntimeClasses(s.engine)
	classes := slices.Clone(runtimeClasses)
	available := make(map[string]bool, len(runtimeClasses))
	metadata := make(map[string]api.NodeClassSnapshot, len(runtimeClasses))
	for _, class := range runtimeClasses {
		available[class] = true
		if snapshot, ok := s.engine.NodeClassSnapshot(almagest.MainWorkspace, class); ok {
			metadata[class] = api.NewNodeClassSnapshot(snapshot, false)
		}
	}
	for _, class := range s.admin.localizedContent.DocumentedNodeClasses(locale) {
		if _, exists := available[class]; exists || s.isDebugDocumentedClass(locale, class) {
			continue
		}
		classes = append(classes, class)
		metadata[class] = api.NodeClassSnapshot{Class: class}
	}
	slices.SortFunc(classes, func(a, b string) int {
		return api.CompareNodeClassSnapshots(metadata[a], metadata[b])
	})
	return classes, names, available
}

func (s *apiSession) isDebugDocumentedClass(locale, class string) bool {
	name, _ := s.admin.localizedContent.NodeShort(locale, class, "")
	englishName, _ := s.admin.localizedContent.NodeShort("en", class, "")
	return isDebugDocsClass(name) || isDebugDocsClass(englishName)
}

func docsRuntimeClasses(engine almagest.Engine) ([]string, map[string]string) {
	if engine == nil {
		return nil, nil
	}
	snapshot, ok := engine.WorkspaceSnapshot(almagest.MainWorkspace)
	if !ok {
		return nil, nil
	}
	ordered := api.SortedNodeClassSnapshots(snapshot.Classes, nil)
	classes := make([]string, 0, len(ordered))
	names := make(map[string]string, len(snapshot.Classes))
	for _, class := range ordered {
		if isDebugDocsClass(class.ShortDescription) {
			continue
		}
		classes = append(classes, class.Class)
		names[class.Class] = class.ShortDescription
	}
	return classes, names
}

func isDebugDocsClass(shortDescription string) bool {
	return strings.HasPrefix(strings.TrimSpace(shortDescription), "DEBUG ")
}

func (a *AdminAPI) registerDocsHTTPHandlers(mux *http.ServeMux, engine almagest.Engine) {
	mux.HandleFunc("GET /api/docs/tree", func(w http.ResponseWriter, r *http.Request) {
		session := a.newAPISession(engine, nil, nil)
		tree := session.docsTree(docsRequestLocale(r))
		writeDocsLocaleCookie(w, r, tree.Locale)
		writeDocsJSON(w, tree)
	})
	mux.HandleFunc("GET /api/docs/page", func(w http.ResponseWriter, r *http.Request) {
		session := a.newAPISession(engine, nil, nil)
		page, ok := session.docsPage(docsRequestLocale(r), r.URL.Query().Get("id"))
		if !ok {
			http.Error(w, "documentation page was not found", http.StatusNotFound)
			return
		}
		writeDocsLocaleCookie(w, r, page.Locale)
		writeDocsJSON(w, page)
	})
	mux.HandleFunc("GET /api/docs/raw", func(w http.ResponseWriter, r *http.Request) {
		session := a.newAPISession(engine, nil, nil)
		page, ok := session.docsPage(docsRequestLocale(r), r.URL.Query().Get("id"))
		if !ok {
			http.Error(w, "documentation page was not found", http.StatusNotFound)
			return
		}
		writeDocsLocaleCookie(w, r, page.Locale)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(page.Content))
	})
	mux.HandleFunc("GET /api/docs/node-class", func(w http.ResponseWriter, r *http.Request) {
		class, ok := docsNodeClassFromRequest(r)
		if !ok {
			http.Error(w, "node documentation page was not specified", http.StatusBadRequest)
			return
		}
		session := a.newAPISession(engine, nil, nil)
		response, ok := session.docsNodeClass(docsRequestLocale(r), class)
		if !ok {
			http.Error(w, "node class was not found", http.StatusNotFound)
			return
		}
		writeDocsJSON(w, response)
	})
}

func docsNodeClassFromRequest(r *http.Request) (string, bool) {
	id := r.URL.Query().Get("id")
	class, ok := strings.CutPrefix(id, "node/")
	return class, ok && class != ""
}

func docsRequestLocale(r *http.Request) string {
	if locale := r.URL.Query().Get("locale"); locale != "" {
		return locale
	}
	cookie, err := r.Cookie(docsLocaleCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func writeDocsLocaleCookie(w http.ResponseWriter, r *http.Request, locale string) {
	http.SetCookie(w, &http.Cookie{
		Name: docsLocaleCookieName, Value: locale, Path: "/", MaxAge: docsLocaleCookieMaxAge,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
}

func writeDocsJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
