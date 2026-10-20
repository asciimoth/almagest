package localizedcontent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/asciimoth/formular"
)

func TestReaderFallbackReloadAndSafety(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{"app.title":"English","only.en":"Fallback"}`)
	writeBundle(t, root, "lorem", `{"app.title":"Lorem"}`)
	write(t, filepath.Join(root, "en", "nodes.json"), `{"almagest/Tun":"System TUN","almagest/Other":"English other"}`)
	write(t, filepath.Join(root, "lorem", "nodes.json"), `{"almagest/Tun":"Lorem TUN"}`)
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reader.Panel("lorem"); got["app.title"] != "Lorem" || got["only.en"] != "Fallback" {
		t.Fatalf("panel = %#v", got)
	}
	if got, source := reader.NodeShort("lorem", "almagest/Tun", "built-in"); got != "Lorem TUN" || source != SourceRequested {
		t.Fatalf("short = %q, %s", got, source)
	}
	if got, source := reader.NodeShort("missing", "almagest/Tun", "built-in"); got != "System TUN" || source != SourceEnglish {
		t.Fatalf("fallback = %q, %s", got, source)
	}
	if got, source := reader.NodeShort("lorem", "almagest/Other", "built-in"); got != "English other" || source != SourceEnglish {
		t.Fatalf("per-key fallback = %q, %s", got, source)
	}
	if got, source := reader.NodeShort("lorem", "../escape", "built-in"); got != "built-in" || source != SourceBuiltin {
		t.Fatalf("unsafe fallback = %q, %s", got, source)
	}

	panel := filepath.Join(root, "lorem", "panel.json")
	write(t, panel, `{"app.title":"Updated"}`)
	when := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(panel, when, when); err != nil {
		t.Fatal(err)
	}
	if got := reader.Panel("lorem")["app.title"]; got != "Updated" {
		t.Fatalf("reloaded title = %q", got)
	}
	temporary := filepath.Join(root, "lorem", "panel.next")
	write(t, temporary, `{"app.title":"Atomic"}`)
	when = when.Add(2 * time.Second)
	if err := os.Chtimes(temporary, when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, panel); err != nil {
		t.Fatal(err)
	}
	if got := reader.Panel("lorem")["app.title"]; got != "Atomic" {
		t.Fatalf("atomic reload title = %q", got)
	}
	nodes := filepath.Join(root, "lorem", "nodes.json")
	write(t, nodes, `{"almagest/Tun":"Updated TUN"}`)
	when = when.Add(2 * time.Second)
	if err := os.Chtimes(nodes, when, when); err != nil {
		t.Fatal(err)
	}
	if got, source := reader.NodeShort("lorem", "almagest/Tun", "built-in"); got != "Updated TUN" || source != SourceRequested {
		t.Fatalf("reloaded node = %q, %s", got, source)
	}
	write(t, panel, `{"app.title":`)
	when = when.Add(2 * time.Second)
	if err := os.Chtimes(panel, when, when); err != nil {
		t.Fatal(err)
	}
	if got := reader.Panel("lorem")["app.title"]; got != "Atomic" {
		t.Fatalf("last valid title = %q", got)
	}

}

func TestTranslateFormularUsesIDsAndDoesNotMutateInput(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{}`)
	write(t, filepath.Join(root, "en", "menus.json"), `{"almagest/Test":{"blocks":{"Tor settings":{"items":{"name":{"label":"Localized name","help":"Localized help","placeholder":"Enter name","templates":{"child":{"label":"Localized child","items":{"nested":{"label":"Nested label"}}}}}}}}}}`)
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	message := formular.MenuSnapshotMessage{Blocks: []formular.Block{{ID: "Tor settings", Items: []formular.Item{{ID: "name", Label: "Name", Field: &formular.Field{Placeholder: "Input", Templates: []formular.ArrayTemplate{{Name: "child", Label: "Child", Items: []formular.Item{{ID: "nested", Label: "Nested"}}}}}}}}}}
	translated := reader.TranslateFormular("en", "almagest/Test", message).(formular.MenuSnapshotMessage)
	item := translated.Blocks[0].Items[0]
	if item.Label != "Localized name" || item.Help != "Localized help" || item.Placeholder != "Enter name" || item.Templates[0].Label != "Localized child" || item.Templates[0].Items[0].Label != "Nested label" {
		t.Fatalf("translated item = %#v", item)
	}
	if message.Blocks[0].Items[0].Label != "Name" || message.Blocks[0].Items[0].Help != "" || message.Blocks[0].Items[0].Templates[0].Items[0].Label != "Nested" {
		t.Fatal("translator mutated the engine message")
	}
}

