//go:build !linux

package roleentry

import "github.com/asciimoth/almagest/bin/internal/isolation/protocol"

func observeAppliedIsolation(Bootstrap) (AppliedIsolation, error) {
	return AppliedIsolation{}, protocol.ErrInvalidContract
}
