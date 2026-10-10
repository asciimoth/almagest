//go:build linux && amd64

package linuxprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Native checks must run only in the disposable qualification container.
func nativeEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv("ALMAGEST_PROFILE_TEST_NATIVE") != "1" {
		t.Skip("run just linux-isolation-profiles in Docker")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("Docker required")
	}
}

func launchNative(t *testing.T, role Role, change func(*exec.Cmd)) ([]byte, error) {
	t.Helper()
	nativeEnvironment(t)
	p := Plan{Role: role, UID: 65534, GID: 65534, Limits: DefaultLimits()}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	open := func(path string) *os.File {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	stateFile := open(state)
	if err := os.Chown(state, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chown(state, 0, 0) })
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(pair[0]), "parent"), os.NewFile(uintptr(pair[1]), "child")
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	in := Inputs{Launcher: open("/qualification/launcher"), Control: child,
		Executable: open("/qualification/probe"), Directory: stateFile, Cgroup: open("/sys/fs/cgroup")}
	lane := func() *os.File {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		writer, reader := os.NewFile(uintptr(fds[0]), "lane-parent"), os.NewFile(uintptr(fds[1]), "lane-child")
		t.Cleanup(func() { _ = writer.Close(); _ = reader.Close() })
		if _, err := writer.Write([]byte("profile-lane")); err != nil {
			t.Fatal(err)
		}
		return reader
	}
	in.Diagnostics = lane()
	if role == Runtime {
		in.Data = lane()
		in.Admin = lane()
	}
	if role == Command {
		in.Command = open("/qualification/command")
	}
	cmd, err := CommandContext(ctx, p, in)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = []string{"UNTRUSTED_SENTINEL=must-not-survive", "PATH=/untrusted"}
	// Inject a descriptor above the eventual hard limit. close_range must
	// remove it even though a loop to the new RLIMIT_NOFILE would miss it.
	for len(cmd.ExtraFiles) < 1997 {
		cmd.ExtraFiles = append(cmd.ExtraFiles, nil)
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, open("/etc/passwd")) // fd 2000
	if change != nil {
		change(cmd)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if _, err := parent.Write([]byte("profile-channel")); err != nil {
		t.Fatal(err)
	}
	err = cmd.Run()
	if err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	return stdout.Bytes(), nil
}

func TestNativeRoles(t *testing.T) {
	for _, role := range []Role{Runtime, Writer, Command} {
		t.Run(string(role), func(t *testing.T) {
			output, err := launchNative(t, role, nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, output)
			}
			var report struct {
				Role    Role
				Threads []int
				Checks  map[string]bool
			}
			if err := json.Unmarshal(output, &report); err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			if report.Role != role || len(report.Threads) != 4 || len(report.Checks) < 40 {
				t.Fatalf("incomplete report: %s", output)
			}
			seen := map[int]bool{}
			for _, tid := range report.Threads {
				seen[tid] = true
			}
			if len(seen) != 4 {
				t.Fatal("not four OS threads")
			}
			for name, ok := range report.Checks {
				if !ok {
					t.Errorf("%s failed", name)
				}
			}
			t.Logf("%s", output)
		})
	}
}

