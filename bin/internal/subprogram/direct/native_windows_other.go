//go:build !windows

package direct

import (
	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

type windowsInstanceBoundary struct{}

func windowsNativePrerequisites(policy.Template) error { return sandbox.ErrUnavailable }
func (*instance) startWindowsBoundary() error          { return sandbox.ErrUnavailable }
func (s *instance) closeWindowsBoundary() error {
	s.windowsBoundary = nil
	return nil
}
