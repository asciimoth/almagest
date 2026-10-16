//go:build windows

package direct

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/asciimoth/almagest/lib/sandbox"
	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func helperWindowsSecurityResult(foreign string) string {
	var failures []string
	token := windows.GetCurrentProcessToken()
	if token.IsElevated() {
		failures = append(failures, "elevated")
	}
	if restricted, err := token.IsRestricted(); err != nil || !restricted {
		failures = append(failures, "unrestricted")
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		failures = append(failures, "groups")
	} else {
		for _, group := range groups.AllGroups() {
			if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
				failures = append(failures, "administrator")
			}
		}
	}
	data, err := boundedTokenInformation(token, windows.TokenIntegrityLevel)
	if err != nil {
		failures = append(failures, "integrity-query")
	} else {
		label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&data[0]))
		count := label.Label.Sid.SubAuthorityCount()
		if count == 0 || label.Label.Sid.SubAuthority(uint32(count-1)) != 0x1000 {
			failures = append(failures, "integrity")
		}
	}
	var inJob uint32
	result, _, jobErr := isProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
	if result == 0 || inJob == 0 || jobErr != windows.ERROR_SUCCESS {
		failures = append(failures, "job")
	}
	if parent, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_VM_WRITE, false, uint32(os.Getppid())); openErr == nil {
		_ = windows.CloseHandle(parent)
		failures = append(failures, "parent-control")
	}
	if writeErr := os.WriteFile(filepath.Join(os.Getenv("ALMAGEST_SANDBOX_TEMP"), "child-write"), []byte("ok"), 0o600); writeErr != nil {
		failures = append(failures, "own-storage")
	}
	if writeErr := os.WriteFile(filepath.Join(foreign, "escaped"), []byte("bad"), 0o600); writeErr == nil {
		failures = append(failures, "foreign-storage")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestDirectHelperProcess$", "--", "echo", "breakaway")
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
	if startErr := child.Start(); startErr == nil {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
		failures = append(failures, "job-breakaway")
	}
	if len(failures) == 0 {
		return "secure"
	}
	return strings.Join(failures, ",")
}

func helperWindowsHandleResult(value string) string {
	handle, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return "invalid"
	}
	file := os.NewFile(uintptr(handle), "unapproved-inherited-handle")
	if file == nil {
		return "denied"
	}
	defer func() { _ = file.Close() }()
	data := make([]byte, 32)
	count, err := file.Read(data)
	if err == nil && string(data[:count]) == "sandbox-handle-secret" {
		return "escaped"
	}
	return "denied"
}

func windowsNativeBackend(t *testing.T, mode sandbox.Mode, root string) *Backend {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := policy.Config{
		Mode: mode, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"),
		Programs: map[sandbox.ProgramID]policy.Program{"helper": {
			Executable: executable, Builder: "fixed-v1",
			Arguments: []policy.Argument{{Literal: "-test.run=^TestDirectHelperProcess$"}, {Literal: "--"}, {Input: "action"}, {Input: "value"}},
			Inputs:    map[string]policy.Input{"action": {Kind: sandbox.TextValue, Required: true, Maximum: 32}, "value": {Kind: sandbox.TextValue, Required: true, Maximum: 1024}},
		}},
		Templates: map[sandbox.TemplateID]policy.Template{"test": {
			AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper"}, RuntimeProfile: "windows-native-v1",
			PersistentRoot: filepath.Join(root, "persistent"), Limits: policy.TemplateLimits{Commands: 2, Tasks: 4, Files: 32,
				Sockets: 32, MemoryBytes: 256 << 20, CPUPercent: 100, TransferBytes: 1 << 20, TemporaryBytes: 32 << 20, OutputBytes: 1 << 20,
				Required: []string{"Tasks", "MemoryBytes", "CPUPercent"}},
		}},
	}
	backend, err := NewBackend(config, policy.Probe{Platform: "windows", EffectiveIdentity: "test-user",
		Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true}})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestWindowsNativeStrictBoundary(t *testing.T) {
	if windows.RtlGetVersion().BuildNumber != qualifiedWindowsBuild {
		t.Skipf("windows-native-v1 requires qualified build %d", qualifiedWindowsBuild)
	}
	backend := windowsNativeBackend(t, sandbox.Strict, t.TempDir())
	first := prepareInstance(t, backend)
	status := first.Status()
	if !status.StrictSatisfied || status.FullBoundary || status.Profile != "windows-native-v1" {
		t.Fatalf("status = %+v", status)
	}
	for _, feature := range status.Features {
		excluded := feature.Feature == sandbox.FilesystemAccess || feature.Feature == sandbox.ExactFilesystemView || feature.Feature == sandbox.PrivateNetwork
		if excluded && feature.State != sandbox.ExcludedOS || !excluded && feature.State != sandbox.Applied {
			t.Fatalf("feature = %+v", feature)
		}
	}
	native := first.(*instance).windowsBoundary
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(native.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		t.Fatal(err)
	}
	wantFlags := uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
		windows.JOB_OBJECT_LIMIT_JOB_MEMORY | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION)
	if limits.BasicLimitInformation.LimitFlags&wantFlags != wantFlags || limits.BasicLimitInformation.ActiveProcessLimit != 4 || limits.JobMemoryLimit != 256<<20 {
		t.Fatalf("job limits = %+v", limits)
	}
	var cpu struct{ Flags, Rate uint32 }
	if err := windows.QueryInformationJobObject(native.job, windows.JobObjectCpuRateControlInformation,
		uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)), nil); err != nil || cpu.Flags != 1|4 || cpu.Rate != 10000 {
		t.Fatalf("CPU limit = %+v, %v", cpu, err)
	}

	secondValue, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "key-2", Epoch: 1,
		Class: "test/Node", Name: "Other", NameRevision: 1}, "main", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	second := secondValue.(*instance)
	t.Cleanup(func() {
		if closeErr := second.Close(); closeErr != nil {
			t.Errorf("close second: %v", closeErr)
		}
	})
	command, err := first.NewCommand("helper", launch("windows-security", second.temporaryPath))
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(context.Background(), command, 1024)
	if err != nil || string(output) != "secure" {
		t.Fatalf("security result = %q, %v", output, err)
	}
	if _, err = os.Stat(filepath.Join(second.temporaryPath, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign marker: %v", err)
	}
	secret, err := os.CreateTemp(t.TempDir(), "inheritable-secret-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secret.Close() })
	if _, err = secret.WriteString("sandbox-handle-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err = secret.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	handle := windows.Handle(secret.Fd())
	if err = windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		t.Fatal(err)
	}
	command, err = first.NewCommand("helper", launch("windows-handle", strconv.FormatUint(uint64(handle), 10)))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), command, 1024)
	if err != nil || string(output) != "denied" {
		t.Fatalf("handle inheritance result = %q, %v", output, err)
	}
}

