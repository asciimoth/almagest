package direct

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func TestDirectHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		return
	}
	if separator+1 >= len(os.Args) {
		os.Exit(90)
	}
	switch os.Args[separator+1] {
	case "echo":
		if _, err := fmt.Fprint(os.Stdout, os.Args[separator+2]); err != nil {
			os.Exit(92)
		}
	case "environment":
		if _, err := fmt.Fprintf(os.Stdout, "%s|%s|%s", os.Getenv("ALMAGEST_SANDBOX_TEMP"), os.Getenv("HOME"), os.Getenv("UNLISTED_SANDBOX_TEST")); err != nil {
			os.Exit(92)
		}
	case "identity":
		if _, err := fmt.Fprint(os.Stdout, helperIdentity()); err != nil {
			os.Exit(92)
		}
	case "namespace":
		if _, err := fmt.Fprint(os.Stdout, helperNamespaceResult()); err != nil {
			os.Exit(92)
		}
	case "linux-security":
		if _, err := fmt.Fprint(os.Stdout, helperLinuxSecurityResult()); err != nil {
			os.Exit(92)
		}
	case "controller-signal":
		if _, err := fmt.Fprint(os.Stdout, helperControllerSignalResult()); err != nil {
			os.Exit(92)
		}
	case "windows-security":
		if _, err := fmt.Fprint(os.Stdout, helperWindowsSecurityResult(os.Args[separator+2])); err != nil {
			os.Exit(92)
		}
	case "windows-handle":
		if _, err := fmt.Fprint(os.Stdout, helperWindowsHandleResult(os.Args[separator+2])); err != nil {
			os.Exit(92)
		}
	case "dial-unix":
		connection, err := net.DialTimeout("unix", os.Args[separator+2], 200*time.Millisecond)
		if connection != nil {
			_ = connection.Close()
		}
		if _, writeErr := fmt.Fprint(os.Stdout, map[bool]string{true: "denied", false: "escaped"}[err != nil]); writeErr != nil {
			os.Exit(92)
		}
	case "fail":
		os.Exit(23)
	case "sleep":
		time.Sleep(time.Minute)
	case "spawn-background":
		child := exec.Command(os.Args[0], "-test.run=TestDirectHelperProcess", "--", "delayed-marker", os.Args[separator+2])
		if err := child.Start(); err != nil {
			os.Exit(94)
		}
	case "spawn-setsid-background":
		marker := filepath.Join(os.Getenv("ALMAGEST_SANDBOX_TEMP"), "escaped-descendant")
		executable := os.Args[0]
		if approved := os.Getenv("ALMAGEST_SANDBOX_PROGRAM_SELF"); approved != "" {
			executable = approved
		}
		child := exec.Command(executable, "-test.run=TestDirectHelperProcess", "--", "delayed-marker", marker)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stderr, os.Stderr
		detachHelperProcess(child)
		if err := child.Start(); err != nil {
			_, _ = fmt.Fprint(os.Stdout, err)
			os.Exit(94)
		}
	case "delayed-marker":
		time.Sleep(time.Second)
		if err := os.WriteFile(os.Args[separator+2], []byte("escaped"), 0o600); err != nil {
			os.Exit(95)
		}
	case "outside":
		if _, err := os.ReadFile("/etc/passwd"); err != nil {
			if _, writeErr := fmt.Fprint(os.Stdout, "denied"); writeErr != nil {
				os.Exit(92)
			}
		} else if _, err := fmt.Fprint(os.Stdout, "escaped"); err != nil {
			os.Exit(92)
		}
	case "read":
		data, err := os.ReadFile(os.Args[separator+2])
		if err != nil {
			os.Exit(93)
		}
		if _, err = os.Stdout.Write(data); err != nil {
			os.Exit(92)
		}
	case "write":
		if err := os.WriteFile(os.Args[separator+2], []byte("changed"), 0o600); err != nil {
			if _, writeErr := fmt.Fprint(os.Stdout, "denied"); writeErr != nil {
				os.Exit(92)
			}
		} else if _, err := fmt.Fprint(os.Stdout, "escaped"); err != nil {
			os.Exit(92)
		}
	case "write-temp":
		name := filepath.Join(os.Getenv("ALMAGEST_SANDBOX_TEMP"), "native-write")
		if err := os.WriteFile(name, []byte(os.Args[separator+2]), 0o600); err != nil {
			if _, writeErr := fmt.Fprint(os.Stdout, err); writeErr != nil {
				os.Exit(92)
			}
			os.Exit(93)
		}
		if _, err := fmt.Fprint(os.Stdout, "written"); err != nil {
			os.Exit(92)
		}
	case "write-state":
		name := filepath.Join(os.Getenv("ALMAGEST_SANDBOX_STATE"), "native-state")
		if err := os.WriteFile(name, []byte(os.Args[separator+2]), 0o600); err != nil {
			os.Exit(93)
		}
		_, _ = fmt.Fprint(os.Stdout, "written")
	case "exec-temp":
		source := os.Getenv("ALMAGEST_SANDBOX_PROGRAM_SELF")
		target := filepath.Join(os.Getenv("ALMAGEST_SANDBOX_TEMP"), "copied-program")
		data, err := os.ReadFile(source)
		if err == nil {
			err = os.WriteFile(target, data, 0o700)
		}
		if err == nil {
			child := exec.Command(target, "-test.run=TestDirectHelperProcess", "--", "echo", "escaped")
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			err = child.Run()
		}
		if _, writeErr := fmt.Fprint(os.Stdout, map[bool]string{true: "denied", false: "escaped"}[err != nil]); writeErr != nil {
			os.Exit(92)
		}
	case "exec-path":
		child := exec.Command(os.Args[separator+2], "-test.run=TestDirectHelperProcess", "--", "echo", "escaped")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := child.Run()
		if _, writeErr := fmt.Fprint(os.Stdout, map[bool]string{true: "denied", false: "escaped"}[err != nil]); writeErr != nil {
			os.Exit(92)
		}
	case "dev-null":
		file, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
		if err == nil {
			_, err = file.Write([]byte("discard"))
			_ = file.Close()
		}
		if err != nil {
			os.Exit(93)
		}
		_, _ = fmt.Fprint(os.Stdout, "usable")
	default:
		os.Exit(91)
	}
	os.Exit(0)
}

