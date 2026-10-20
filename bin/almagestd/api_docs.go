package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
)

const (
	docsLocaleCookieName   = "almagest_locale"
	docsLocaleCookieMaxAge = 365 * 24 * 60 * 60
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
	classes, names := docsRuntimeClasses(s.engine)
	topics, effective := s.admin.localizedContent.DocsTree(locale, classes, names)
	result := api.DocsTreeResponse{Locale: effective, Topics: apiDocsTopics(topics)}
	if s.admin.localizedContent == nil {
		result.Available = []api.AvailableLocale{{Locale: "en", Name: "English"}}
		return result
	}
	for _, locale := range s.admin.localizedContent.AvailableLocales() {
		result.Available = append(result.Available, api.AvailableLocale{Locale: locale.Locale, Name: locale.Name})
	}
	return result
}

func (s *apiSession) docsPage(locale, id string) (api.DocsPageResponse, bool) {
	builtin := ""
	if class, isNode := strings.CutPrefix(id, "node/"); isNode {
		snapshot, ok := s.engine.NodeClassSnapshot(almagest.MainWorkspace, class)
		if !ok || isDebugDocsClass(snapshot.ShortDescription) {
			return api.DocsPageResponse{}, false
		}
		builtin, ok = s.engine.NodeClassLongDescription(almagest.MainWorkspace, class)
		if !ok {
			return api.DocsPageResponse{}, false
		}
	}
	page, content, effective, ok := s.admin.localizedContent.DocPage(locale, id, builtin)
	if !ok {
		return api.DocsPageResponse{}, false
	}
	return api.DocsPageResponse{ID: page.ID, Name: page.Name, Content: content, Locale: effective}, true
}

func apiDocsTopics(values []localizedcontent.DocTopic) []api.DocsTopic {
	result := make([]api.DocsTopic, 0, len(values))
	for _, value := range values {
		topic := api.DocsTopic{
			ID: value.ID, Name: value.Name, Folded: value.Folded, Order: value.Order,
			Topics: apiDocsTopics(value.Topics), Pages: make([]api.DocsPageSummary, 0, len(value.Pages)),
		}
		for _, page := range value.Pages {
			topic.Pages = append(topic.Pages, api.DocsPageSummary{ID: page.ID, Name: page.Name, Folded: page.Folded, Order: page.Order, Default: page.Default})
		}
		result = append(result, topic)
	}
	return result
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
