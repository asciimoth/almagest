package nodekit

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

// NormalizeListText normalizes comma, semicolon, space, and newline separated
// values to one value per line.
func NormalizeListText(value string) string {
	return strings.Join(SplitListText(value), "\n")
}

// SplitListText splits comma, semicolon, space, and newline separated values.
func SplitListText(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

// ParseAddrList reads a list of IP addresses. When allowCIDR is true, CIDR
// values are accepted and their address part is returned.
func ParseAddrList(value string, allowCIDR bool) ([]netip.Addr, error) {
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

// ParsePort parses numeric or service-name ports. It returns fallback for an
// empty or invalid value and returns 0 for out-of-range numeric ports.
func ParsePort(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}

	port, err := strconv.Atoi(value)
	if err != nil {
		port, err = gonnect.LookupPortOffline("ip", value)
		if err != nil {
			return fallback
		}
	}

	if port <= 0 || port > 65535 {
		return 0
	}

	return port
}

// ConfigTextField builds a validated text config field.
func ConfigTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if multiline {
		opts = append(opts, formular.Multiline)
	}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.TextField(id, label, value, opts...)
}

// ConfigIntField builds a validated integer config field.
func ConfigIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.IntField(id, label, value, opts...)
}

// ConfigFloatField builds a validated float config field.
func ConfigFloatField(id, label string, value float64, statusErr bool, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.FloatField(id, label, value, opts...)
}

// ConfigCheckbox builds a boolean config field.
func ConfigCheckbox(id, label string, value bool) formular.Item {
	return formular.CheckboxField(id, label, value)
}

// ReadonlyTextField builds a read-only text field.
func ReadonlyTextField(id, label, value string) formular.Item {
	return formular.ReadonlyTextField(id, label, value)
}

// ReadonlyMultilineTextField builds a read-only multiline text field.
func ReadonlyMultilineTextField(id, label, value string) formular.Item {
	return formular.MultilineTextField(id, label, value, formular.Readonly)
}

// ReadonlyStringListField builds a read-only multiline list field.
func ReadonlyStringListField(id, label string, values []string) formular.Item {
	return ReadonlyMultilineTextField(id, label, strings.Join(values, "\n"))
}

// ReadonlyIntField builds a read-only integer field.
func ReadonlyIntField(id, label string, value int) formular.Item {
	return formular.IntField(id, label, value, formular.Readonly)
}

// ReadonlyBoolField builds a read-only boolean field.
func ReadonlyBoolField(id, label string, value bool) formular.Item {
	return formular.CheckboxField(id, label, value, formular.Readonly)
}

// LogPayload writes a debug log entry for byte payload operations.
func LogPayload(logger pasta.Logger, op string, payload []byte, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debugf("%s bytes=%d payload=%q error=%v", op, len(payload), payload, err)
		return
	}
	logger.Debugf("%s bytes=%d payload=%q", op, len(payload), payload)
}

// LogResult writes a debug log entry for operations without payload data.
func LogResult(logger pasta.Logger, op string, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debugf("%s error=%v", op, err)
		return
	}
	logger.Debug(op)
}