func TestWindowsNativeRejectsUnenforceableRequiredLimits(t *testing.T) {
	for _, name := range []string{"Files", "TemporaryBytes"} {
		t.Run(name, func(t *testing.T) {
			template := policy.Template{Limits: policy.TemplateLimits{Required: []string{name}}}
			if err := windowsNativePrerequisites(template); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidatePathComponentsRejectsWindowsJunction(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	junction := filepath.Join(root, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	if err = validatePathComponents(filepath.Join(junction, "child")); !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("error = %v", err)
	}
}

func TestWindowsNativeIdentityIsStableAndOwnerScoped(t *testing.T) {
	owner := sandbox.Owner{Workspace: "main", Key: "owner-key"}
	first, err := sandboxSID(owner, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sandboxSID(owner, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := sandboxSID(sandbox.Owner{Workspace: "main", Key: "other"}, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	otherSlot, err := sandboxSID(owner, "other-slot", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equals(second) || first.Equals(foreign) || first.Equals(otherSlot) || !strings.HasPrefix(first.String(), "S-1-5-21-") {
		t.Fatalf("SIDs: %s %s %s", first, second, foreign)
	}
}

func TestWindowsNativeLocalSystemLaunch(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("LocalSystem qualification is started by the elevated WinVM case")
	}
	if windows.RtlGetVersion().BuildNumber != qualifiedWindowsBuild {
		t.Skipf("windows-native-v1 requires qualified build %d", qualifiedWindowsBuild)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	taskName := fmt.Sprintf("AlmagestSandboxTest%d", os.Getpid())
	result := filepath.Join(os.Getenv("ProgramData"), taskName+".txt")
	_ = os.Remove(result)
	t.Cleanup(func() {
		_ = exec.Command("schtasks.exe", "/Delete", "/TN", taskName, "/F").Run()
		_ = os.Remove(result)
	})
	commandLine := fmt.Sprintf(`cmd.exe /d /s /c ""%s" -test.run=^TestWindowsNativeStrictBoundary$ -test.v >"%s" 2>&1"`, executable, result)
	output, err := exec.Command("schtasks.exe", "/Create", "/TN", taskName, "/SC", "ONCE", "/ST", "00:00",
		"/RU", "SYSTEM", "/RL", "HIGHEST", "/TR", commandLine, "/F").CombinedOutput()
	if err != nil {
		t.Fatalf("create LocalSystem qualification task: %v: %s", err, output)
	}
	output, err = exec.Command("schtasks.exe", "/Run", "/TN", taskName).CombinedOutput()
	if err != nil {
		t.Fatalf("start LocalSystem qualification task: %v: %s", err, output)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(result)
		if readErr == nil && strings.Contains(string(data), "--- PASS: TestWindowsNativeStrictBoundary") {
			return
		}
		if readErr == nil && strings.Contains(string(data), "--- FAIL:") {
			t.Fatalf("LocalSystem qualification failed:\n%s", data)
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(result)
	t.Fatalf("LocalSystem qualification did not complete:\n%s", data)
}
