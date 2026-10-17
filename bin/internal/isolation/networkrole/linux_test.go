//go:build linux && amd64

package networkrole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
)

func validationFixture(t *testing.T) LinuxLaunch {
	t.Helper()
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	journal, err := supervision.OpenJournal(filepath.Join(t.TempDir(), "journal.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	return LinuxLaunch{Runtime: LinuxRole{Plan: linuxprofile.Plan{Role: linuxprofile.Runtime, UID: 123, GID: 123, Limits: linuxprofile.DefaultLimits()}, Inputs: linuxprofile.Inputs{Launcher: file, Executable: file, Directory: file, Cgroup: file}}, RuntimeStorage: &bootstrap.RuntimeStorageInput{Regular: []byte(`{}`)}, State: file, Cgroup: file, Journal: journal, Limits: linuxprofile.DefaultLimits(), RuntimeInput: roleentry.RuntimeInput{Version: protocol.Version}, Policy: policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: "test"}, policy.CommandAuthority{})}
}

func TestLinuxLaunchRejectsInvalidAuthorityBeforeExecution(t *testing.T) {
	changes := map[string]func(*LinuxLaunch){
		"missing state":         func(l *LinuxLaunch) { l.State = nil },
		"missing cgroup":        func(l *LinuxLaunch) { l.Cgroup = nil },
		"missing journal":       func(l *LinuxLaunch) { l.Journal = nil },
		"missing runtime input": func(l *LinuxLaunch) { l.RuntimeStorage = nil },
		"ambiguous cgroups":     func(l *LinuxLaunch) { l.Cgroups = &linuxprofile.RoleCgroups{} },
		"wrong backend":         func(l *LinuxLaunch) { l.Policy.Launch.Backend = "windows" },
		"missing executable":    func(l *LinuxLaunch) { l.Runtime.Inputs.Executable = nil },
		"caller channel":        func(l *LinuxLaunch) { l.Runtime.Inputs.Control = l.State },
		"caller callback lane":  func(l *LinuxLaunch) { l.Runtime.Inputs.Callbacks = l.State },
		"root runtime":          func(l *LinuxLaunch) { l.Runtime.Plan.UID = 0 },
		"wrong role":            func(l *LinuxLaunch) { l.Runtime.Plan.Role = linuxprofile.Network },
		"invalid N limits":      func(l *LinuxLaunch) { l.Limits.Tasks = 0 },
		"missing helper":        func(l *LinuxLaunch) { l.Policy.Commands.NodeCommands = []string{"fixed"} },
		"shared identity": func(l *LinuxLaunch) {
			role := l.Runtime
			role.Plan.Role = linuxprofile.Writer
			l.Writer = &LinuxWriter{LinuxRole: role, Target: "config.hujson"}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			launch := validationFixture(t)
			if err := launch.validate(); err != nil {
				t.Fatal(err)
			}
			change(&launch)
			if err := RunLinux(context.Background(), launch, func(roleentry.RuntimeIsolationStatus) error {
				t.Error("published readiness for invalid launch")
				return nil
			}); !errors.Is(err, supervision.ErrInvalidComposition) {
				t.Fatalf("invalid launch: %v", err)
			}
		})
	}
}

func TestLinuxLaunchRejectsMismatchedCallbackAuthority(t *testing.T) {
	launch := validationFixture(t)
	helper := launch.Runtime
	helper.Plan.Role, helper.Plan.UID, helper.Plan.GID = linuxprofile.Command, 124, 124
	helper.Inputs.Command = launch.State
	launch.Helper = &LinuxHelper{LinuxRole: helper, Config: commandhelper.Config{Callbacks: map[string]commandhelper.Callback{"event": {}}}}
	if err := launch.validate(); !errors.Is(err, supervision.ErrInvalidComposition) {
		t.Fatalf("accepted mismatched callbacks: %v", err)
	}
}

func TestLinuxLaunchRejectsInvalidWriterTargetBeforeExecution(t *testing.T) {
	launch := validationFixture(t)
	writer := launch.Runtime
	writer.Plan.Role, writer.Plan.UID, writer.Plan.GID = linuxprofile.Writer, 124, 124
	launch.Writer = &LinuxWriter{LinuxRole: writer, Target: "../protected"}
	if err := RunLinux(context.Background(), launch, nil); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("writer target: %v", err)
	}
}

func TestLinuxIsolationStatusReportsOnlyEnforcedRoleRestrictions(t *testing.T) {
	disabled := linuxIsolationStatus(linuxprofile.ProfileFull, false, false, false, false)
	if disabled.Roles["config_writer"] != "disabled" || disabled.Roles["command_helper"] != "disabled" ||
		disabled.Restrictions["config_writer"] != nil || disabled.Restrictions["command_helper"] != nil ||
		disabled.Capabilities["config_persistence"] || disabled.Capabilities["commands"] || disabled.Capabilities["backend_callbacks"] {
		t.Fatalf("disabled status = %#v", disabled)
	}
	enabled := linuxIsolationStatus(linuxprofile.ProfileFull, true, true, true, true)
	if enabled.Roles["config_writer"] != "ready" || enabled.Roles["command_helper"] != "ready" ||
		enabled.Roles["sandbox_supervisor"] != "ready" || len(enabled.Restrictions["sandbox_supervisor"]) == 0 ||
		len(enabled.Restrictions["config_writer"]) == 0 || len(enabled.Restrictions["command_helper"]) == 0 ||
		!enabled.Capabilities["config_persistence"] || !enabled.Capabilities["commands"] || !enabled.Capabilities["subprogram_sandboxes"] || !enabled.Capabilities["backend_callbacks"] {
		t.Fatalf("enabled status = %#v", enabled)
	}
}

func TestSandboxConfigured(t *testing.T) {
	for _, value := range [][]byte{nil, []byte("null"), []byte(" {} \n"), []byte(`{"Mode":"off"}`)} {
		if sandboxConfigured(value) {
			t.Fatalf("empty sandbox config %q was active", value)
		}
	}
	for _, value := range [][]byte{[]byte(`{"Programs":{}}`), []byte(`{"Templates":{}}`), []byte(`{broken`)} {
		if !sandboxConfigured(value) {
			t.Fatalf("sandbox config %q was ignored", value)
		}
	}
}

func TestManagedSandboxCgroupAndStartupLogging(t *testing.T) {
	data, err := setSandboxCgroupRoot([]byte(`{"Mode":"auto"}`), "/sys/fs/cgroup/almagest/sandbox")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value["LinuxCgroupRoot"] != "/sys/fs/cgroup/almagest/sandbox" {
		t.Fatalf("managed cgroup root = %#v", value["LinuxCgroupRoot"])
	}

	status := roleentry.RuntimeIsolationStatus{RequestedPolicy: "auto", ProfileID: "linux-full-v1",
		Mechanisms: map[string]map[string]roleentry.RuntimeIsolationMechanism{"runtime": {}}}
	var output bytes.Buffer
	logAdaptiveLinuxStatus(&output, "selected", status, false)
	if output.Len() != 0 {
		t.Fatalf("normal startup log contains diagnostics: %q", output.String())
	}
	logAdaptiveLinuxStatus(&output, "selected", status, true)
	if got := output.String(); !strings.Contains(got, "DEBUG isolation state=selected") || strings.Contains(got, "effective_caps") {
		t.Fatalf("debug startup log is not concise: %q", got)
	}
}
