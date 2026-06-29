package almagest

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/asciimoth/formular"
)

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

func configTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if multiline {
		opts = append(opts, formular.Multiline)
	}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.TextField(id, label, value, opts...)
}

func configIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.IntField(id, label, value, opts...)
}

func configFloatField(id, label string, value float64, statusErr bool, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.FloatField(id, label, value, opts...)
}

func configCheckbox(id, label string, value bool) formular.Item {
	return formular.CheckboxField(id, label, value)
}

func readonlyTextField(id, label, value string) formular.Item {
	return formular.ReadonlyTextField(id, label, value)
}

func readonlyMultilineTextField(id, label, value string) formular.Item {
	return formular.MultilineTextField(id, label, value, formular.Readonly)
}

func readonlyStringListField(id, label string, values []string) formular.Item {
	return readonlyMultilineTextField(id, label, strings.Join(values, "\n"))
}

func readonlyIntField(id, label string, value int) formular.Item {
	return formular.IntField(id, label, value, formular.Readonly)
}

func readonlyBoolField(id, label string, value bool) formular.Item {
	return formular.CheckboxField(id, label, value, formular.Readonly)
}
