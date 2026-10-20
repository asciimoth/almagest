package api

import "github.com/asciimoth/almagest/lib/typecolor"

type DocsMethod string

const (
	MethodDocsTree DocsMethod = "tree"
	MethodDocsPage DocsMethod = "page"
)

func DocsMethods() []string {
	return []string{string(MethodDocsTree), string(MethodDocsPage)}
}

type DocsPageRequest struct {
	ID string `json:"id"`
}

type DocsPageSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Folded  bool   `json:"folded"`
	Order   *int   `json:"order,omitempty"`
	Default bool   `json:"default,omitempty"`
}

type DocsTopic struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Folded bool              `json:"folded"`
	Order  *int              `json:"order,omitempty"`
	Topics []DocsTopic       `json:"topics"`
	Pages  []DocsPageSummary `json:"pages"`
}

type DocsTreeResponse struct {
	Locale    string            `json:"locale"`
	Available []AvailableLocale `json:"available"`
	Topics    []DocsTopic       `json:"topics"`
}

type DocsPageResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Locale  string `json:"locale"`
}

// DocsNodeClassResponse contains the node metadata needed to render the same
// compact preview that the admin panel shows in its node picker.
type DocsNodeClassResponse struct {
	Snapshot NodeClassSnapshot                `json:"snapshot"`
	ColorMap map[string]typecolor.TypePalette `json:"colormap"`
}
