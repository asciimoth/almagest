//go:build linux && amd64

package linuxprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeUserRoles(t *testing.T) {
	nativeEnvironment(t)
	if os.Geteuid() == 0 {
		t.Skip("run this test as the non-root qualification user")
	}
	for _, role := range []Role{Runtime, Writer} {
		t.Run(string(role), func(t *testing.T) {
			output, err := launchNativeUser(t, role)
			if err != nil {
				t.Fatalf("%v\n%s", err, output)
			}
			var report struct {
				Role    Role
				Threads []int
				Checks  map[string]bool
			}
			if err := json.Unmarshal(output, &report); err != nil {
				t.Fatalf("decode report: %v: %s", err, output)
			}
			if report.Role != role || len(report.Threads) != 4 || len(report.Checks) < 25 {
				t.Fatalf("incomplete user profile report: %s", output)
			}
			for name, passed := range report.Checks {
				if !passed {
					t.Errorf("%s failed", name)
				}
			}
		})
	}
}

func launchNativeUser(t *testing.T, role Role) ([]byte, error) {
	t.Helper()
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	open := func(path string) *os.File {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	state, code := t.TempDir(), t.TempDir()
	for _, path := range []string{state, code} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stateFile, codeFile := open(state), open(code)
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(pair[0]), "user-parent"), os.NewFile(uintptr(pair[1]), "user-child")
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	plan := Plan{Profile: ProfileUser, Role: role, UID: uid, GID: gid, Limits: DefaultLimits()}
	input := Inputs{Launcher: open("/qualification/launcher"), Control: child,
		Executable: open("/qualification/probe"), Directory: stateFile, Code: codeFile}
	lanes := make([]*os.File, 0, 5)
	newLane := func() *os.File {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		writer, reader := os.NewFile(uintptr(fds[0]), "user-lane-parent"), os.NewFile(uintptr(fds[1]), "user-lane-child")
		t.Cleanup(func() { _ = writer.Close(); _ = reader.Close() })
		if _, err := writer.Write([]byte("profile-lane")); err != nil {
			t.Fatal(err)
		}
		lanes = append(lanes, writer)
		return reader
	}
	input.Diagnostics = newLane()
	if role == Runtime {
		input.Data, input.Admin, input.Config, input.Commands = newLane(), newLane(), newLane(), newLane()
		locales := t.TempDir()
		if err := os.WriteFile(filepath.Join(locales, "visible"), []byte("localized content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/passwd", filepath.Join(locales, "escape")); err != nil {
			t.Fatal(err)
		}
		input.Locales = open(locales)
	}
	command, err := CommandContext(ctx, plan, input)
	if err != nil {
		t.Fatal(err)
	}
	command.Env = []string{"UNTRUSTED_SENTINEL=must-not-survive", "PATH=/untrusted"}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if _, err := parent.Write([]byte("profile-channel")); err != nil {
		t.Fatal(err)
	}
	err = command.Run()
	if err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	return stdout.Bytes(), nil
}
