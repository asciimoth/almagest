package almagest

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func readConfigBool(cfg configer.Config, key string, fallback bool) bool {
	if cfg == nil {
		return fallback
	}
	raw, err := cfg.Get(configer.Path{key})
	if err != nil {
		return fallback
	}
	if value, ok := raw.(bool); ok {
		return value
	}
	return fallback
}

func readConfigFloat(cfg configer.Config, key string, fallback float64) float64 {
	if cfg == nil {
		return fallback
	}
	raw, err := cfg.Get(configer.Path{key})
	if err != nil {
		return fallback
	}
	switch value := raw.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return fallback
	}
}

func floatFromAny(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func normalizeListText(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	return strings.Join(fields, "\n")
}

func parseAddrList(value string, allowCIDR bool) ([]netip.Addr, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]netip.Addr, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if allowCIDR {
			if prefix, err := netip.ParsePrefix(field); err == nil {
				out = append(out, prefix.Addr())
				continue
			}
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address", field)
		}
		out = append(out, addr)
	}
	return out, nil
}

func portHasLinks(w *pasta.Workspace, port uint64) bool {
	if w == nil || port == 0 {
		return false
	}
	snapshot, ok := w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

func configTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	field := &formular.Field{Kind: formular.FieldText, Value: value, Validation: true, Multiline: multiline}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = statusText
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func configIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	field := &formular.Field{Kind: formular.FieldInt, Value: value, Validation: true}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = statusText
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func configFloatField(id, label string, value float64, statusErr bool, statusText string) formular.Item {
	field := &formular.Field{Kind: formular.FieldFloat, Value: value, Validation: true}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = statusText
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func configCheckbox(id, label string, value bool) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{Kind: formular.FieldCheckbox, Value: value}}
}

func readonlyTextField(id, label, value string) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:     formular.FieldText,
		Value:    value,
		Readonly: true,
	}}
}

func readonlyMultilineTextField(id, label, value string) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:      formular.FieldText,
		Value:     value,
		Readonly:  true,
		Multiline: true,
	}}
}

func readonlyStringListField(id, label string, values []string) formular.Item {
	return readonlyMultilineTextField(id, label, strings.Join(values, "\n"))
}

func readonlyIntField(id, label string, value int) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:     formular.FieldInt,
		Value:    value,
		Readonly: true,
	}}
}

func readonlyBoolField(id, label string, value bool) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:     formular.FieldCheckbox,
		Value:    value,
		Readonly: true,
	}}
}
