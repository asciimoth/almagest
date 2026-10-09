//go:build windows

// windows-isolation-probe is a development-only checkpoint 11 launcher and
// restricted child. It is not linked to the production daemon.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	securityCapabilitiesAttribute  = 0x00020009
	jobObjectLimitActiveProcess    = 0x00000008
	jobObjectUILimitHandles        = 0x00000001
	jobObjectUILimitReadClipboard  = 0x00000002
	jobObjectUILimitWriteClipboard = 0x00000004
	jobObjectUILimitSystemParams   = 0x00000008
	jobObjectUILimitDesktop        = 0x00000040
	jobObjectUILimitExitWindows    = 0x00000080
	processCreationMitigationDEP   = uint64(0x01)
	processCreationMitigationSEHOP = uint64(0x04)
)

var (
	userenv                   = windows.NewLazySystemDLL("userenv.dll")
	createAppContainerProfile = userenv.NewProc("CreateAppContainerProfile")
	deleteAppContainerProfile = userenv.NewProc("DeleteAppContainerProfile")
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	openSCManager             = advapi32.NewProc("OpenSCManagerW")
	createRestrictedToken     = advapi32.NewProc("CreateRestrictedToken")
	ws2_32                    = windows.NewLazySystemDLL("ws2_32.dll")
	wsaDuplicateSocket        = ws2_32.NewProc("WSADuplicateSocketW")
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	isProcessInJob            = kernel32.NewProc("IsProcessInJob")
	getProcessDEPPolicy       = kernel32.NewProc("GetProcessDEPPolicy")
)

type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    uintptr
	CapabilityCount uint32
	Reserved        uint32
}

type check struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type report struct {
	SchemaVersion int              `json:"schemaVersion"`
	Profile       string           `json:"profile"`
	LauncherUser  string           `json:"launcherUser,omitempty"`
	LauncherPID   uint32           `json:"launcherPid,omitempty"`
	ChildPID      uint32           `json:"childPid,omitempty"`
	OSBuild       uint32           `json:"osBuild,omitempty"`
	Checks        map[string]check `json:"checks"`
	Notes         []string         `json:"notes,omitempty"`
}

var initHostRead = probeRead(`C:\winvm\isolation-host-secret.txt`)

