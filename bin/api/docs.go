package api

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