func TestTranslateFormularFallsBackPerValueAndPassesUnknownMessages(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{}`)
	write(t, filepath.Join(root, "en", "menus.json"), `{"almagest/Test":{"dialogs":{"confirm":{"title":"English title","yes_label":"Proceed","options":{"safe":"Safe option"}}}}}`)
	writeBundle(t, root, "lorem", `{}`)
	write(t, filepath.Join(root, "lorem", "menus.json"), `{"almagest/Test":{"dialogs":{"confirm":{"title":"Lorem title"}}}}`)
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	message := formular.DialogCreateMessage{Dialog: formular.Dialog{
		ID: "confirm", Title: "Built-in title", YesLabel: "Built-in yes",
		Options:   []formular.DialogOption{{Value: "safe", Label: "Built-in option"}},
		Resources: []formular.DialogResource{{ID: "image", MIMEType: "image/png", Data: "runtime-data", Alt: "Built-in alt"}},
	}}
	translated := reader.TranslateFormular("lorem", "almagest/Test", message).(formular.DialogCreateMessage)
	if translated.Dialog.Title != "Lorem title" || translated.Dialog.YesLabel != "Proceed" || translated.Dialog.Options[0].Label != "Safe option" {
		t.Fatalf("translated dialog = %#v", translated.Dialog)
	}
	if translated.Dialog.Options[0].Value != "safe" || translated.Dialog.Resources[0].Data != "runtime-data" || translated.Dialog.Resources[0].MIMEType != "image/png" {
		t.Fatalf("semantic dialog data changed: %#v", translated.Dialog)
	}
	unknown := &struct{ Value string }{Value: "unchanged"}
	if got := reader.TranslateFormular("lorem", "almagest/Test", unknown); got != unknown {
		t.Fatal("unknown message was copied or changed")
	}
}

func TestRejectDuplicateJSONKeys(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{"same":"one","same":"two"}`)
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if panel := reader.Panel("en"); len(panel) != 0 {
		t.Fatalf("duplicate catalog accepted: %#v", panel)
	}
}

func TestReaderRejectsInvalidAndOversizedContent(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{"safe":"value"}`)
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	writeBytes(t, filepath.Join(root, "en", "nodes.json"), []byte{0xff})
	if got, source := reader.NodeShort("en", "almagest/Bad", "fallback"); got != "fallback" || source != SourceBuiltin {
		t.Fatalf("invalid UTF-8 = %q, %s", got, source)
	}
	writeBytes(t, filepath.Join(root, "en", "docs", "pages", "nodes", "almagest", "Large.md"), bytes.Repeat([]byte("x"), maxNodeDocument+1))
	if got, source := reader.NodeLong("en", "almagest/Large", "fallback"); got != "fallback" || source != SourceBuiltin {
		t.Fatalf("oversized document = %q, %s", got, source)
	}

	writeBytes(t, filepath.Join(root, "en", "panel.json"), bytes.Repeat([]byte(" "), maxCatalog+1))
	if got := reader.Panel("en"); len(got) != 0 {
		t.Fatalf("oversized panel accepted: %#v", got)
	}
}

func TestDocumentationTreePageAndLocaleFallback(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{}`)
	writeBundle(t, root, "lorem", `{}`)
	index := `{"topics":[{"id":"nodes","name":"Nodes","order":20,"folded":false,"node_pages":true},{"id":"guide","name":"Guide","order":10,"folded":false,"pages":[{"id":"guide/later","name":"Later","file":"guide/later.md","order":20},{"id":"guide/start","name":"Start","file":"guide/start.md","order":10,"default":true}]}]}`
	write(t, filepath.Join(root, "en", "docs", "index.json"), index)
	write(t, filepath.Join(root, "lorem", "docs", "index.json"), `{"topics":[{"id":"nodes","name":"Nodi","order":20,"folded":false,"node_pages":true},{"id":"guide","name":"Dux","order":10,"folded":false,"pages":[{"id":"guide/later","name":"Postea","file":"guide/later.md","order":20},{"id":"guide/start","name":"Initium","file":"guide/start.md","order":10,"default":true}]}]}`)
	write(t, filepath.Join(root, "en", "docs", "pages", "nodes", "almagest", "Tun.md"), "English node")
	write(t, filepath.Join(root, "en", "nodes.json"), `{"almagest/Tun":"System TUN"}`)
	write(t, filepath.Join(root, "lorem", "nodes.json"), `{"almagest/Tun":"TUN lorem"}`)
	write(t, filepath.Join(root, "en", "docs", "pages", "guide", "start.md"), "English guide")
	write(t, filepath.Join(root, "lorem", "docs", "pages", "guide", "start.md"), "Lorem guide")
	write(t, filepath.Join(root, "en", "docs", "pages", "guide", "later.md"), "English later guide")
	write(t, filepath.Join(root, "lorem", "docs", "pages", "guide", "later.md"), "Lorem later guide")
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	topics, locale := reader.DocsTree("lorem", []string{"almagest/Tun", "missing", "pasta/Trigger"})
	if locale != "lorem" || len(topics) != 2 || topics[0].Name != "Dux" || topics[0].Folded || len(topics[0].Pages) != 2 || topics[0].Pages[0].Name != "Initium" || !topics[0].Pages[0].Default || topics[1].Name != "Nodi" || len(topics[1].Pages) != 2 || topics[1].Pages[0].Name != "TUN lorem" {
		t.Fatalf("tree = %#v, locale = %q", topics, locale)
	}
	if page, content, effective, ok := reader.DocPage("lorem", "node/almagest/Tun"); !ok || page.Name != "TUN lorem" || content != "English node" || effective != "lorem" {
		t.Fatalf("node page = %#v, %q, %q, %t", page, content, effective, ok)
	}
	if page, content, _, ok := reader.DocPage("lorem", "node/pasta/Trigger", "Built-in trigger documentation"); !ok || page.Name != "Trigger" || content != "Built-in trigger documentation" {
		t.Fatalf("built-in node page = %#v, %q, %t", page, content, ok)
	}
	if page, content, _, ok := reader.DocPage("lorem", "guide/start"); !ok || page.Name != "Initium" || content != "Lorem guide" || !page.Folded || !page.Default || page.Order == nil || *page.Order != 10 {
		t.Fatalf("guide page = %#v, %q, %t", page, content, ok)
	}
	if _, _, _, ok := reader.DocPage("lorem", "../escape"); ok {
		t.Fatal("unsafe documentation ID was accepted")
	}
}

