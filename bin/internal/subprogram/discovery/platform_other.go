//go:build !linux

package discovery

import "github.com/asciimoth/almagest/bin/internal/subprogram/policy"

func defaultLinuxIdentity() policy.Linux { return policy.Linux{} }

func discoverRuntimeInputs(...string) ([]policy.ReadOnly, error) { return nil, nil }
