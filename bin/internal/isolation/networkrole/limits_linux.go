//go:build linux && amd64

package networkrole

import (
	"fmt"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
)

// linuxProductionLimits resolves startup overrides before config is loaded or
// any role is started. An explicit zero must fail validation, not select defaults.
func linuxProductionLimits(args daemonargs.Args, limits linuxprofile.Limits) (linuxprofile.Limits, error) {
	if limits == (linuxprofile.Limits{}) {
		limits = linuxprofile.DefaultLimits()
	}
	for _, field := range []struct {
		name   string
		value  *uint64
		target *uint64
	}{
		{"isolation-memory-bytes", args.IsolationMemoryBytes, &limits.MemoryBytes},
		{"isolation-tasks", args.IsolationTasks, &limits.Tasks},
		{"isolation-cpu-quota", args.IsolationCPUQuota, &limits.CPUQuota},
		{"isolation-descriptors", args.IsolationDescriptors, &limits.Descriptors},
		{"isolation-file-bytes", args.IsolationFileBytes, &limits.FileBytes},
	} {
		if field.value != nil {
			*field.target = *field.value
			if err := limits.Validate(); err != nil {
				return linuxprofile.Limits{}, fmt.Errorf("--%s=%d: %w", field.name, *field.value, err)
			}
		}
	}
	if err := limits.Validate(); err != nil {
		return linuxprofile.Limits{}, err
	}
	return limits, nil
}