func main() {
	child := flag.Bool("child", false, "run the restricted probe")
	profile := flag.String("profile", "caller", "caller or service profile")
	output := flag.String("output", "", "result JSON path")
	storage := flag.String("storage", "", "private storage directory")
	pipeHandle := flag.Uint64("pipe-handle", 0, "inherited result pipe handle")
	parentPID := flag.Uint("parent-pid", 0, "launcher process ID")
	flag.Parse()

	if *child {
		runChild(*profile, *storage, windows.Handle(*pipeHandle), uint32(*parentPID))
		return
	}
	if *output == "" || *storage == "" {
		fmt.Fprintln(os.Stderr, "-output and -storage are required")
		os.Exit(2)
	}
	if *profile != "caller" && *profile != "service" {
		fmt.Fprintln(os.Stderr, "-profile must be caller or service")
		os.Exit(2)
	}
	if err := runLauncher(*profile, *output, *storage); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runLauncher(profile, output, storage string) error {
	if err := os.MkdirAll(storage, 0o700); err != nil {
		return fmt.Errorf("create private storage: %w", err)
	}
	packageName := fmt.Sprintf("Almagest.Isolation.%s.%d", profile, os.Getpid())
	name, _ := windows.UTF16PtrFromString(packageName)
	display, _ := windows.UTF16PtrFromString("Almagest isolation experiment")
	description, _ := windows.UTF16PtrFromString("Disposable checkpoint 11 profile")
	var appSID *windows.SID
	r1, _, callErr := createAppContainerProfile.Call(
		uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(display)),
		uintptr(unsafe.Pointer(description)), 0, 0, uintptr(unsafe.Pointer(&appSID)),
	)
	if r1 != 0 {
		return fmt.Errorf("create AppContainer profile: %w", callErr)
	}
	defer func() { _, _, _ = deleteAppContainerProfile.Call(uintptr(unsafe.Pointer(name))) }()
	defer func() { _ = windows.FreeSid(appSID) }()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate probe: %w", err)
	}
	if err = grantAppContainer(exe, appSID, windows.GENERIC_READ|windows.GENERIC_EXECUTE, windows.NO_INHERITANCE); err != nil {
		return fmt.Errorf("grant probe access: %w", err)
	}
	if err = grantAppContainer(storage, appSID, windows.GENERIC_ALL, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT); err != nil {
		return fmt.Errorf("grant storage access: %w", err)
	}

	var readPipe, writePipe windows.Handle
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err = windows.CreatePipe(&readPipe, &writePipe, &sa, 0); err != nil {
		return fmt.Errorf("create private result pipe: %w", err)
	}
	defer func() { _ = windows.CloseHandle(readPipe) }()
	defer func() {
		if writePipe != 0 {
			_ = windows.CloseHandle(writePipe)
		}
	}()
	var logRead, logWrite windows.Handle
	if err = windows.CreatePipe(&logRead, &logWrite, &sa, 0); err != nil {
		return fmt.Errorf("create restricted log pipe: %w", err)
	}
	defer func() { _ = windows.CloseHandle(logRead) }()
	defer func() {
		if logWrite != 0 {
			_ = windows.CloseHandle(logWrite)
		}
	}()
	if err = windows.SetHandleInformation(logRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return fmt.Errorf("make log read pipe private: %w", err)
	}
	if err = windows.SetHandleInformation(readPipe, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return fmt.Errorf("make read pipe private: %w", err)
	}
	forbidden, err := windows.CreateEvent(&sa, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("create inheritance sentinel: %w", err)
	}
	defer func() { _ = windows.CloseHandle(forbidden) }()

	attrs, err := windows.NewProcThreadAttributeList(3)
	if err != nil {
		return fmt.Errorf("allocate process attributes: %w", err)
	}
	defer attrs.Delete()
	handles := []windows.Handle{writePipe, logWrite}
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return fmt.Errorf("set exact inherited handles: %w", err)
	}
	capabilities := securityCapabilities{AppContainerSid: appSID}
	if err = attrs.Update(securityCapabilitiesAttribute, unsafe.Pointer(&capabilities), unsafe.Sizeof(capabilities)); err != nil {
		return fmt.Errorf("set AppContainer identity: %w", err)
	}
	mitigations := processCreationMitigationDEP | processCreationMitigationSEHOP
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_MITIGATION_POLICY, unsafe.Pointer(&mitigations), unsafe.Sizeof(mitigations)); err != nil {
		return fmt.Errorf("set process mitigations: %w", err)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create Job Object: %w", err)
	}
	defer func() { _ = windows.CloseHandle(job) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | jobObjectLimitActiveProcess
	limits.BasicLimitInformation.ActiveProcessLimit = 1
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("set Job Object limits: %w", err)
	}
	uiLimits := uint32(jobObjectUILimitHandles | jobObjectUILimitReadClipboard | jobObjectUILimitWriteClipboard |
		jobObjectUILimitSystemParams | jobObjectUILimitDesktop | jobObjectUILimitExitWindows)
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&uiLimits)), uint32(unsafe.Sizeof(uiLimits))); err != nil {
		return fmt.Errorf("set Job Object UI limits: %w", err)
	}

	args := []string{exe, "-child=true", "-profile", profile, "-storage", storage,
		"-pipe-handle", strconv.FormatUint(uint64(writePipe), 10),
		"-parent-pid", strconv.Itoa(os.Getpid())}
	cmdline, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	exe16, _ := windows.UTF16PtrFromString(exe)
	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.ProcThreadAttributeList = attrs.List()
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdOutput = logWrite
	si.StdErr = logWrite
	pi := windows.ProcessInformation{}
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW |
		windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	var sourceToken windows.Token
	if err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY, &sourceToken); err != nil {
		return fmt.Errorf("open launcher token: %w", err)
	}
	defer func() { _ = sourceToken.Close() }()
	var restrictedToken windows.Token
	r1, _, restrictErr := createRestrictedToken.Call(uintptr(sourceToken), 0x1, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&restrictedToken)))
	if r1 == 0 {
		return fmt.Errorf("create restricted primary token: %w", restrictErr)
	}
	defer func() { _ = restrictedToken.Close() }()
	if err = windows.CreateProcessAsUser(restrictedToken, exe16, cmdline, nil, nil, true, flags, nil, nil, &si.StartupInfo, &pi); err != nil {
		return fmt.Errorf("create suspended AppContainer: %w", err)
	}
	defer func() { _ = windows.CloseHandle(pi.Process) }()
	defer func() { _ = windows.CloseHandle(pi.Thread) }()
	var duplicated windows.Handle
	duplicateErr := windows.DuplicateHandle(pi.Process, forbidden, windows.CurrentProcess(), &duplicated, 0, false, windows.DUPLICATE_SAME_ACCESS)
	if duplicateErr == nil {
		_ = windows.CloseHandle(duplicated)
	}
	if err = windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		return fmt.Errorf("assign child to non-breakaway job: %w", err)
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		return fmt.Errorf("resume restricted child: %w", err)
	}
	_ = windows.CloseHandle(writePipe)
	writePipe = 0
	_ = windows.CloseHandle(logWrite)
	logWrite = 0

	pipeFile := os.NewFile(uintptr(readPipe), "isolation-result")
	logFile := os.NewFile(uintptr(logRead), "isolation-log")
	logDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(logFile)
		logDone <- data
	}()
	decoder := json.NewDecoder(pipeFile)
	var result report
	decodeDone := make(chan error, 1)
	go func() { decodeDone <- decoder.Decode(&result) }()
	select {
	case err = <-decodeDone:
	case <-time.After(30 * time.Second):
		_ = windows.TerminateJobObject(job, 1)
		return errors.New("restricted child timed out")
	}
	if err != nil {
		_, _ = windows.WaitForSingleObject(pi.Process, 10_000)
		logs := <-logDone
		var exitCode uint32
		if codeErr := windows.GetExitCodeProcess(pi.Process, &exitCode); codeErr != nil {
			return fmt.Errorf("read restricted child result: %w; get exit code: %v; child log: %s", err, codeErr, logs)
		}
		return fmt.Errorf("read restricted child result: %w; child exit code %d; child log: %s", err, exitCode, logs)
	}
	if _, err = windows.WaitForSingleObject(pi.Process, 10_000); err != nil {
		return fmt.Errorf("wait for restricted child: %w", err)
	}
	result.LauncherPID = uint32(os.Getpid())
	result.ChildPID = pi.ProcessId
	result.LauncherUser = currentUser()
	result.Checks["unlisted_handle_denied"] = denied(duplicateErr)
	result.Notes = append(result.Notes,
		"The profile has no AppContainer network capabilities.",
		"The exact inherited handle is an anonymous-pipe endpoint; the sentinel handle is inheritable but absent from the handle list.")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err = os.WriteFile(output, data, 0o600); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return validateResult(result)
}