func TestNativeRejectsUnsafeInputs(t *testing.T) {
	cases := map[string]func(*exec.Cmd){
		"missing-channel":      func(c *exec.Cmd) { c.ExtraFiles[0] = nil },
		"file-channel":         func(c *exec.Cmd) { c.ExtraFiles[0] = c.ExtraFiles[1] },
		"fake-cgroup":          func(c *exec.Cmd) { c.ExtraFiles[3] = c.ExtraFiles[2] },
		"missing-executable":   func(c *exec.Cmd) { c.ExtraFiles[1] = nil },
		"directory-executable": func(c *exec.Cmd) { c.ExtraFiles[1] = c.ExtraFiles[2] },
		"file-storage":         func(c *exec.Cmd) { c.ExtraFiles[2] = c.ExtraFiles[1] },
		"root-uid":             func(c *exec.Cmd) { c.Args[3] = "0" },
		"unknown-role":         func(c *exec.Cmd) { c.Args[2] = "dns-unrestricted" },
		"backend-role":         func(c *exec.Cmd) { c.Args[2] = "network" },
		"overflow":             func(c *exec.Cmd) { c.Args[3] = "18446744073709551616" },
		"signed":               func(c *exec.Cmd) { c.Args[3] = "+65534" },
		"cap-expansion":        func(c *exec.Cmd) { c.Args[5] = "2097152" },
		"unmet-memory":         func(c *exec.Cmd) { c.Args[8] = "33554432" },
		"unmet-tasks":          func(c *exec.Cmd) { c.Args[9] = "8" },
		"unmet-cpu":            func(c *exec.Cmd) { c.Args[10] = "1000" },
		"extra-argument":       func(c *exec.Cmd) { c.Args = append(c.Args, "/escape") },
		"file-data-channel":    func(c *exec.Cmd) { c.ExtraFiles[6] = c.ExtraFiles[1] },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			output, err := launchNative(t, Runtime, change)
			if err == nil || !strings.Contains(string(output), "--no-isolation") {
				t.Fatalf("unsafe launch: %v %s", err, output)
			}
		})
	}
}

func TestNativeELFRejection(t *testing.T) {
	nativeEnvironment(t)
	for _, data := range [][]byte{[]byte("#!/bin/sh\nexit 0\n"), make([]byte, 128), []byte("\x7fELF")} {
		output, err := launchNative(t, Runtime, func(c *exec.Cmd) {
			path := filepath.Join(t.TempDir(), "executable")
			if err := os.WriteFile(path, data, 0555); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			c.ExtraFiles[1] = f
		})
		if err == nil || !bytes.Contains(output, []byte("--no-isolation")) {
			t.Fatalf("accepted ELF: %v %s", err, output)
		}
	}
}

func TestNativeTSyncFailure(t *testing.T) {
	output, err := launchNative(t, Runtime, func(c *exec.Cmd) {
		f, err := os.Open("/qualification/tsync-probe")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		c.ExtraFiles[1] = f
	})
	if err != nil || !bytes.Contains(output, []byte("TSYNC divergence rejected")) {
		t.Fatalf("%v: %s", err, output)
	}
}

func TestNativeAlternateABI(t *testing.T) {
	output, err := launchNative(t, Runtime, func(c *exec.Cmd) {
		f, err := os.Open("/qualification/x32-probe")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		c.ExtraFiles[1] = f
	})
	if err == nil || !bytes.Contains(output, []byte("signal 31")) {
		t.Fatalf("alternate ABI not killed: %v %s", err, output)
	}
}

func TestNativeRequiredRestrictions(t *testing.T) {
	if os.Getenv("ALMAGEST_PROFILE_EXPECT_UNAVAILABLE") != "1" {
		t.Skip("requires deliberately missing restrictions")
	}
	for _, role := range []Role{Runtime, Writer, Command} {
		t.Run(string(role), func(t *testing.T) {
			output, err := launchNative(t, role, nil)
			if err == nil || !bytes.Contains(output, []byte("--no-isolation")) || bytes.Contains(output, []byte(`"Checks"`)) {
				t.Fatalf("restriction failure did not stop startup: %v %s", err, output)
			}
		})
	}
}

