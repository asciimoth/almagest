//go:build !linux

package direct

import (
	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/asciimoth/almagest/lib/sandbox"
)

func nativeIdentity(policy.Linux) (uint32, uint32, bool, error) {
	return 0, 0, false, sandbox.ErrUnavailable
}
