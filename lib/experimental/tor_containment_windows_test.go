//go:build windows

package experimentalnodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	tor "github.com/asciimoth/tor-driver"
	"golang.org/x/sys/windows"
)

var xTorTokenHelperOutput = flag.String("xtor-token-helper-output", "", "write XTorDriver token state and exit")

func TestXTorDriverWindowsBestEffortWarnsWithoutContainment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	system, _, err := newDirectXTorSystem(tor.Config{TorExecutable: executable})
	if err != nil {
		t.Fatalf("newDirectXTorSystem: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("close system: %v", err)
		}
	})
	logger := &xTorWarningLogger{}
	deps := system.Dependencies(nil, nil, logger)
	if deps.Processes == nil {
		t.Fatal("best-effort system has no process adapter")
	}
	if warnings := strings.Join(logger.warnings, "\n"); !strings.Contains(warnings, "host network directly") {
		t.Fatalf("containment warnings = %q", warnings)
	}
}

func TestWindowsXTorElevatedParentDropsAdministratorPrivileges(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("administrator-token transition requires the elevated WinVM suite")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	system, _, err := newDirectXTorSystem(tor.Config{TorExecutable: executable})
	if err != nil {
		t.Fatalf("newDirectXTorSystem: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("close system: %v", err)
		}
	})
	output := t.TempDir() + `\token.json`
	deps := system.Dependencies(nil, nil, tor.NopLogger{})
	var processOutput bytes.Buffer
	process, err := deps.Processes.Start(context.Background(), tor.Launch{
		Executable: executable,
		Args: []string{
			"-test.run=^TestXTorTokenHelper$",
			"-xtor-token-helper-output=" + output,
		},
		Stdout: &processOutput,
		Stderr: &processOutput,
	})
	if err != nil {
		t.Fatalf("start restricted helper: %v", err)
	}
	if err = errors.Join(process.Wait(), process.Release()); err != nil {
		t.Fatalf("restricted helper: %v\n%s", err, processOutput.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read token evidence: %v", err)
	}
	var evidence struct {
		Elevated      bool `json:"elevated"`
		Administrator bool `json:"administrator"`
	}
	if err = json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Elevated || evidence.Administrator {
		t.Fatalf("Tor child token = %+v, want no administrator privileges", evidence)
	}
}

func TestXTorTokenHelper(t *testing.T) {
	if *xTorTokenHelperOutput == "" {
		t.Skip("helper process only")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE,
		&token,
	); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = token.Close() }()
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := xTorTokenIsMember(token, adminSID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Elevated      bool `json:"elevated"`
		Administrator bool `json:"administrator"`
	}{Elevated: token.IsElevated(), Administrator: admin})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(*xTorTokenHelperOutput, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type xTorWarningLogger struct {
	tor.NopLogger
	warnings []string
}

func (l *xTorWarningLogger) Warn(args ...any) {
	l.warnings = append(l.warnings, fmt.Sprint(args...))
}

func (l *xTorWarningLogger) Warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}