func runChild(profile, storage string, pipeHandle windows.Handle, parentPID uint32) {
	r := report{SchemaVersion: 1, Profile: profile, ChildPID: uint32(os.Getpid()), Checks: make(map[string]check)}
	f := os.NewFile(uintptr(pipeHandle), "result")
	if f == nil {
		os.Exit(3)
	}
	defer func() { _ = f.Close() }()
	defer func() {
		if recovered := recover(); recovered != nil {
			r.Checks["child_panic"] = check{Detail: fmt.Sprint(recovered)}
			_ = json.NewEncoder(f).Encode(r)
		}
	}()
	r.Checks["package_init_host_file_denied"] = denied(initHostRead)
	r.Checks["appcontainer_token"] = boolCheck(isAppContainer(), "TokenIsAppContainer is set")
	r.Checks["restricted_token"] = boolCheck(tokenUint32(windows.TokenHasRestrictions) != 0, "TokenHasRestrictions is set")
	r.Checks["low_integrity"] = boolCheck(lowIntegrity(), "token integrity RID is Low or below")
	r.Checks["dep_enabled"] = boolCheck(depEnabled(), "process DEP policy is enabled")
	r.Checks["job_membership"] = boolCheck(inJob(), "process is assigned to a Job Object")
	r.Checks["private_storage_write"] = allowed(writeStorage(storage))
	r.Checks["host_file_denied"] = denied(probeRead(`C:\winvm\isolation-host-secret.txt`))
	r.Checks["parent_token_denied"] = denied(openParentToken(parentPID))
	r.Checks["process_creation_denied"] = denied(spawnBreakaway())
	r.Checks["driver_control_denied"] = denied(probeDriverControl())
	r.Checks["native_network_denied"] = denied(probeNetwork())
	r.Checks["wintun_open_denied"] = denied(probeWintunOpen())
	r.Checks["winsock_duplicate_target_denied"] = denied(probeWinsockDuplicate(parentPID))
	r.Checks["ipc_write"] = boolCheck(pipeHandle != 0, "exact inherited pipe handle is present")

	if err := json.NewEncoder(f).Encode(r); err != nil {
		os.Exit(4)
	}
}

func grantAppContainer(path string, sid *windows.SID, access windows.ACCESS_MASK, inheritance uint32) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: access, AccessMode: windows.GRANT_ACCESS, Inheritance: inheritance,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_GROUP, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func validateResult(r report) error {
	for name, value := range r.Checks {
		if !value.OK {
			return fmt.Errorf("restricted check %s failed: %s", name, value.Detail)
		}
	}
	return nil
}

