package dnsnodes

import (
	"errors"
	"math"
	"time"

	"github.com/asciimoth/formular"
)

const maxDNSTimeoutMilliseconds = int64(math.MaxInt64) / int64(time.Millisecond)

// dnsTimeoutDuration converts a validated millisecond menu value to the
// duration used by gonnect. Zero stays zero so gonnect can select its default.
func dnsTimeoutDuration(milliseconds int) time.Duration {
	return time.Duration(milliseconds) * time.Millisecond
}

// validateDNSTimeoutMilliseconds rejects values that cannot be represented as
// a non-negative time.Duration. Zero is valid and means "use the default".
func validateDNSTimeoutMilliseconds(milliseconds int) error {
	if milliseconds < 0 {
		return errors.New("timeout must not be negative")
	}
	if int64(milliseconds) > maxDNSTimeoutMilliseconds {
		return errors.New("timeout is too large")
	}
	return nil
}

// dnsTimeoutErrorText returns the saved-config validation error for a menu
// snapshot. An empty string means the timeout is valid.
func dnsTimeoutErrorText(milliseconds int) string {
	if err := validateDNSTimeoutMilliseconds(milliseconds); err != nil {
		return err.Error()
	}
	return ""
}

// dnsTimeoutField builds the common integer timeout control used by DNS nodes.
// The caller supplies text that describes what one timeout bounds.
func dnsTimeoutField(id, label string, value int, help, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusText != "" {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	item := formular.IntField(id, label, value, opts...)
	item.Help = help + " Set 0 to use the gonnect default."
	return item
}

// validateDNSTimeoutField validates an integer timeout Formular value. Values
// of other types are ignored because Formular reports type errors separately.
func validateDNSTimeoutField(value any) error {
	milliseconds, ok := formular.IntValue(value)
	if !ok {
		return nil
	}
	return validateDNSTimeoutMilliseconds(milliseconds)
}
