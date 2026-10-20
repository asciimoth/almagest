package localizedcontent

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/asciimoth/formular"
)

type presentation struct {
	Label       string                     `json:"label,omitempty"`
	Text        string                     `json:"text,omitempty"`
	Help        string                     `json:"help,omitempty"`
	Placeholder string                     `json:"placeholder,omitempty"`
	Templates   map[string]templateCatalog `json:"templates,omitempty"`
}

type templateCatalog struct {
	Label string                  `json:"label,omitempty"`
	Items map[string]presentation `json:"items,omitempty"`
}

type blockCatalog struct {
	Items map[string]presentation `json:"items"`
}

type dialogCatalog struct {
	Title       string            `json:"title,omitempty"`
	Text        string            `json:"text,omitempty"`
	Placeholder string            `json:"placeholder,omitempty"`
	YesLabel    string            `json:"yes_label,omitempty"`
	NoLabel     string            `json:"no_label,omitempty"`
	SubmitLabel string            `json:"submit_label,omitempty"`
	CancelLabel string            `json:"cancel_label,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	Resources   map[string]string `json:"resources,omitempty"`
}

type classCatalog struct {
	Blocks  map[string]blockCatalog  `json:"blocks,omitempty"`
	Dialogs map[string]dialogCatalog `json:"dialogs,omitempty"`
}

type menuCatalog map[string]classCatalog

func validateMenus(catalog menuCatalog) error {
	count := 0
	for class, value := range catalog {
		if !validClass(class) {
			return errInvalidMenu
		}
		for blockID, block := range value.Blocks {
			if !validAddress(blockID) {
				return errInvalidMenu
			}
			for itemID, item := range block.Items {
				if !validAddress(itemID) {
					return errInvalidMenu
				}
				count++
				if !validPresentation(item) {
					return errInvalidMenu
				}
				for templateName, template := range item.Templates {
					if !validAddress(templateName) {
						return errInvalidMenu
					}
					if !validValue(template.Label, true) {
						return errInvalidMenu
					}
					for nestedID, nested := range template.Items {
						if !validAddress(nestedID) {
							return errInvalidMenu
						}
						count++
						if !validPresentation(nested) {
							return errInvalidMenu
						}
					}
				}
			}
		}
		for dialogID, dialog := range value.Dialogs {
			if !validAddress(dialogID) || !validDialog(dialog) {
				return errInvalidMenu
			}
			count++
		}
	}
	if count > maxKeys {
		return errInvalidMenu
	}
	return nil
}

var errInvalidMenu = &menuError{}

type menuError struct{}

func (*menuError) Error() string { return "invalid menu catalog" }

func validPresentation(value presentation) bool {
	return validValue(value.Label, true) && validValue(value.Text, true) && validValue(value.Help, true) && validValue(value.Placeholder, true)
}

func validDialog(value dialogCatalog) bool {
	if !validValue(value.Title, true) || !validValue(value.Text, true) || !validValue(value.Placeholder, true) ||
		!validValue(value.YesLabel, true) || !validValue(value.NoLabel, true) || !validValue(value.SubmitLabel, true) || !validValue(value.CancelLabel, true) {
		return false
	}
	for address, label := range value.Options {
		if !validAddress(address) || !validValue(label, false) {
			return false
		}
	}
	for address, alternative := range value.Resources {
		if !validAddress(address) || !validValue(alternative, false) {
			return false
		}
	}
	return true
}

func validAddress(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func validValue(value string, optional bool) bool {
	return (optional || value != "") && len(value) <= maxValue && utf8.ValidString(value)
}

// TranslateFormular returns a translated deep copy of known backend messages.
func (r *Reader) TranslateFormular(locale, class string, message any) any {
	if r == nil || !validClass(class) {
		return message
	}
	english, _ := r.menu("en")
	requested, _ := r.menu(locale)
	lookup := func() (classCatalog, classCatalog) { return english[class], requested[class] }
	switch value := message.(type) {
	case formular.MenuSnapshotMessage:
		out := value.Copy()
		en, req := lookup()
		translateBlocks(out.Blocks, en, req)
		return out
	case *formular.MenuSnapshotMessage:
		if value == nil {
			return message
		}
		out := value.Copy()
		en, req := lookup()
		translateBlocks(out.Blocks, en, req)
		return &out
	case formular.BlockSnapshotMessage:
		out := value.Copy()
		en, req := lookup()
		translateBlock(&out.Block, en.Blocks[out.Block.ID], req.Blocks[out.Block.ID])
		return out
	case *formular.BlockSnapshotMessage:
		if value == nil {
			return message
		}
		out := value.Copy()
		en, req := lookup()
		translateBlock(&out.Block, en.Blocks[out.Block.ID], req.Blocks[out.Block.ID])
		return &out
	case formular.DialogCreateMessage:
		out := value.Copy()
		en, req := lookup()
		translateDialog(&out.Dialog, en.Dialogs[out.Dialog.ID], req.Dialogs[out.Dialog.ID])
		return out
	case *formular.DialogCreateMessage:
		if value == nil {
			return message
		}
		out := value.Copy()
		en, req := lookup()
		translateDialog(&out.Dialog, en.Dialogs[out.Dialog.ID], req.Dialogs[out.Dialog.ID])
		return &out
	default:
		return message
	}
}

func translateBlocks(blocks []formular.Block, en, req classCatalog) {
	for index := range blocks {
		translateBlock(&blocks[index], en.Blocks[blocks[index].ID], req.Blocks[blocks[index].ID])
	}
}
func translateBlock(block *formular.Block, en, req blockCatalog) {
	translateItems(block.Items, en.Items, req.Items)
}
func translateItems(items []formular.Item, en, req map[string]presentation) {
	for index := range items {
		item := &items[index]
		applyPresentation(item, en[item.ID], req[item.ID])
		if item.Field == nil {
			continue
		}
		for ti := range item.Templates {
			template := &item.Templates[ti]
			enTemplate := en[item.ID].Templates[template.Name]
			reqTemplate := req[item.ID].Templates[template.Name]
			if value := choose(reqTemplate.Label, enTemplate.Label); value != "" {
				template.Label = value
			}
			translateItems(template.Items, enTemplate.Items, reqTemplate.Items)
		}
		for ei := range item.Elements {
			element := &item.Elements[ei]
			enTemplate := en[item.ID].Templates[element.Template]
			reqTemplate := req[item.ID].Templates[element.Template]
			translateItems(element.Items, enTemplate.Items, reqTemplate.Items)
		}
	}
}
func applyPresentation(item *formular.Item, en, req presentation) {
	if value := choose(req.Label, en.Label); value != "" {
		item.Label = value
	}
	if value := choose(req.Text, en.Text); value != "" {
		item.Text = value
	}
	if value := choose(req.Help, en.Help); value != "" {
		item.Help = value
	}
	if item.Field != nil {
		if value := choose(req.Placeholder, en.Placeholder); value != "" {
			item.Placeholder = value
		}
	}
}
func translateDialog(dialog *formular.Dialog, en, req dialogCatalog) {
	if value := choose(req.Title, en.Title); value != "" {
		dialog.Title = value
	}
	if value := choose(req.Text, en.Text); value != "" {
		dialog.Text = value
	}
	if value := choose(req.Placeholder, en.Placeholder); value != "" {
		dialog.Placeholder = value
	}
	if value := choose(req.YesLabel, en.YesLabel); value != "" {
		dialog.YesLabel = value
	}
	if value := choose(req.NoLabel, en.NoLabel); value != "" {
		dialog.NoLabel = value
	}
	if value := choose(req.SubmitLabel, en.SubmitLabel); value != "" {
		dialog.SubmitLabel = value
	}
	if value := choose(req.CancelLabel, en.CancelLabel); value != "" {
		dialog.CancelLabel = value
	}
	for index := range dialog.Options {
		if value := choose(req.Options[dialog.Options[index].Value], en.Options[dialog.Options[index].Value]); value != "" {
			dialog.Options[index].Label = value
		}
	}
	for index := range dialog.Resources {
		if value := choose(req.Resources[dialog.Resources[index].ID], en.Resources[dialog.Resources[index].ID]); value != "" {
			dialog.Resources[index].Alt = value
		}
	}
}
func choose(requested, english string) string {
	if requested != "" {
		return requested
	}
	return english
}