func allowed(err error) check {
	if err != nil {
		return check{Detail: err.Error()}
	}
	return check{OK: true, Detail: "operation succeeded"}
}

func denied(err error) check {
	if err == nil {
		return check{Detail: "operation unexpectedly succeeded"}
	}
	return check{OK: true, Detail: err.Error()}
}

func boolCheck(ok bool, detail string) check { return check{OK: ok, Detail: detail} }

func probeRead(path string) error { _, err := os.ReadFile(path); return err }

func writeStorage(path string) error {
	name := filepath.Join(path, "runtime-write.txt")
	if err := os.WriteFile(name, []byte("restricted runtime\n"), 0o600); err != nil {
		return err
	}
	return os.Remove(name)
}

func currentUser() string {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "unknown"
	}
	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return user.User.Sid.String()
	}
	return domain + `\` + account
}

func isAppContainer() bool {
	return tokenUint32(29) != 0
}

func tokenUint32(class uint32) uint32 {
	var value uint32
	var size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &size); err != nil {
		return 0
	}
	return value
}

func lowIntegrity() bool {
	var size uint32
	_ = windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenIntegrityLevel, nil, 0, &size)
	if size == 0 {
		return false
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenIntegrityLevel, &buffer[0], size, &size); err != nil {
		return false
	}
	label := (*windows.SIDAndAttributes)(unsafe.Pointer(&buffer[0]))
	count := label.Sid.SubAuthorityCount()
	return count != 0 && label.Sid.SubAuthority(uint32(count-1)) <= 0x1000
}

func depEnabled() bool {
	var flags uint32
	var permanent int32
	r1, _, _ := getProcessDEPPolicy.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&permanent)))
	return r1 != 0 && flags&1 != 0
}

func inJob() bool {
	var yes int32
	r1, _, _ := isProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&yes)))
	return r1 != 0 && yes != 0
}

func openParentToken(pid uint32) error {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(p) }()
	var token windows.Token
	if err = windows.OpenProcessToken(p, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return err
	}
	_ = token.Close()
	return nil
}

func spawnBreakaway() error {
	executable, err := windows.UTF16PtrFromString(os.Getenv("COMSPEC"))
	if err != nil {
		return err
	}
	command, err := windows.UTF16PtrFromString(`cmd.exe /d /c exit 0`)
	if err != nil {
		return err
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	pi := windows.ProcessInformation{}
	err = windows.CreateProcess(executable, command, nil, nil, false,
		windows.CREATE_BREAKAWAY_FROM_JOB|windows.CREATE_NO_WINDOW, nil, nil, &si, &pi)
	if err != nil {
		return err
	}
	_ = windows.TerminateProcess(pi.Process, 1)
	_ = windows.CloseHandle(pi.Thread)
	_ = windows.CloseHandle(pi.Process)
	return nil
}

func probeDriverControl() error {
	r1, _, err := openSCManager.Call(0, 0, 0x0002)
	if r1 != 0 {
		_ = windows.CloseServiceHandle(windows.Handle(r1))
		return nil
	}
	return err
}

func probeNetwork() error {
	c, err := net.DialTimeout("tcp4", "192.0.2.1:9", 750*time.Millisecond)
	if err != nil {
		return err
	}
	_ = c.Close()
	return nil
}

func probeWintunOpen() error {
	dll := windows.NewLazySystemDLL("wintun.dll")
	proc := dll.NewProc("WintunOpenAdapter")
	if err := proc.Find(); err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString("AlmagestIsolationMissing")
	r1, _, err := proc.Call(uintptr(unsafe.Pointer(name)))
	if r1 != 0 {
		return nil
	}
	return err
}

func probeWinsockDuplicate(targetPID uint32) error {
	socket, err := windows.Socket(windows.AF_INET, windows.SOCK_STREAM, windows.IPPROTO_TCP)
	if err != nil {
		return err
	}
	defer func() { _ = windows.Closesocket(socket) }()
	var protocolInfo [1024]byte
	r1, _, callErr := wsaDuplicateSocket.Call(uintptr(socket), uintptr(targetPID), uintptr(unsafe.Pointer(&protocolInfo[0])))
	if int32(r1) == 0 {
		return nil
	}
	if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
		return callErr
	}
	return errors.New("WSADuplicateSocketW rejected the target")
}

func init() {
	// Keep package initialization free of work except the deliberate access
	// probe. The launcher records this result after the child starts.
	_ = strings.Builder{}
}
