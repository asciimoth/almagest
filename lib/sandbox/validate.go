package sandbox

import (
	"fmt"
	"io/fs"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateName checks a bounded logical identifier (slot, template, or program).
func ValidateName(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return ErrInvalid
	}
	for _, c := range name {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !valid {
			return ErrInvalid
		}
	}
	if name == "." || name == ".." {
		return ErrInvalid
	}
	return nil
}

// ValidatePath is a syntax check, not filesystem confinement. Backends must
// also reject symlinks, reparse points, and hard-link aliases with held handles.
func ValidatePath(p Path, allowRoot bool) error {
	if p.Root != Temporary && p.Root != Persistent {
		return ErrInvalid
	}
	if p.Rel == "" || p.Rel == "." {
		if allowRoot {
			return nil
		}
		return ErrInvalid
	}
	if len(p.Rel) > 4096 || !utf8.ValidString(p.Rel) {
		return ErrInvalid
	}
	for _, part := range strings.Split(p.Rel, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.TrimRight(part, " .") != part {
			return ErrInvalid
		}
		for _, c := range part {
			if unicode.IsControl(c) || strings.ContainsRune(`\:<>"|?*`, c) {
				return ErrInvalid
			}
		}
		base, _, _ := strings.Cut(part, ".")
		base = strings.ToUpper(strings.TrimRight(base, " "))
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return ErrInvalid
		}
		if len(base) > 3 && utf8.RuneCountInString(base[3:]) == 1 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.Contains("123456789¹²³", base[3:]) {
			return ErrInvalid
		}
	}
	return nil
}
func (o OpenOptions) Validate() error {
	if !o.Read && !o.Write || (o.Create || o.Truncate || o.Append) && !o.Write || o.Exclusive && !o.Create || o.Append && o.Truncate || o.Mode & ^fs.FileMode(0777) != 0 {
		return ErrInvalid
	}
	return nil
}

// ValidateEndpoint accepts only numeric, unmapped, zone-free loopback IPs.
func ValidateEndpoint(network, address string, listen bool) error {
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		return ErrUnsupported
	}
	a, err := netip.ParseAddrPort(address)
	if err != nil || a.Addr().Zone() != "" || a.Addr().Is4In6() || !a.Addr().IsLoopback() || !listen && a.Port() == 0 {
		return ErrInvalid
	}
	if strings.HasSuffix(network, "4") && !a.Addr().Is4() || strings.HasSuffix(network, "6") && !a.Addr().Is6() {
		return ErrInvalid
	}
	return nil
}

// Validate rejects non-selected union fields, including zero-valued path tags.
func (v Value) Validate() error {
	remaining := v
	remaining.Kind = ""
	switch v.Kind {
	case TextValue:
		if len(v.Text) > 65536 || !utf8.ValidString(v.Text) || strings.ContainsRune(v.Text, 0) {
			return ErrInvalid
		}
		remaining.Text = ""
	case IntegerValue:
		remaining.Int = 0
	case BooleanValue:
		remaining.Bool = false
	case PathValue:
		if err := ValidatePath(v.Path, true); err != nil {
			return err
		}
		remaining.Path = Path{}
	case ProgramValue:
		if err := ValidateName(string(v.Program)); err != nil {
			return err
		}
		remaining.Program = ""
	case EndpointValue:
		if err := ValidateEndpoint("tcp", v.Endpoint, false); err != nil {
			return err
		}
		remaining.Endpoint = ""
	default:
		return fmt.Errorf("%w: value kind", ErrInvalid)
	}
	if remaining != (Value{}) {
		return fmt.Errorf("%w: non-selected value field", ErrInvalid)
	}
	return nil
}
