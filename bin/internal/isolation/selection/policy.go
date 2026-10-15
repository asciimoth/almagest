// Package selection resolves the daemon isolation policy and selects a fixed
// implementation profile. It does not probe the host or apply restrictions.
package selection

import (
	"fmt"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
)

type Policy string

const (
	PolicyStrict Policy = "strict"
	PolicyAuto   Policy = "auto"
	PolicyOff    Policy = "off"
)

type Profile string

const (
	ProfileLinuxFullV1 Profile = "linux-full-v1"
	ProfileLinuxUserV1 Profile = "linux-user-v1"
)

type Platform struct {
	OS   string
	Arch string
}

func (p Platform) supportsLinuxProfiles() bool {
	return p.OS == "linux" && p.Arch == "amd64"
}

// DefaultPolicy returns the compatibility default for a platform. Linux uses
// automatic profile selection. Windows stays in direct mode. Other targets
// keep a fail-closed policy until a profile is qualified.
func DefaultPolicy(platform Platform) Policy {
	switch platform.OS {
	case "linux":
		return PolicyAuto
	case "windows":
		return PolicyOff
	default:
		return PolicyStrict
	}
}

// ResolvePolicy applies the public option aliases and platform rules. It does
// not inspect profile prerequisites.
func ResolvePolicy(arguments daemonargs.Args, platform Platform) (Policy, error) {
	policy := DefaultPolicy(platform)
	if arguments.Isolation != "" {
		policy = Policy(arguments.Isolation)
	}
	if policy != PolicyStrict && policy != PolicyAuto && policy != PolicyOff {
		return "", fmt.Errorf("invalid --isolation value %q: use strict, auto, or off", arguments.Isolation)
	}
	if arguments.NoIsolation {
		if arguments.Isolation != "" && policy != PolicyOff {
			return "", fmt.Errorf("--no-isolation conflicts with --isolation=%s", policy)
		}
		policy = PolicyOff
	}
	if arguments.Experimental && policy != PolicyOff {
		return "", fmt.Errorf("--experimental requires explicit --no-isolation or --isolation=off")
	}
	if policy != PolicyOff && !platform.supportsLinuxProfiles() {
		return "", fmt.Errorf("isolation policy %s is not qualified on %s/%s; use explicit --isolation=off", policy, platform.OS, platform.Arch)
	}
	if arguments.IsolationStateDir != "" && (arguments.Isolation != string(PolicyAuto) || policy != PolicyAuto || !platform.supportsLinuxProfiles()) {
		return "", fmt.Errorf("--isolation-state-dir requires explicit --isolation=auto on Linux/amd64")
	}
	return policy, nil
}

type ResourceRequirements struct {
	PerRoleCgroups bool
	ProcessLimits  bool
}

// Resources reports mechanisms that explicit resource options require.
func Resources(arguments daemonargs.Args) ResourceRequirements {
	return ResourceRequirements{
		PerRoleCgroups: arguments.IsolationMemoryBytes != nil || arguments.IsolationTasks != nil || arguments.IsolationCPUQuota != nil,
		ProcessLimits:  arguments.IsolationDescriptors != nil || arguments.IsolationFileBytes != nil,
	}
}

type ProfileRequest struct {
	Policy Policy

	Platform Platform
	UID      uint32

	FullAvailable              bool
	UserAvailable              bool
	UserCgroupsAvailable       bool
	UserProcessLimitsAvailable bool

	IsolationStateDir string
	Resources         ResourceRequirements
}

// SelectProfile selects one complete profile from preflight results. It never
// selects direct mode and never removes an explicit resource requirement.
func SelectProfile(request ProfileRequest) (Profile, error) {
	if request.Policy == PolicyOff {
		return "", fmt.Errorf("direct mode does not select an isolation profile")
	}
	if request.Policy != PolicyStrict && request.Policy != PolicyAuto {
		return "", fmt.Errorf("invalid resolved isolation policy %q", request.Policy)
	}
	if !request.Platform.supportsLinuxProfiles() {
		return "", fmt.Errorf("no isolation profile is qualified on %s/%s", request.Platform.OS, request.Platform.Arch)
	}
	if request.Policy == PolicyStrict {
		if request.IsolationStateDir != "" {
			return "", fmt.Errorf("--isolation-state-dir is not available with strict isolation")
		}
		if !request.FullAvailable {
			return "", fmt.Errorf("linux-full-v1 prerequisites are unavailable")
		}
		return ProfileLinuxFullV1, nil
	}
	if request.FullAvailable {
		if request.IsolationStateDir != "" {
			return "", fmt.Errorf("--isolation-state-dir requires linux-user-v1")
		}
		return ProfileLinuxFullV1, nil
	}
	if request.UID == 0 {
		return "", fmt.Errorf("a root caller cannot use the same-UID user profile")
	}
	if !request.UserAvailable {
		return "", fmt.Errorf("linux-user-v1 prerequisites are unavailable")
	}
	if request.Resources.PerRoleCgroups && !request.UserCgroupsAvailable {
		return "", fmt.Errorf("explicit memory, task, or CPU limits require per-role cgroups")
	}
	if request.Resources.ProcessLimits && !request.UserProcessLimitsAvailable {
		return "", fmt.Errorf("explicit descriptor or file-size limits require verified process limits")
	}
	return ProfileLinuxUserV1, nil
}