func testBackend(t *testing.T, mode sandbox.Mode) *Backend {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := policy.Config{Mode: mode, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"), Programs: map[sandbox.ProgramID]policy.Program{
		"helper": {Executable: executable, Builder: "fixed-v1", Arguments: []policy.Argument{{Literal: "-test.run=TestDirectHelperProcess"}, {Literal: "--"}, {Input: "action"}, {Input: "value"}}, Inputs: map[string]policy.Input{"action": {Kind: sandbox.TextValue, Required: true, Maximum: 32}, "value": {Kind: sandbox.TextValue, Required: true, Maximum: 1024}}},
	}, Templates: map[sandbox.TemplateID]policy.Template{"test": {AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper"}, RuntimeProfile: "direct-v1", PersistentRoot: filepath.Join(root, "persistent"), Environment: policy.Environment{Set: map[string]string{"ALMAGEST_DIRECT_HELPER": "1"}}, Limits: policy.TemplateLimits{Commands: 2, Files: 4, Sockets: 8, TransferBytes: 128, TemporaryBytes: 64, OutputBytes: 512}}}}
	probe := policy.Probe{Platform: runtime.GOOS, EffectiveIdentity: "test-user", Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true}, Reasons: map[sandbox.Feature]string{sandbox.WritableStorage: "direct-child-storage-not-confined"}}
	backend, err := NewBackend(config, probe)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestAuthorityPathsRejectSymlinkAncestors(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	for _, name := range []string{alias, filepath.Join(alias, "nested"), filepath.Join(alias, "new", "root")} {
		if err := validatePathComponents(name); !errors.Is(err, sandbox.ErrDenied) {
			t.Fatalf("path %q error = %v", name, err)
		}
	}
}

func prepareInstance(t *testing.T, backend *Backend) sandbox.Sandbox {
	t.Helper()
	value, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "key-1", Epoch: 1, Class: "test/Node", Name: "Test", NameRevision: 1}, "main", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := value.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return value
}

func launch(action, value string) sandbox.Launch {
	return sandbox.Launch{Directory: sandbox.Path{Root: sandbox.Temporary}, Inputs: map[string]sandbox.Value{"action": {Kind: sandbox.TextValue, Text: action}, "value": {Kind: sandbox.TextValue, Text: value}}}
}

func TestNativeFullNewCommandRendersPathInputWithoutLockRecursion(t *testing.T) {
	temporary := t.TempDir()
	instance := &instance{
		backend: &Backend{policy: policy.Config{Programs: map[sandbox.ProgramID]policy.Program{
			"helper": {
				Executable: "/bin/false",
				Arguments:  []policy.Argument{{Input: "config"}},
				Inputs: map[string]policy.Input{
					"config": {Kind: sandbox.PathValue, Required: true},
				},
			},
		}}},
		template:      policy.Template{Programs: []sandbox.ProgramID{"helper"}, Limits: policy.TemplateLimits{Commands: 1}},
		nativeFull:    true,
		status:        sandbox.Status{State: sandbox.Ready},
		temporaryPath: temporary,
		roots:         map[sandbox.Root]*confinedRoot{sandbox.Temporary: {}},
		commands:      make(map[*command]struct{}),
		pending:       make(map[*command]struct{}),
	}
	result := make(chan error, 1)
	go func() {
		value, err := instance.NewCommand("helper", sandbox.Launch{
			Directory: sandbox.Path{Root: sandbox.Temporary},
			Inputs: map[string]sandbox.Value{
				"config": {Kind: sandbox.PathValue, Path: sandbox.Path{Root: sandbox.Temporary, Rel: "torrc"}},
			},
		})
		if err == nil {
			command := value.(*command)
			if !slices.Equal(command.arguments, []string{"/almagest/temp/torrc"}) {
				err = fmt.Errorf("arguments = %q", command.arguments)
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("NewCommand deadlocked while rendering a native path input")
	}
}

func TestDirectCommandContract(t *testing.T) {
	instance := prepareInstance(t, testBackend(t, sandbox.Off))
	process := instance.ProcessContext()
	if process.Platform != runtime.GOOS || process.ControllerPID <= 0 {
		t.Fatalf("process context = %+v", process)
	}
	overLimit, err := instance.NewCommand("helper", launch("echo", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sandbox.Output(context.Background(), overLimit, 513); !errors.Is(err, sandbox.ErrQuota) {
		t.Fatalf("output policy limit: %v", err)
	}
	command, err := instance.NewCommand("helper", launch("echo", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(context.Background(), command, 128)
	if err != nil || string(output) != "hello" {
		t.Fatalf("output=%q error=%v", output, err)
	}
	exit, err := command.Wait()
	if err != nil || exit.Code != 0 {
		t.Fatalf("repeat Wait: %+v %v", exit, err)
	}
	if err = command.Start(context.Background()); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("second Start: %v", err)
	}

	command, err = instance.NewCommand("helper", launch("fail", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	exit, err = sandbox.Run(context.Background(), command)
	var exitError *sandbox.ExitError
	if !errors.As(err, &exitError) || exit.Code != 23 || exitError.Exit.Code != 23 {
		t.Fatalf("exit=%+v error=%v", exit, err)
	}
}

func TestDirectCommandEnvironmentAndCancellation(t *testing.T) {
	t.Setenv("UNLISTED_SANDBOX_TEST", "secret")
	instance := prepareInstance(t, testBackend(t, sandbox.Auto))
	command, err := instance.NewCommand("helper", launch("environment", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(context.Background(), command, 512)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(output), "|")
	if len(parts) != 3 || parts[0] == "" || parts[0] != parts[1] || parts[2] != "" {
		t.Fatalf("environment=%q", output)
	}

	ctx, cancel := context.WithCancel(context.Background())
	command, err = instance.NewCommand("helper", launch("sleep", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-command.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("canceled process did not stop")
	}
	exit, err := command.Wait()
	if err == nil || !exit.Killed {
		t.Fatalf("exit=%+v error=%v", exit, err)
	}
}

func TestDirectCloseKillsRunningCommandsCleanly(t *testing.T) {
	backend := testBackend(t, sandbox.Off)
	value, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "key-close", Epoch: 1, Class: "test/Node", Name: "Test", NameRevision: 1}, "main", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	command, err := value.NewCommand("helper", launch("sleep", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = value.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	exit, err := command.Wait()
	var exitErr *sandbox.ExitError
	if !errors.As(err, &exitErr) || !exit.Killed {
		t.Fatalf("exit=%+v error=%v", exit, err)
	}
}

func TestDirectCloseRevokesCommandBeforeStart(t *testing.T) {
	backend := testBackend(t, sandbox.Off)
	value, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "key-pending", Epoch: 1, Class: "test/Node", Name: "Test", NameRevision: 1}, "main", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	command, err := value.NewCommand("helper", launch("echo", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = value.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("pending command pipe survived sandbox close")
	}
	if err = command.Start(context.Background()); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("pending command start after close = %v", err)
	}
}

func TestDirectOffInheritsNormalEnvironment(t *testing.T) {
	t.Setenv("UNLISTED_SANDBOX_TEST", "visible-off")
	t.Setenv("ALMAGEST_SANDBOX_TEMP", "host-temp")
	t.Setenv("HOME", "host-home")
	instance := prepareInstance(t, testBackend(t, sandbox.Off))
	command, err := instance.NewCommand("helper", launch("environment", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(context.Background(), command, 512)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(output), "|")
	if len(parts) != 3 || parts[0] != "host-temp" || parts[1] != "host-home" || parts[2] != "visible-off" {
		t.Fatalf("environment=%q", output)
	}
}

func TestDirectGlobalCommandLimit(t *testing.T) {
	backend := testBackend(t, sandbox.Off)
	backend.policy.Limits.Commands = 1
	first := prepareInstance(t, backend)
	second, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "key-2", Epoch: 1, Class: "test/Node", Name: "Second", NameRevision: 1}, "main", "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	running, err := first.NewCommand("helper", launch("sleep", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	if err = running.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	blocked, err := second.NewCommand("helper", launch("echo", "blocked"))
	if err != nil {
		t.Fatal(err)
	}
	if err = blocked.Start(context.Background()); !errors.Is(err, sandbox.ErrQuota) {
		t.Fatalf("second command error = %v", err)
	}
	if err = running.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err = running.Wait(); err == nil {
		t.Fatal("killed command returned no error")
	}

	allowed, err := second.NewCommand("helper", launch("echo", "allowed"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(context.Background(), allowed, 128)
	if err != nil || string(output) != "allowed" {
		t.Fatalf("output=%q error=%v", output, err)
	}
}

func TestDirectFilesEnforceBoundsAndCloseStreams(t *testing.T) {
	sand := prepareInstance(t, testBackend(t, sandbox.Off))
	files, err := sand.FS(sandbox.Temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err = files.WriteFile(context.Background(), "a", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := files.ReadFile(context.Background(), "a", 5)
	if err != nil || string(data) != "hello" {
		t.Fatalf("data=%q error=%v", data, err)
	}
	if _, err = files.ReadFile(context.Background(), "a", 4); !errors.Is(err, sandbox.ErrQuota) {
		t.Fatalf("bounded read: %v", err)
	}
	if err = files.WriteFile(context.Background(), "large", make([]byte, 65), 0o600); !errors.Is(err, sandbox.ErrQuota) {
		t.Fatalf("storage quota: %v", err)
	}
	appendFile, err := files.Open(context.Background(), "a", sandbox.OpenOptions{Write: true, Append: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = appendFile.Write(make([]byte, 59)); err != nil {
		t.Fatalf("append to quota: %v", err)
	}
	if _, err = appendFile.Write([]byte("x")); !errors.Is(err, sandbox.ErrQuota) {
		t.Fatalf("append quota: %v", err)
	}
	if err = appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	persistent, err := sand.FS(sandbox.Persistent)
	if err != nil {
		t.Fatal(err)
	}
	if err = persistent.WriteFile(context.Background(), "larger-than-temporary-limit", make([]byte, 65), 0o600); err != nil {
		t.Fatalf("persistent write was treated as temporary quota use: %v", err)
	}
	file, err := files.Open(context.Background(), "stream", sandbox.OpenOptions{Write: true, Create: true, Mode: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	if err = sand.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("x")); err == nil {
		t.Fatal("open file survived sandbox close")
	}
}

func TestDirectWaitTerminatesDescendants(t *testing.T) {
	sand := prepareInstance(t, testBackend(t, sandbox.Off))
	marker := filepath.Join(sand.(*instance).temporaryPath, "descendant-marker")
	command, err := sand.NewCommand("helper", launch("spawn-background", marker))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sandbox.Run(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err = os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived command completion: %v", err)
	}
}

func TestRootUsageUsesVerifiedOpenHandleSize(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "open"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "other"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	usage, entries, err := rootUsageWithSize(root, 16, "open", 64)
	if err != nil || usage != 69 || entries != 2 {
		t.Fatalf("usage=%d entries=%d error=%v", usage, entries, err)
	}
}

func TestDirectRejectsSymlinkAndHardLink(t *testing.T) {
	sand := prepareInstance(t, testBackend(t, sandbox.Off))
	files, err := sand.FS(sandbox.Temporary)
	if err != nil {
		t.Fatal(err)
	}
	host := sand.(*instance).temporaryPath
	if err = os.WriteFile(filepath.Join(host, "target"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("symlink", func(t *testing.T) {
		if linkErr := os.Symlink("target", filepath.Join(host, "link")); linkErr != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("creating a test symlink requires Windows developer mode or elevated authority: %v", linkErr)
			}
			t.Fatal(linkErr)
		}
		if _, openErr := files.Open(context.Background(), "link", sandbox.OpenOptions{Read: true}); !errors.Is(openErr, sandbox.ErrDenied) {
			t.Fatalf("symlink: %v", openErr)
		}
	})
	t.Run("hard link", func(t *testing.T) {
		if linkErr := os.Link(filepath.Join(host, "target"), filepath.Join(host, "alias")); linkErr != nil {
			t.Fatal(linkErr)
		}
		if _, openErr := files.Open(context.Background(), "alias", sandbox.OpenOptions{Read: true}); !errors.Is(openErr, sandbox.ErrDenied) {
			t.Fatalf("hard link: %v", openErr)
		}
	})
}

func TestDirectCleanupRejectsReplacedTemporaryDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents renaming the held directory")
	}
	backend := testBackend(t, sandbox.Off)
	value, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "key-cleanup", Epoch: 1, Class: "test/Node", Name: "Test", NameRevision: 1}, "main", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	instance := value.(*instance)
	moved := instance.temporaryPath + "-moved"
	if err = os.Rename(instance.temporaryPath, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(instance.temporaryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = instance.Close(); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("cleanup error = %v", err)
	}
	if _, err = os.Stat(instance.temporaryPath); err != nil {
		t.Fatalf("replacement directory was removed: %v", err)
	}
	_ = os.RemoveAll(instance.temporaryPath)
	_ = os.RemoveAll(moved)
}

func TestDirectRejectsReplacedPersistentDirectory(t *testing.T) {
	backend := testBackend(t, sandbox.Off)
	owner := sandbox.Owner{Workspace: "main", Key: "key-persistent-alias", Epoch: 1, Class: "test/Node", Name: "Test", NameRevision: 1}
	value, err := backend.Prepare(context.Background(), owner, "main", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	instance := value.(*instance)
	persistentPath := instance.persistentPath
	if err = instance.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(persistentPath); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err = os.Symlink(external, persistentPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a test symlink requires Windows developer mode or elevated authority: %v", err)
		}
		t.Fatal(err)
	}
	if _, err = backend.Prepare(context.Background(), owner, "main", "test", 2); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("persistent alias error = %v", err)
	}
}

func TestDirectLoopbackSockets(t *testing.T) {
	instance := prepareInstance(t, testBackend(t, sandbox.Off))
	listener, err := instance.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			defer func() { _ = conn.Close() }()
			_, e = io.Copy(conn, conn)
		}
		accepted <- e
	}()
	conn, err := instance.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err = io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if string(response) != "ping" {
		t.Fatal(string(response))
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-accepted; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, err = instance.DialContext(context.Background(), "tcp", "8.8.8.8:53"); !errors.Is(err, sandbox.ErrInvalid) {
		t.Fatalf("external dial: %v", err)
	}
	packet, err := instance.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = packet.WriteTo([]byte("udp"), packet.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 3)
	if _, _, err = packet.ReadFrom(data); err != nil || string(data) != "udp" {
		t.Fatalf("UDP loopback = %q, %v", data, err)
	}
	if _, err = packet.WriteTo([]byte("blocked"), &net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 53}); !errors.Is(err, sandbox.ErrInvalid) {
		t.Fatalf("external UDP send: %v", err)
	}
	if err = packet.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectUnixSockets(t *testing.T) {
	instance := prepareInstance(t, testBackend(t, sandbox.Off))
	files, err := instance.FS(sandbox.Temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err = files.MkdirAll(context.Background(), "sockets", 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := sandbox.Path{Root: sandbox.Temporary, Rel: "sockets/service.sock"}
	listener, err := instance.ListenUnix(context.Background(), socketPath)
	if errors.Is(err, sandbox.ErrUnsupported) {
		t.Skip("platform does not support Unix sockets")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer func() { _ = conn.Close() }()
			_, acceptErr = io.Copy(conn, conn)
		}
		accepted <- acceptErr
	}()
	conn, err := instance.DialUnix(context.Background(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte("unix")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err = io.ReadFull(conn, response); err != nil || string(response) != "unix" {
		t.Fatalf("response=%q error=%v", response, err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-accepted; err != nil {
		t.Fatal(err)
	}
	if _, err = files.Stat(context.Background(), socketPath.Rel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket path remained after close: %v", err)
	}
}

func TestDirectAutoReportsOnlyQualifiedProtections(t *testing.T) {
	status := prepareInstance(t, testBackend(t, sandbox.Auto)).Status()
	if status.StrictSatisfied || status.FullBoundary {
		t.Fatalf("partial backend overstated status: %+v", status)
	}
	for _, item := range status.Features {
		if item.State == sandbox.Applied && item.Feature != sandbox.Environment && item.Feature != sandbox.HandleInheritance {
			t.Fatalf("unqualified feature applied: %+v", item)
		}
		if item.Feature == sandbox.WritableStorage && (item.State != sandbox.OmittedAuto || item.Reason != "direct-child-storage-not-confined") {
			t.Fatalf("storage confinement status: %+v", item)
		}
	}
}

func TestDirectStrictRefusesBeforeLaunch(t *testing.T) {
	config := testBackend(t, sandbox.Off).policy
	config.Mode = sandbox.Strict
	if _, err := NewBackend(config, policy.Probe{Platform: runtime.GOOS, EffectiveIdentity: "test-user", Available: map[sandbox.Feature]bool{sandbox.Environment: true}}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("strict preflight error = %v", err)
	}
}

func TestDirectOffAcceptsMappedReadOnlyPolicy(t *testing.T) {
	config := testBackend(t, sandbox.Off).policy
	source := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(source, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	template := config.Templates["test"]
	template.ReadOnly = []policy.ReadOnly{{Source: source, Target: "/mapped/input"}}
	config.Templates["test"] = template
	probe := policy.Probe{
		Platform: runtime.GOOS, EffectiveIdentity: "test-user",
		Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true},
		Reasons:   map[sandbox.Feature]string{sandbox.WritableStorage: "direct-child-storage-not-confined"},
	}
	if _, err := NewBackend(config, probe); err != nil {
		t.Fatalf("off mode rejected inactive read-only mapping: %v", err)
	}
}

func TestDirectBackendReopensNativeTraversalRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX root permissions are not available")
	}
	backend := testBackend(t, sandbox.Off)
	config := backend.policy
	if err := os.Chmod(config.TemporaryRoot, 0o711); err != nil {
		t.Fatal(err)
	}
	probe := policy.Probe{
		Platform: runtime.GOOS, EffectiveIdentity: "test-user",
		Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true},
		Reasons:   map[sandbox.Feature]string{sandbox.WritableStorage: "direct-child-storage-not-confined"},
	}
	if _, err := NewBackend(config, probe); err != nil {
		t.Fatalf("backend restart rejected native traversal root: %v", err)
	}
}