func FuzzNativeArguments(f *testing.F) {
	if os.Getenv("ALMAGEST_PROFILE_TEST_NATIVE") != "1" {
		f.Skip("native parser fuzzing requires the Docker qualification")
	}
	f.Add("runtime", "65534", "0", "1024")
	f.Add("unknown", "-1", "18446744073709551616", "")
	f.Fuzz(func(t *testing.T, role, uid, caps, fds string) {
		nativeEnvironment(t)
		for _, value := range []string{role, uid, caps, fds} {
			if len(value) > 1024 || strings.ContainsRune(value, 0) {
				return
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/qualification/launcher", "v1", role, uid, "65534", caps, fds,
			"268435456", "536870912", "128", "100000")
		cmd.Env = []string{}
		// No inherited control, storage or cgroup descriptors: even a valid
		// decimal combination must fail before any namespace or mount operation.
		output, err := cmd.CombinedOutput()
		if err == nil || ctx.Err() != nil || !bytes.Contains(output, []byte("--no-isolation")) {
			t.Fatalf("native parser failure: %v %s", err, output)
		}
	})
}

func TestSealFilter(t *testing.T) {
	// Inspect BPF without restricting the test runner or any host process.
	for _, role := range []Role{Runtime, Writer, Command} {
		f := sealFilter(role)
		for _, tc := range []struct{ arch, call, want uint32 }{
			{unix.AUDIT_ARCH_I386, unix.SYS_READ, unix.SECCOMP_RET_KILL_PROCESS},
			{unix.AUDIT_ARCH_X86_64, unix.SYS_EXECVEAT, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
			{unix.AUDIT_ARCH_X86_64, unix.SYS_READ, unix.SECCOMP_RET_ALLOW},
		} {
			if got := evaluateFilter(t, f, tc.arch, tc.call); got != tc.want {
				t.Fatalf("%s: %#x != %#x", role, got, tc.want)
			}
		}
		want := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
		if role == Command {
			want = unix.SECCOMP_RET_ALLOW
		}
		if evaluateFilter(t, f, unix.AUDIT_ARCH_X86_64, unix.SYS_EXECVE) != want {
			t.Fatal("exec policy")
		}
	}
	if err := Seal(Network); err == nil {
		t.Fatal("trusted backend accepted by restricted seal")
	}
	if err := Seal("unknown"); err == nil {
		t.Fatal("unknown role")
	}
}

func TestHeldLaunchContract(t *testing.T) {
	p := Plan{Role: Runtime, UID: 65534, GID: 65534, Limits: DefaultLimits()}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	in := Inputs{Launcher: f, Control: f, Executable: f, Directory: f, Cgroup: f}
	// This is a construction test only. Native validation rejects these handles.
	cmd, err := CommandContext(context.Background(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/proc/self/fd/8" || cmd.Dir != "/" || len(cmd.Env) != 0 || len(cmd.ExtraFiles) != 9 || cmd.ExtraFiles[5] != f {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.UseCgroupFD || cmd.SysProcAttr.CgroupFD != int(f.Fd()) {
		t.Fatal("role launch does not atomically enter the held cgroup")
	}
	for _, missing := range []func(*Inputs){func(i *Inputs) { i.Launcher = nil }, func(i *Inputs) { i.Control = nil },
		func(i *Inputs) { i.Executable = nil }, func(i *Inputs) { i.Directory = nil }, func(i *Inputs) { i.Cgroup = nil },
		func(i *Inputs) { i.Command = f }} {
		bad := in
		missing(&bad)
		if _, err := CommandContext(context.Background(), p, bad); err == nil {
			t.Fatal("accepted incomplete/extra authority")
		}
	}
	if _, err := CommandContext(nil, p, in); err == nil { //nolint:staticcheck // Verify rejection of an invalid caller context.
		t.Fatal("nil context")
	}
	p.Role = Command
	if _, err := CommandContext(context.Background(), p, in); err == nil {
		t.Fatal("missing command")
	}
	in.Command = f
	if _, err := CommandContext(context.Background(), p, in); err != nil {
		t.Fatal(err)
	}
	p.Role = "invalid"
	if _, err := CommandContext(context.Background(), p, in); err == nil {
		t.Fatal("unknown profile")
	}
}

func evaluateFilter(t *testing.T, f []unix.SockFilter, arch, call uint32) uint32 {
	t.Helper()
	var a uint32
	for pc := 0; pc < len(f); pc++ {
		op := f[pc]
		switch op.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			if op.K == 4 {
				a = arch
			} else {
				a = call
			}
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if a == op.K {
				pc += int(op.Jt)
			} else {
				pc += int(op.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return op.K
		default:
			t.Fatalf("unsupported instruction %v", op)
		}
	}
	t.Fatal(io.ErrUnexpectedEOF)
	return 0
}