func TestDocumentationIndexRejectsInvalidOrderingAndMultipleDefaults(t *testing.T) {
	negative := -1
	if err := validateDocsIndex(docIndex{Topics: []docTopic{{ID: "guide", Name: "Guide", Order: &negative}}}); err == nil {
		t.Fatal("negative documentation order was accepted")
	}
	index := docIndex{Topics: []docTopic{{
		ID: "guide", Name: "Guide", Pages: []docPage{
			{ID: "guide/one", Name: "One", File: "guide/one.md", Default: true},
			{ID: "guide/two", Name: "Two", File: "guide/two.md", Default: true},
		},
	}}}
	if err := validateDocsIndex(index); err == nil {
		t.Fatal("multiple default documentation pages were accepted")
	}
}

func TestReaderFallsBackWhenCatalogFilesAndKeysAreMissing(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "en", "manifest.json"), `{"format":1,"locale":"en","name":"English","development_only":false}`)
	reader, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	if panel := reader.Panel("en"); len(panel) != 0 {
		t.Fatalf("missing panel catalog = %#v, want an empty built-in overlay", panel)
	}
	if got, source := reader.NodeShort("en", "almagest/Missing", "built-in"); got != "built-in" || source != SourceBuiltin {
		t.Fatalf("missing node text = %q, %s", got, source)
	}
	message := formular.MenuSnapshotMessage{Blocks: []formular.Block{{ID: "main", Items: []formular.Item{{ID: "name", Label: "Built-in label"}}}}}
	translated := reader.TranslateFormular("en", "almagest/Missing", message).(formular.MenuSnapshotMessage)
	if translated.Blocks[0].Items[0].Label != "Built-in label" {
		t.Fatalf("missing menu key removed built-in text: %#v", translated)
	}
}

func TestValidateRootRejectsUnexpectedFilesAndSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{"safe":"value"}`)
	write(t, filepath.Join(root, "en", "unexpected"), "value")
	if err := ValidateRoot(root); err == nil {
		t.Fatal("unexpected bundle file was accepted")
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "locales")
	if makeTestSymlink(t, target, link) {
		if _, err := New(link, nil); err == nil {
			t.Fatal("symlink locale root was accepted")
		}
	}
}

func TestValidateRootRejectsInvalidNodeCatalog(t *testing.T) {
	root := t.TempDir()
	writeBundle(t, root, "en", `{}`)
	write(t, filepath.Join(root, "en", "nodes.json"), `{"../unsafe":"name"}`)
	if err := ValidateRoot(root); err == nil {
		t.Fatal("invalid node catalog was accepted")
	}
}

func makeTestSymlink(t *testing.T, target, link string) bool {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return true
	}
	// Ordinary Windows accounts need Developer Mode or
	// SeCreateSymbolicLinkPrivilege to construct this test fixture.
	if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
		t.Log("symlink rejection assertion is unavailable without Windows symlink privilege")
		return false
	}
	t.Fatal(err)
	return false
}

func writeBundle(t *testing.T, root, locale, panel string) {
	t.Helper()
	development := "false"
	if locale == "lorem" {
		development = "true"
	}
	write(t, filepath.Join(root, locale, "manifest.json"), `{"format":1,"locale":"`+locale+`","name":"`+locale+`","development_only":`+development+`}`)
	write(t, filepath.Join(root, locale, "panel.json"), panel)
	write(t, filepath.Join(root, locale, "menus.json"), `{}`)
	write(t, filepath.Join(root, locale, "nodes.json"), `{}`)
}

func write(t *testing.T, path, value string) {
	t.Helper()
	writeBytes(t, path, []byte(value))
}

func writeBytes(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o644); err != nil {
		t.Fatal(err)
	}
}
