//go:build windows

package networkrole

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/windowsprofile"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
)

func windowsValidationFixture(t *testing.T) WindowsLaunch {
	t.Helper()
	storage, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	journal, err := supervision.OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	provider := tlsplatform.ProviderFunc(func(context.Context, tlsplatform.Request) (tlsplatform.Snapshot, error) {
		return tlsplatform.Snapshot{}, nil
	})
	return WindowsLaunch{
		Runtime:      WindowsRole{Plan: windowsprofile.DefaultPlan(windowsprofile.Runtime), Identity: &windowsprofile.Identity{}, Executable: `C:\Almagest\almagestd.exe`, Storage: storage},
		RuntimeInput: roleentry.RuntimeInput{Version: protocol.Version}, RuntimeStorage: &bootstrap.RuntimeStorageInput{Regular: []byte(`{}`), TLSPlatform: []byte(`{}`)}, RuntimeDir: directory,
		Policy: policy.Default(policy.LaunchAuthority{Backend: "windows", CredentialClass: "test"}, policy.CommandAuthority{}), Journal: journal, TLSPlatform: provider,
	}
}

func TestWindowsLaunchRejectsInvalidAuthorityBeforeExecution(t *testing.T) {
	changes := map[string]func(*WindowsLaunch){
		"missing identity":      func(l *WindowsLaunch) { l.Runtime.Identity = nil },
		"missing directory":     func(l *WindowsLaunch) { l.RuntimeDir = nil },
		"different directory":   func(l *WindowsLaunch) { l.Runtime.Storage = filepath.Join(l.Runtime.Storage, "other") },
		"missing storage input": func(l *WindowsLaunch) { l.RuntimeStorage = nil },
		"missing journal":       func(l *WindowsLaunch) { l.Journal = nil },
		"wrong role":            func(l *WindowsLaunch) { l.Runtime.Plan = windowsprofile.DefaultPlan(windowsprofile.Writer) },
		"invalid role limits":   func(l *WindowsLaunch) { l.Runtime.Plan.CPUPercent = 0 },
		"wrong backend":         func(l *WindowsLaunch) { l.Policy.Launch.Backend = "linux" },
		"command authority":     func(l *WindowsLaunch) { l.Policy.Commands.NodeCommands = []string{"run"} },
		"invalid runtime input": func(l *WindowsLaunch) { l.RuntimeInput.Version++ },
		"shared writer identity": func(l *WindowsLaunch) {
			l.Writer = &WindowsWriter{WindowsRole: WindowsRole{Plan: windowsprofile.DefaultPlan(windowsprofile.Writer), Identity: l.Runtime.Identity, Executable: `C:\Almagest\writer.exe`, Storage: `C:\Almagest\writer`}, Target: "alm.hujson"}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			launch := windowsValidationFixture(t)
			if err := launch.validate(); err != nil {
				t.Fatal(err)
			}
			change(&launch)
			if err := launch.validate(); !errors.Is(err, supervision.ErrInvalidComposition) {
				t.Fatalf("invalid launch = %v", err)
			}
		})
	}
}
