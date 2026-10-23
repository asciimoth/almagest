package selection

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
)

var (
	linuxAMD64   = Platform{OS: "linux", Arch: "amd64"}
	windowsAMD64 = Platform{OS: "windows", Arch: "amd64"}
)

func TestResolvePolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments daemonargs.Args
		platform  Platform
		want      Policy
		wantError string
	}{
		{name: "Linux default is off", platform: linuxAMD64, want: PolicyOff},
		{name: "Windows default is off", platform: windowsAMD64, want: PolicyOff},
		{name: "strict", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "strict"}, want: PolicyStrict},
		{name: "automatic", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "auto"}, want: PolicyAuto},
		{name: "off", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "off"}, want: PolicyOff},
		{name: "compatibility alias", platform: linuxAMD64, arguments: daemonargs.Args{NoIsolation: true}, want: PolicyOff},
		{name: "consistent aliases", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "off", NoIsolation: true}, want: PolicyOff},
		{name: "conflicting strict alias", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "strict", NoIsolation: true}, wantError: "conflicts"},
		{name: "conflicting automatic alias", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "auto", NoIsolation: true}, wantError: "conflicts"},
		{name: "unknown value", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "AUTO"}, wantError: "invalid --isolation"},
		{name: "experimental automatic", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "auto", Experimental: true}, wantError: "--experimental requires"},
		{name: "experimental direct", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "off", Experimental: true}, want: PolicyOff},
		{name: "unsupported explicit strict", platform: windowsAMD64, arguments: daemonargs.Args{Isolation: "strict"}, wantError: "not qualified"},
		{name: "unsupported explicit automatic", platform: windowsAMD64, arguments: daemonargs.Args{Isolation: "auto"}, wantError: "not qualified"},
		{name: "unsupported Linux architecture defaults to off", platform: Platform{OS: "linux", Arch: "arm64"}, want: PolicyOff},
		{name: "unsupported Linux architecture rejects explicit automatic", platform: Platform{OS: "linux", Arch: "arm64"}, arguments: daemonargs.Args{Isolation: "auto"}, wantError: "not qualified"},
		{name: "unsupported target fails closed", platform: Platform{OS: "freebsd", Arch: "amd64"}, wantError: "not qualified"},
		{name: "unsupported target explicit off", platform: Platform{OS: "freebsd", Arch: "amd64"}, arguments: daemonargs.Args{Isolation: "off"}, want: PolicyOff},
		{name: "user state root", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "auto", IsolationStateDir: "/state"}, want: PolicyAuto},
		{name: "user state root needs explicit automatic policy", platform: linuxAMD64, arguments: daemonargs.Args{IsolationStateDir: "/state"}, wantError: "explicit --isolation=auto"},
		{name: "state root with strict", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "strict", IsolationStateDir: "/state"}, wantError: "--isolation-state-dir requires"},
		{name: "state root with direct", platform: linuxAMD64, arguments: daemonargs.Args{Isolation: "off", IsolationStateDir: "/state"}, wantError: "--isolation-state-dir requires"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolvePolicy(test.arguments, test.platform)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("ResolvePolicy() error = %v, want text %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ResolvePolicy() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSelectProfile(t *testing.T) {
	base := ProfileRequest{Policy: PolicyAuto, Platform: linuxAMD64, UID: 1000, UserAvailable: true}
	for _, test := range []struct {
		name      string
		change    func(*ProfileRequest)
		want      Profile
		wantError string
	}{
		{name: "automatic user", want: ProfileLinuxUserV1},
		{name: "automatic prefers full", change: func(r *ProfileRequest) { r.FullAvailable = true }, want: ProfileLinuxFullV1},
		{name: "strict full", change: func(r *ProfileRequest) { r.Policy = PolicyStrict; r.FullAvailable = true }, want: ProfileLinuxFullV1},
		{name: "strict unavailable", change: func(r *ProfileRequest) { r.Policy = PolicyStrict }, wantError: "linux-full-v1 prerequisites"},
		{name: "root cannot use user", change: func(r *ProfileRequest) { r.UID = 0 }, wantError: "root caller"},
		{name: "user unavailable", change: func(r *ProfileRequest) { r.UserAvailable = false }, wantError: "linux-user-v1 prerequisites"},
		{name: "explicit cgroup option unavailable", change: func(r *ProfileRequest) { r.Resources.PerRoleCgroups = true }, wantError: "require per-role cgroups"},
		{name: "explicit cgroup option available", change: func(r *ProfileRequest) { r.Resources.PerRoleCgroups = true; r.UserCgroupsAvailable = true }, want: ProfileLinuxUserV1},
		{name: "process limits do not require cgroups", change: func(r *ProfileRequest) { r.Resources.ProcessLimits = true; r.UserProcessLimitsAvailable = true }, want: ProfileLinuxUserV1},
		{name: "process limits must be verified", change: func(r *ProfileRequest) { r.Resources.ProcessLimits = true }, wantError: "verified process limits"},
		{name: "state directory selects user only", change: func(r *ProfileRequest) { r.IsolationStateDir = "/state" }, want: ProfileLinuxUserV1},
		{name: "state directory rejects full", change: func(r *ProfileRequest) { r.IsolationStateDir = "/state"; r.FullAvailable = true }, wantError: "requires linux-user-v1"},
		{name: "direct has no profile", change: func(r *ProfileRequest) { r.Policy = PolicyOff }, wantError: "direct mode"},
		{name: "unsupported platform", change: func(r *ProfileRequest) { r.Platform = windowsAMD64 }, wantError: "no isolation profile"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			if test.change != nil {
				test.change(&request)
			}
			got, err := SelectProfile(request)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("SelectProfile() error = %v, want text %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("SelectProfile() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResourceRequirements(t *testing.T) {
	value := uint64(1)
	for _, test := range []struct {
		name string
		args daemonargs.Args
		want ResourceRequirements
	}{
		{name: "none"},
		{name: "memory", args: daemonargs.Args{IsolationMemoryBytes: &value}, want: ResourceRequirements{PerRoleCgroups: true}},
		{name: "tasks", args: daemonargs.Args{IsolationTasks: &value}, want: ResourceRequirements{PerRoleCgroups: true}},
		{name: "CPU", args: daemonargs.Args{IsolationCPUQuota: &value}, want: ResourceRequirements{PerRoleCgroups: true}},
		{name: "descriptors", args: daemonargs.Args{IsolationDescriptors: &value}, want: ResourceRequirements{ProcessLimits: true}},
		{name: "file size", args: daemonargs.Args{IsolationFileBytes: &value}, want: ResourceRequirements{ProcessLimits: true}},
		{name: "both mechanisms", args: daemonargs.Args{IsolationMemoryBytes: &value, IsolationDescriptors: &value}, want: ResourceRequirements{PerRoleCgroups: true, ProcessLimits: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Resources(test.args); got != test.want {
				t.Fatalf("Resources() = %+v, want %+v", got, test.want)
			}
		})
	}
}
