package diagnostics

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const replacement = "?"

// Sanitize makes one terminal-safe, single-line UTF-8 field and truncates it
// on a rune boundary. It removes ANSI controls, line breaks, bidi controls,
// invalid UTF-8, and other non-printing code points.
func Sanitize(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var out strings.Builder
	out.Grow(min(len(value), limit))
	for len(value) != 0 && out.Len() < limit {
		r, size := utf8.DecodeRuneInString(value)
		value = value[size:]
		text := string(r)
		if r == utf8.RuneError && size == 1 || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == '\n' || r == '\r' {
			text = replacement
		}
		if out.Len()+len(text) > limit {
			break
		}
		out.WriteString(text)
	}
	return strings.TrimSpace(out.String())
}

func containsUnsafe(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}

// Redactor removes bootstrap-provided secret literals and values that follow
// security-sensitive labels. It is a final safety boundary. Callers must not
// intentionally put config bodies, packet bodies, command slots, or raw
// process metadata in diagnostic messages.
type Redactor struct{ secrets []string }

func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	for _, secret := range secrets {
		if secret != "" {
			r.secrets = append(r.secrets, secret)
		}
	}
	slices.SortFunc(r.secrets, func(a, b string) int { return len(b) - len(a) })
	return r
}

var sensitiveLabels = []string{
	"secret", "password", "passwd", "token", "authorization", "cookie",
	"config", "packet", "command_slot", "argv", "environment", "process_metadata",
	"pid", "process_id", "executable", "cwd",
}

func (r *Redactor) String(value string) string {
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, label := range sensitiveLabels {
		search := 0
		for {
			i := indexASCIIFold(value[search:], label)
			if i < 0 {
				break
			}
			i += search + len(label)
			j := i
			for j < len(value) && (value[j] == ' ' || value[j] == '\t') {
				j++
			}
			if j >= len(value) || value[j] != '=' && value[j] != ':' {
				search = i
				continue
			}
			j++
			for j < len(value) && value[j] == ' ' {
				j++
			}
			end := sensitiveValueEnd(value, j)
			value = value[:j] + "[REDACTED]" + value[end:]
			search = j + len("[REDACTED]")
		}
	}
	return value
}

func indexASCIIFold(value, target string) int {
	if len(target) == 0 {
		return 0
	}
	for start := 0; start+len(target) <= len(value); start++ {
		matched := true
		for i := range len(target) {
			a, b := value[start+i], target[i]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if a != b {
				matched = false
				break
			}
		}
		if matched {
			return start
		}
	}
	return -1
}

func sensitiveValueEnd(value string, start int) int {
	if start >= len(value) {
		return start
	}
	if quote := value[start]; quote == '\'' || quote == '"' {
		for i := start + 1; i < len(value); i++ {
			if value[i] == quote && value[i-1] != '\\' {
				return i + 1
			}
		}
		return len(value)
	}
	if open := value[start]; open == '{' || open == '[' || open == '(' {
		var closeByte byte
		switch open {
		case '{':
			closeByte = '}'
		case '[':
			closeByte = ']'
		case '(':
			closeByte = ')'
		}
		for i := start + 1; i < len(value); i++ {
			if value[i] == closeByte {
				return i + 1
			}
		}
		return len(value)
	}
	end := start
	for end < len(value) && value[end] != ' ' && value[end] != ',' && value[end] != ';' {
		end++
	}
	return end
}
