//go:build !linux

package direct

import (
	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func nativeIdentity(policy.Linux) (uint32, uint32, bool, error) {
	return 0, 0, false, sandbox.ErrUnavailable
}
