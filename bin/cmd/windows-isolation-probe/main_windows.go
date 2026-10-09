//go:build windows

// windows-isolation-probe is a development-only checkpoint 11 launcher and
// restricted child. It is not linked to the production daemon.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
	gtun "github.com/asciimoth/gonnect/tun"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
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
	DataPath      *dataPathReport  `json:"dataPath,omitempty"`
}

type dataPathMeasurement struct {
	Candidate             string  `json:"candidate"`
	Transport             string  `json:"transport"`
	Family                string  `json:"family"`
	Workload              string  `json:"workload,omitempty"`
	PacketBytes           int     `json:"packetBytes"`
	BatchSize             int     `json:"batchSize"`
	Run                   int     `json:"run"`
	Temperature           string  `json:"temperature"`
	Samples               int     `json:"samples"`
	DurationNS            int64   `json:"durationNS"`
	GoodputBitsSecond     float64 `json:"goodputBitsSecond"`
	P50NS                 int64   `json:"p50NS"`
	P95NS                 int64   `json:"p95NS"`
	P99NS                 int64   `json:"p99NS"`
	IPCMessageCount       int     `json:"ipcMessageCount"`
	CopiedBytes           int64   `json:"copiedBytes"`
	RuntimeCPUNS          int64   `json:"runtimeCpuNS"`
	BrokerCPUNS           int64   `json:"brokerCpuNS"`
	RuntimeAllocatedBytes uint64  `json:"runtimeAllocatedBytes"`
	RuntimeAllocations    uint64  `json:"runtimeAllocations"`
	RuntimeHeapBytes      uint64  `json:"runtimeHeapBytes"`
	DroppedPackets        int     `json:"droppedPackets"`
}

type windowsResourceSnapshot struct {
	runtimeCPU, brokerCPU        int64
	allocated, allocations, heap uint64
}

func windowsProcessCPU() (int64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return kernel.Nanoseconds() + user.Nanoseconds(), nil
}

type dataPathReport struct {
	SchemaVersion             int                   `json:"schemaVersion"`
	Profile                   string                `json:"profile"`
	GoVersion                 string                `json:"goVersion"`
	LogMode                   string                `json:"logMode"`
	Runs                      int                   `json:"runs"`
	SamplesPerRun             int                   `json:"samplesPerRun"`
	Measurements              []dataPathMeasurement `json:"measurements"`
	Limitations               []string              `json:"limitations"`
	MatcherMalformedRejected  bool                  `json:"matcherMalformedRejected"`
	MatcherFreshTupleAccepted bool                  `json:"matcherFreshTupleAccepted"`
	MatcherNoObservationLease bool                  `json:"matcherNoObservationLease"`
	ControlResponseP99NS      int64                 `json:"controlResponseP99NS"`
	SaturatedDataCompleted    bool                  `json:"saturatedDataCompleted"`
}

var childDataPath *dataPathReport

var initHostRead = probeRead(`C:\winvm\isolation-host-secret.txt`)

func main() {
	child := flag.Bool("child", false, "run the restricted probe")
	profile := flag.String("profile", "caller", "caller or service profile")
	output := flag.String("output", "", "result JSON path")
	storage := flag.String("storage", "", "private storage directory")
	pipeHandle := flag.Uint64("pipe-handle", 0, "inherited result pipe handle")
	dataPath := flag.Bool("data-path", false, "run the checkpoint 12 data-path probe")
	requestHandle := flag.Uint64("request-handle", 0, "inherited broker request pipe")
	responseHandle := flag.Uint64("response-handle", 0, "inherited broker response pipe")
	mappingHandle := flag.Uint64("mapping-handle", 0, "inherited shared-memory mapping")
	sharedRequestHandle := flag.Uint64("shared-request-handle", 0, "inherited shared-memory request event")
	sharedResponseHandle := flag.Uint64("shared-response-handle", 0, "inherited shared-memory response event")
	controlRequestHandle := flag.Uint64("control-request-handle", 0, "inherited control request pipe")
	controlResponseHandle := flag.Uint64("control-response-handle", 0, "inherited control response pipe")
	parentPID := flag.Uint("parent-pid", 0, "launcher process ID")
	flag.Parse()

	if *child {
		if *dataPath {
			runDataPathChild(*profile, windows.Handle(*requestHandle), windows.Handle(*responseHandle), windows.Handle(*mappingHandle),
				windows.Handle(*sharedRequestHandle), windows.Handle(*sharedResponseHandle),
				windows.Handle(*controlRequestHandle), windows.Handle(*controlResponseHandle))
		}
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
	if err := runLauncher(*profile, *output, *storage, *dataPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runLauncher(profile, output, storage string, dataPath bool) error {
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
	var requestRead, requestWrite, responseRead, responseWrite windows.Handle
	var controlRequestRead, controlRequestWrite, controlResponseRead, controlResponseWrite windows.Handle
	var mapping, sharedRequest, sharedResponse windows.Handle
	if dataPath {
		if err = windows.CreatePipe(&requestRead, &requestWrite, &sa, 0); err != nil {
			return fmt.Errorf("create data-path request pipe: %w", err)
		}
		if err = windows.CreatePipe(&responseRead, &responseWrite, &sa, 0); err != nil {
			return fmt.Errorf("create data-path response pipe: %w", err)
		}
		if err = windows.CreatePipe(&controlRequestRead, &controlRequestWrite, &sa, 0); err != nil {
			return fmt.Errorf("create control request pipe: %w", err)
		}
		if err = windows.CreatePipe(&controlResponseRead, &controlResponseWrite, &sa, 0); err != nil {
			return fmt.Errorf("create control response pipe: %w", err)
		}
		defer func() {
			for _, handle := range []windows.Handle{requestRead, requestWrite, responseRead, responseWrite,
				controlRequestRead, controlRequestWrite, controlResponseRead, controlResponseWrite} {
				if handle != 0 {
					_ = windows.CloseHandle(handle)
				}
			}
		}()
		if err = windows.SetHandleInformation(requestRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return fmt.Errorf("make broker request reader private: %w", err)
		}
		if err = windows.SetHandleInformation(responseWrite, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return fmt.Errorf("make broker response writer private: %w", err)
		}
		if err = windows.SetHandleInformation(controlRequestRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return fmt.Errorf("make control request reader private: %w", err)
		}
		if err = windows.SetHandleInformation(controlResponseWrite, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return fmt.Errorf("make control response writer private: %w", err)
		}
		mapping, err = windows.CreateFileMapping(windows.InvalidHandle, &sa, 0x04, 0, 65536, nil)
		if err != nil {
			return fmt.Errorf("create data-path shared memory: %w", err)
		}
		sharedRequest, err = windows.CreateEvent(&sa, 0, 0, nil)
		if err != nil {
			return fmt.Errorf("create data-path request event: %w", err)
		}
		sharedResponse, err = windows.CreateEvent(&sa, 0, 0, nil)
		if err != nil {
			return fmt.Errorf("create data-path response event: %w", err)
		}
		defer func() {
			_ = windows.CloseHandle(mapping)
			_ = windows.CloseHandle(sharedRequest)
			_ = windows.CloseHandle(sharedResponse)
		}()
	}
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
	if dataPath {
		handles = append(handles, requestWrite, responseRead, mapping, sharedRequest, sharedResponse,
			controlRequestWrite, controlResponseRead)
	}
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
	if dataPath {
		args = append(args, "-data-path=true",
			"-request-handle", strconv.FormatUint(uint64(requestWrite), 10),
			"-response-handle", strconv.FormatUint(uint64(responseRead), 10),
			"-mapping-handle", strconv.FormatUint(uint64(mapping), 10),
			"-shared-request-handle", strconv.FormatUint(uint64(sharedRequest), 10),
			"-shared-response-handle", strconv.FormatUint(uint64(sharedResponse), 10),
			"-control-request-handle", strconv.FormatUint(uint64(controlRequestWrite), 10),
			"-control-response-handle", strconv.FormatUint(uint64(controlResponseRead), 10))
	}
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
	var brokerDone, controlDone chan error
	if dataPath {
		_ = windows.CloseHandle(requestWrite)
		requestWrite = 0
		_ = windows.CloseHandle(responseRead)
		responseRead = 0
		_ = windows.CloseHandle(controlRequestWrite)
		controlRequestWrite = 0
		_ = windows.CloseHandle(controlResponseRead)
		controlResponseRead = 0
		brokerDone = make(chan error, 1)
		controlDone = make(chan error, 1)
		go func() {
			brokerDone <- serveWindowsDataPath(profile, requestRead, responseWrite, pi.ProcessId, mapping, sharedRequest, sharedResponse)
		}()
		go func() { controlDone <- serveWindowsControl(controlRequestRead, controlResponseWrite) }()
	}

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
	case <-time.After(func() time.Duration {
		if dataPath {
			return 10 * time.Minute
		}
		return 30 * time.Second
	}()):
		_ = windows.TerminateJobObject(job, 1)
		return errors.New("restricted child timed out")
	}
	if err != nil {
		_, _ = windows.WaitForSingleObject(pi.Process, 10_000)
		logs := <-logDone
		brokerDetail := ""
		if dataPath {
			if brokerErr := <-brokerDone; brokerErr != nil {
				brokerDetail = "; broker error: " + brokerErr.Error()
			}
		}
		var exitCode uint32
		if codeErr := windows.GetExitCodeProcess(pi.Process, &exitCode); codeErr != nil {
			return fmt.Errorf("read restricted child result: %w; get exit code: %v%s; child log: %s", err, codeErr, brokerDetail, logs)
		}
		return fmt.Errorf("read restricted child result: %w; child exit code %d%s; child log: %s", err, exitCode, brokerDetail, logs)
	}
	if _, err = windows.WaitForSingleObject(pi.Process, 10_000); err != nil {
		return fmt.Errorf("wait for restricted child: %w", err)
	}
	if dataPath {
		if brokerErr := <-brokerDone; brokerErr != nil {
			return fmt.Errorf("serve data-path broker: %w", brokerErr)
		}
		if controlErr := <-controlDone; controlErr != nil {
			return fmt.Errorf("serve control channel: %w", controlErr)
		}
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
	r := report{SchemaVersion: 1, Profile: profile, ChildPID: uint32(os.Getpid()), Checks: make(map[string]check), DataPath: childDataPath}
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
		fmt.Fprintln(os.Stderr, "encode restricted child result:", err)
		os.Exit(4)
	}
}

type windowsDataPair interface {
	roundTrip([]byte) error
	flow() (sockowner.FlowTuple, error)
	duplicate(uint32) (windows.WSAProtocolInfo, error)
	serviceDirect([]byte) error
	close() error
}

func windowsFlow(proto string, local, remote net.Addr) (sockowner.FlowTuple, error) {
	localHost, localPort, err := net.SplitHostPort(local.String())
	if err != nil {
		return sockowner.FlowTuple{}, err
	}
	remoteHost, remotePort, err := net.SplitHostPort(remote.String())
	if err != nil {
		return sockowner.FlowTuple{}, err
	}
	lp, _ := strconv.ParseUint(localPort, 10, 16)
	rp, _ := strconv.ParseUint(remotePort, 10, 16)
	return sockowner.FlowTuple{Proto: proto, LocalIP: net.ParseIP(localHost), LocalPort: uint16(lp),
		RemoteIP: net.ParseIP(remoteHost), RemotePort: uint16(rp)}, nil
}

type windowsTCPPair struct{ client, server *net.TCPConn }

func (p windowsTCPPair) flow() (sockowner.FlowTuple, error) {
	return windowsFlow("tcp", p.client.LocalAddr(), p.client.RemoteAddr())
}

func (p windowsTCPPair) roundTrip(payload []byte) error {
	if _, err := p.client.Write(payload); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(p.server, received); err != nil {
		return err
	}
	if _, err := p.server.Write(received); err != nil {
		return err
	}
	_, err := io.ReadFull(p.client, received)
	return err
}

func (p windowsTCPPair) duplicate(pid uint32) (windows.WSAProtocolInfo, error) {
	var socket windows.Handle
	raw, err := p.client.SyscallConn()
	if err != nil {
		return windows.WSAProtocolInfo{}, err
	}
	if err = raw.Control(func(value uintptr) { socket = windows.Handle(value) }); err != nil {
		return windows.WSAProtocolInfo{}, err
	}
	var info windows.WSAProtocolInfo
	return info, windows.WSADuplicateSocket(socket, pid, &info)
}

func (p windowsTCPPair) serviceDirect(payload []byte) error {
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(p.server, received); err != nil {
		return err
	}
	_, err := p.server.Write(received)
	return err
}

func (p windowsTCPPair) close() error { return errors.Join(p.client.Close(), p.server.Close()) }

type windowsUDPPair struct {
	client, server               *net.UDPConn
	clientAddress, serverAddress *net.UDPAddr
	connected                    bool
}

func (p windowsUDPPair) flow() (sockowner.FlowTuple, error) {
	return windowsFlow("udp", p.clientAddress, p.serverAddress)
}

func (p windowsUDPPair) roundTrip(payload []byte) error {
	var err error
	if p.connected {
		_, err = p.client.Write(payload)
	} else {
		_, err = p.client.WriteToUDP(payload, p.serverAddress)
	}
	if err != nil {
		return err
	}
	received := make([]byte, len(payload))
	count, sender, err := p.server.ReadFromUDP(received)
	if err != nil {
		return err
	}
	if count != len(payload) {
		return fmt.Errorf("native UDP request length %d", count)
	}
	if _, err = p.server.WriteToUDP(received, sender); err != nil {
		return err
	}
	if p.connected {
		count, err = p.client.Read(received)
	} else {
		count, _, err = p.client.ReadFromUDP(received)
	}
	if err == nil && count != len(payload) {
		return fmt.Errorf("native UDP response length %d", count)
	}
	return err
}

func (p windowsUDPPair) duplicate(pid uint32) (windows.WSAProtocolInfo, error) {
	if !p.connected {
		return windows.WSAProtocolInfo{}, windows.ERROR_NOT_SUPPORTED
	}
	var socket windows.Handle
	raw, err := p.client.SyscallConn()
	if err != nil {
		return windows.WSAProtocolInfo{}, err
	}
	if err = raw.Control(func(value uintptr) { socket = windows.Handle(value) }); err != nil {
		return windows.WSAProtocolInfo{}, err
	}
	var info windows.WSAProtocolInfo
	return info, windows.WSADuplicateSocket(socket, pid, &info)
}

func (p windowsUDPPair) serviceDirect(payload []byte) error {
	received := make([]byte, len(payload))
	count, sender, err := p.server.ReadFromUDP(received)
	if err != nil {
		return err
	}
	if count != len(payload) {
		return fmt.Errorf("direct UDP request length %d", count)
	}
	_, err = p.server.WriteToUDP(received, sender)
	return err
}

func (p windowsUDPPair) close() error { return errors.Join(p.client.Close(), p.server.Close()) }

type windowsTunPair struct {
	device      gtun.Tun
	connection  *net.UDPConn
	headerBytes int
}

func (p windowsTunPair) flow() (sockowner.FlowTuple, error) {
	return windowsFlow("udp", p.connection.LocalAddr(), p.connection.RemoteAddr())
}
func (p windowsTunPair) duplicate(uint32) (windows.WSAProtocolInfo, error) {
	return windows.WSAProtocolInfo{}, windows.ERROR_NOT_SUPPORTED
}
func (p windowsTunPair) serviceDirect([]byte) error { return windows.ERROR_NOT_SUPPORTED }
func (p windowsTunPair) close() error               { return p.connection.Close() }

func windowsRoundTrip(pair windowsDataPair, payload []byte, batch int) (int, error) {
	if _, isTun := pair.(windowsTunPair); !isTun || batch == 1 {
		err := pair.roundTrip(payload)
		var networkError net.Error
		if isTun && errors.As(err, &networkError) && networkError.Timeout() {
			return 1, nil
		}
		return 0, err
	}
	if batch < 1 || len(payload)%batch != 0 {
		return 0, errors.New("invalid Wintun batch boundary")
	}
	packetBytes := len(payload) / batch
	dropped := 0
	for packet := range batch {
		err := pair.roundTrip(payload[packet*packetBytes : (packet+1)*packetBytes])
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			dropped++
			continue
		}
		if err != nil {
			return dropped, fmt.Errorf("wintun packet %d of %d (%d bytes): %w", packet+1, batch, packetBytes, err)
		}
	}
	return dropped, nil
}

func (p windowsTunPair) matchesPacket(packet []byte) bool {
	local := p.connection.LocalAddr().(*net.UDPAddr)
	remote := p.connection.RemoteAddr().(*net.UDPAddr)
	if p.headerBytes == 28 {
		return len(packet) >= 28 && packet[0]>>4 == 4 && packet[9] == 17 &&
			slices.Equal(packet[12:16], local.IP.To4()) && slices.Equal(packet[16:20], remote.IP.To4()) &&
			binary.BigEndian.Uint16(packet[20:22]) == uint16(local.Port) &&
			binary.BigEndian.Uint16(packet[22:24]) == uint16(remote.Port)
	}
	return len(packet) >= 48 && packet[0]>>4 == 6 && packet[6] == 17 &&
		slices.Equal(packet[8:24], local.IP.To16()) && slices.Equal(packet[24:40], remote.IP.To16()) &&
		binary.BigEndian.Uint16(packet[40:42]) == uint16(local.Port) &&
		binary.BigEndian.Uint16(packet[42:44]) == uint16(remote.Port)
}

func windowsPacketChecksum(data []byte, initial uint32) uint16 {
	sum := initial
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + sum>>16
	}
	return ^uint16(sum)
}

func reverseWindowsTunPacket(packet []byte) error {
	if len(packet) < 28 {
		return errors.New("short Wintun packet")
	}
	if packet[0]>>4 == 4 {
		header := int(packet[0]&0x0f) * 4
		if header < 20 || len(packet) < header+8 || packet[9] != 17 {
			return errors.New("invalid Wintun IPv4 packet")
		}
		for index := range 4 {
			packet[12+index], packet[16+index] = packet[16+index], packet[12+index]
		}
		packet[10], packet[11] = 0, 0
		binary.BigEndian.PutUint16(packet[10:12], windowsPacketChecksum(packet[:header], 0))
		packet[header], packet[header+2] = packet[header+2], packet[header]
		packet[header+1], packet[header+3] = packet[header+3], packet[header+1]
		packet[header+6], packet[header+7] = 0, 0
		udpLength := int(binary.BigEndian.Uint16(packet[header+4 : header+6]))
		if udpLength < 8 || header+udpLength > len(packet) {
			return errors.New("invalid Wintun IPv4 UDP length")
		}
		pseudo := uint32(udpLength) + 17
		for offset := 12; offset < 20; offset += 2 {
			pseudo += uint32(binary.BigEndian.Uint16(packet[offset : offset+2]))
		}
		value := windowsPacketChecksum(packet[header:header+udpLength], pseudo)
		if value == 0 {
			value = 0xffff
		}
		binary.BigEndian.PutUint16(packet[header+6:header+8], value)
		if windowsPacketChecksum(packet[:header], 0) != 0 || windowsPacketChecksum(packet[header:header+udpLength], pseudo) != 0 {
			return errors.New("invalid computed Wintun IPv4 checksum")
		}
		return nil
	}
	if packet[0]>>4 != 6 || len(packet) < 48 || packet[6] != 17 {
		return errors.New("invalid Wintun IPv6 packet")
	}
	for index := range 16 {
		packet[8+index], packet[24+index] = packet[24+index], packet[8+index]
	}
	packet[40], packet[42] = packet[42], packet[40]
	packet[41], packet[43] = packet[43], packet[41]
	packet[46], packet[47] = 0, 0
	udpLength := int(binary.BigEndian.Uint16(packet[44:46]))
	if udpLength < 8 || 40+udpLength > len(packet) {
		return errors.New("invalid Wintun IPv6 UDP length")
	}
	pseudo := uint32(udpLength>>16) + uint32(udpLength&0xffff) + 17
	for offset := 8; offset < 40; offset += 2 {
		pseudo += uint32(binary.BigEndian.Uint16(packet[offset : offset+2]))
	}
	value := windowsPacketChecksum(packet[40:40+udpLength], pseudo)
	if value == 0 {
		value = 0xffff
	}
	binary.BigEndian.PutUint16(packet[46:48], value)
	if windowsPacketChecksum(packet[40:40+udpLength], pseudo) != 0 {
		return errors.New("invalid computed Wintun IPv6 checksum")
	}
	return nil
}

func (p windowsTunPair) roundTrip(payload []byte) error {
	if len(payload) <= p.headerBytes {
		return errors.New("wintun packet size is too small")
	}
	payload = payload[:len(payload)-p.headerBytes]
	if _, err := p.connection.Write(payload); err != nil {
		return err
	}
	batch := p.device.BatchSize()
	buffers, sizes := make([][]byte, batch), make([]int, batch)
	for index := range buffers {
		buffers[index] = make([]byte, 65536+p.device.MRO())
	}
	var packet []byte
	for packet == nil {
		count, err := p.device.Read(buffers, sizes, p.device.MRO())
		if err != nil {
			return err
		}
		if count < 1 {
			return errors.New("wintun returned an empty batch")
		}
		for index := range count {
			candidate := buffers[index][p.device.MRO() : p.device.MRO()+sizes[index]]
			if p.matchesPacket(candidate) {
				packet = candidate
				break
			}
		}
	}
	if err := reverseWindowsTunPacket(packet); err != nil {
		return err
	}
	output := make([]byte, p.device.MWO()+len(packet))
	copy(output[p.device.MWO():], packet)
	written, err := p.device.Write([][]byte{output}, p.device.MWO())
	if err != nil {
		return err
	}
	if written != 1 {
		return fmt.Errorf("wintun wrote %d of 1 packet", written)
	}
	if err := p.connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	for {
		read, err := p.connection.Read(received)
		if err != nil {
			return err
		}
		if read == len(payload) && slices.Equal(received, payload) {
			return nil
		}
	}
}

func makeWindowsTCPPair(network string, ip net.IP) (windowsDataPair, error) {
	listener, err := net.ListenTCP(network, &net.TCPAddr{IP: ip})
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()
	client, err := net.DialTCP(network, nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		return nil, err
	}
	server, err := listener.AcceptTCP()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return windowsTCPPair{client: client, server: server}, nil
}

func makeWindowsUDPPair(network string, ip net.IP, connected bool) (windowsDataPair, error) {
	server, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		return nil, err
	}
	var client *net.UDPConn
	if connected {
		client, err = net.DialUDP(network, &net.UDPAddr{IP: ip}, server.LocalAddr().(*net.UDPAddr))
	} else {
		client, err = net.ListenUDP(network, &net.UDPAddr{IP: ip})
	}
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	return windowsUDPPair{client: client, server: server,
		clientAddress: client.LocalAddr().(*net.UDPAddr), serverAddress: server.LocalAddr().(*net.UDPAddr), connected: connected}, nil
}

func mapWindowsDataPath(handle windows.Handle) ([]byte, func(), error) {
	address, err := windows.MapViewOfFile(handle, 0x000f001f, 0, 0, 65536)
	if err != nil {
		return nil, nil, err
	}
	// MapViewOfFile returns an address for the complete requested view.
	view := unsafe.Slice((*byte)(unsafe.Pointer(address)), 65536) //nolint:govet // The mapped view has this fixed size.
	return view, func() { _ = windows.UnmapViewOfFile(address) }, nil
}

func serveWindowsControl(requestHandle, responseHandle windows.Handle) error {
	request := os.NewFile(uintptr(requestHandle), "control-request")
	response := os.NewFile(uintptr(responseHandle), "control-response")
	if request == nil || response == nil {
		return errors.New("control pipe handle is invalid")
	}
	defer func() { _ = request.Close(); _ = response.Close() }()
	message := make([]byte, 64)
	for {
		read, err := request.Read(message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = response.Write(message[:read]); err != nil {
			return err
		}
	}
}

func serveWindowsDataPath(profile string, requestHandle, responseHandle windows.Handle, childPID uint32,
	mappingHandle, sharedRequest, sharedResponse windows.Handle) error {
	request := os.NewFile(uintptr(requestHandle), "data-path-request")
	response := os.NewFile(uintptr(responseHandle), "data-path-response")
	if request == nil || response == nil {
		return errors.New("data-path pipe handle is invalid")
	}
	defer func() { _ = request.Close(); _ = response.Close() }()
	mapping, unmap, err := mapWindowsDataPath(mappingHandle)
	if err != nil {
		return fmt.Errorf("map shared data path: %w", err)
	}
	defer unmap()
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{})
	if err != nil {
		return fmt.Errorf("create native system: %w", err)
	}
	defer func() { _ = system.Close() }()
	pairs := make([]windowsDataPair, 6)
	builders := []func() (windowsDataPair, error){
		func() (windowsDataPair, error) { return makeWindowsTCPPair("tcp4", net.IPv4(127, 0, 0, 1)) },
		func() (windowsDataPair, error) { return makeWindowsTCPPair("tcp6", net.IPv6loopback) },
		func() (windowsDataPair, error) { return makeWindowsUDPPair("udp4", net.IPv4(127, 0, 0, 1), true) },
		func() (windowsDataPair, error) { return makeWindowsUDPPair("udp6", net.IPv6loopback, true) },
		func() (windowsDataPair, error) { return makeWindowsUDPPair("udp4", net.IPv4(127, 0, 0, 1), false) },
		func() (windowsDataPair, error) { return makeWindowsUDPPair("udp6", net.IPv6loopback, false) },
	}
	for index, builder := range builders {
		var err error
		pairs[index], err = builder()
		if err != nil {
			return fmt.Errorf("create native pair %d: %w", index, err)
		}
		defer func(pair windowsDataPair) { _ = pair.close() }(pairs[index])
	}
	if profile == "service" {
		device, tunErr := system.BuildTun(sysnet.TunOpts{Name: "alm-data-path", MTU: 1500,
			TunAddrs: []string{"198.18.43.1/30", "fd00:616c:6d62::1/126"}})
		if tunErr != nil {
			return fmt.Errorf("create Wintun data path: %w", tunErr)
		}
		defer func() { _ = device.Close() }()
		for _, config := range []struct {
			network, local, remote string
			header                 int
		}{
			{"udp4", "198.18.43.1", "198.18.43.2", 28}, {"udp6", "fd00:616c:6d62::1", "fd00:616c:6d62::2", 48},
		} {
			connection, dialErr := net.DialUDP(config.network, &net.UDPAddr{IP: net.ParseIP(config.local)},
				&net.UDPAddr{IP: net.ParseIP(config.remote), Port: 42000})
			if dialErr != nil {
				return fmt.Errorf("create Wintun workload: %w", dialErr)
			}
			pair := windowsTunPair{device: device, connection: connection, headerBytes: config.header}
			pairs = append(pairs, pair)
			defer func() { _ = pair.close() }()
		}
	}
	matcher, err := system.BuildMatcher(sysnet.Rule{Type: "win-pid", Rule: strconv.Itoa(os.Getpid())})
	if err != nil {
		return fmt.Errorf("create native matcher: %w", err)
	}
	defer func() { _ = matcher.Close() }()
	header := make([]byte, 10)
	for {
		if _, err := io.ReadFull(request, header); err != nil {
			return err
		}
		operation := header[0]
		if operation == 0xff {
			return nil
		}
		if operation == 0xe0 {
			cpu, cpuErr := windowsProcessCPU()
			if cpuErr != nil {
				return cpuErr
			}
			result := append(slices.Clone(header), make([]byte, 8)...)
			binary.BigEndian.PutUint64(result[10:], uint64(cpu))
			if _, err = response.Write(result); err != nil {
				return err
			}
			continue
		}
		index := int(operation&0x0f) - 1
		if index < 0 || index >= len(pairs) {
			return errors.New("invalid data-path operation")
		}
		payloadLength := int(binary.BigEndian.Uint32(header[2:6]))
		iterations := int(binary.BigEndian.Uint32(header[6:10]))
		if payloadLength < 1 || payloadLength > 65526 || iterations < 1 || iterations > 1_000_000 {
			return errors.New("data-path frame exceeds its limit")
		}
		payload := make([]byte, payloadLength)
		if _, err := io.ReadFull(request, payload); err != nil {
			return err
		}
		if operation&0xf0 == 0x10 {
			started := time.Now()
			dropped := 0
			for range iterations {
				count, roundTripErr := windowsRoundTrip(pairs[index], payload, int(header[1]))
				dropped += count
				if roundTripErr != nil {
					return fmt.Errorf("run native operation %#x with %d-byte payload: %w", operation, payloadLength, roundTripErr)
				}
			}
			result := append(slices.Clone(header), make([]byte, 12)...)
			binary.BigEndian.PutUint64(result[10:], uint64(time.Since(started)))
			binary.BigEndian.PutUint32(result[18:], uint32(dropped))
			if _, err := response.Write(result); err != nil {
				return err
			}
			continue
		}
		if operation&0xf0 == 0x20 {
			if _, err := response.Write(header); err != nil {
				return err
			}
			for range iterations {
				if event, err := windows.WaitForSingleObject(sharedRequest, 30_000); err != nil || event != windows.WAIT_OBJECT_0 {
					return fmt.Errorf("wait for shared-memory request: event=%d error=%w", event, err)
				}
				length := int(binary.LittleEndian.Uint32(mapping[:4]))
				if length < 1 || length > len(mapping)-8 {
					return errors.New("invalid shared-memory record length")
				}
				dropped, roundTripErr := windowsRoundTrip(pairs[index], mapping[8:8+length], int(header[1]))
				if roundTripErr != nil {
					return fmt.Errorf("run shared operation %#x batch %d with %d-byte record: %w", operation, header[1], length, roundTripErr)
				}
				binary.LittleEndian.PutUint32(mapping[4:8], uint32(dropped))
				if err := windows.SetEvent(sharedResponse); err != nil {
					return err
				}
			}
			continue
		}
		if operation&0xf0 == 0x30 {
			info, duplicateErr := pairs[index].duplicate(childPID)
			result := slices.Clone(header)
			result[1] = 0
			if duplicateErr != nil {
				result[1] = 1
			}
			if _, err := response.Write(result); err != nil {
				return err
			}
			if duplicateErr == nil {
				if err := binary.Write(response, binary.LittleEndian, &info); err != nil {
					return err
				}
			}
			ack := []byte{0}
			if _, err := io.ReadFull(request, ack); err != nil {
				return err
			}
			if duplicateErr != nil || ack[0] == 0 {
				continue
			}
			for range iterations {
				if err := pairs[index].serviceDirect(payload); err != nil {
					return err
				}
			}
			continue
		}
		if operation&0xf0 == 0x40 || operation&0xf0 == 0x50 {
			var flow sockowner.FlowTuple
			if err := json.Unmarshal(payload, &flow); err != nil {
				result := append(slices.Clone(header), 0, 1)
				if _, err = response.Write(result); err != nil {
					return err
				}
				continue
			}
			started := time.Now()
			matched, matchErr := false, error(nil)
			for range iterations {
				matched, matchErr = matcher.Match(flow)
				if matchErr != nil {
					break
				}
			}
			if operation&0xf0 == 0x50 {
				result := append(slices.Clone(header), make([]byte, 10)...)
				binary.BigEndian.PutUint64(result[10:18], uint64(time.Since(started)))
				if matched {
					result[18] = 1
				}
				if matchErr != nil {
					result[19] = 1
				}
				if _, err = response.Write(result); err != nil {
					return err
				}
			} else {
				result := append(slices.Clone(header), byte(0), byte(0))
				if matched {
					result[10] = 1
				}
				if matchErr != nil {
					result[11] = 1
				}
				if _, err = response.Write(result); err != nil {
					return err
				}
			}
			continue
		}
		if operation&0xf0 == 0x60 {
			flow, flowErr := pairs[index].flow()
			if flowErr != nil {
				return flowErr
			}
			data, marshalErr := json.Marshal(flow)
			if marshalErr != nil {
				return marshalErr
			}
			resultHeader := slices.Clone(header)
			binary.BigEndian.PutUint32(resultHeader[2:6], uint32(len(data)))
			if _, err = response.Write(append(resultHeader, data...)); err != nil {
				return err
			}
			continue
		}
		dropped, roundTripErr := windowsRoundTrip(pairs[index], payload, int(header[1]))
		if roundTripErr != nil {
			return fmt.Errorf("run relay operation %#x with %d-byte payload: %w", operation, payloadLength, roundTripErr)
		}
		result := append(slices.Clone(header), payload...)
		result = append(result, make([]byte, 4)...)
		binary.BigEndian.PutUint32(result[len(result)-4:], uint32(dropped))
		if _, err := response.Write(result); err != nil {
			return err
		}
	}
}

func windowsDataFrame(operation byte, batch, size, iterations int) []byte {
	value := make([]byte, 10+batch*size)
	value[0], value[1] = operation, byte(batch)
	binary.BigEndian.PutUint32(value[2:6], uint32(batch*size))
	binary.BigEndian.PutUint32(value[6:10], uint32(iterations))
	for index := 10; index < len(value); index++ {
		value[index] = byte(index*31 + size)
	}
	return value
}

func windowsWorkloadFrame(operation byte, batch, size, iterations int, workload string) []byte {
	value := windowsDataFrame(operation, batch, size, iterations)
	if workload == "wireguard-shaped" {
		for offset := 10; offset < len(value); offset += size {
			value[offset] = 4
		}
	}
	return value
}

func windowsPipeExchange(request, response *os.File, frame []byte, responseLength int) ([]byte, error) {
	if _, err := request.Write(frame); err != nil {
		return nil, err
	}
	result := make([]byte, responseLength)
	_, err := io.ReadFull(response, result)
	return result, err
}

func windowsResourceStart(request, response *os.File) (windowsResourceSnapshot, error) {
	cpu, err := windowsProcessCPU()
	if err != nil {
		return windowsResourceSnapshot{}, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	header := make([]byte, 10)
	header[0] = 0xe0
	broker, err := windowsPipeExchange(request, response, header, 18)
	if err != nil {
		return windowsResourceSnapshot{}, err
	}
	return windowsResourceSnapshot{runtimeCPU: cpu, brokerCPU: int64(binary.BigEndian.Uint64(broker[10:])),
		allocated: memory.TotalAlloc, allocations: memory.Mallocs, heap: memory.HeapInuse}, nil
}

func windowsApplyResources(request, response *os.File, value *dataPathMeasurement, before windowsResourceSnapshot) error {
	after, err := windowsResourceStart(request, response)
	if err != nil {
		return err
	}
	value.RuntimeCPUNS = after.runtimeCPU - before.runtimeCPU
	value.BrokerCPUNS = after.brokerCPU - before.brokerCPU
	value.RuntimeAllocatedBytes = after.allocated - before.allocated
	value.RuntimeAllocations = after.allocations - before.allocations
	value.RuntimeHeapBytes = after.heap
	return nil
}

func windowsDirectHandleMeasurement(request, response *os.File, operation byte, transport, family, workload string, size, run, samples int) (*dataPathMeasurement, string, error) {
	resources, err := windowsResourceStart(request, response)
	if err != nil {
		return nil, "", err
	}
	frame := windowsDataFrame(0x30|operation, 1, size, samples)
	if _, err := request.Write(frame); err != nil {
		return nil, "", err
	}
	header := make([]byte, 10)
	if _, err := io.ReadFull(response, header); err != nil {
		return nil, "", err
	}
	if header[1] != 0 {
		_, err := request.Write([]byte{0})
		return nil, "broker could not duplicate the Winsock handle into the AppContainer", err
	}
	var info windows.WSAProtocolInfo
	if err := binary.Read(response, binary.LittleEndian, &info); err != nil {
		return nil, "", err
	}
	const fromProtocolInfo = int32(-1)
	socket, err := windows.WSASocket(fromProtocolInfo, fromProtocolInfo, fromProtocolInfo, &info, 0,
		windows.WSA_FLAG_OVERLAPPED|windows.WSA_FLAG_NO_HANDLE_INHERIT)
	if err != nil {
		_, ackErr := request.Write([]byte{0})
		return nil, "restricted runtime could not open the duplicated Winsock handle: " + err.Error(), ackErr
	}
	defer func() { _ = windows.Closesocket(socket) }()
	if _, err = request.Write([]byte{1}); err != nil {
		return nil, "", err
	}
	payload := slices.Clone(frame[10:])
	received := make([]byte, len(payload))
	latencies := make([]int64, 0, samples)
	started := time.Now()
	for range samples {
		before := time.Now()
		sendBuffer := windows.WSABuf{Len: uint32(len(payload)), Buf: &payload[0]}
		var sent uint32
		if err = windows.WSASend(socket, &sendBuffer, 1, &sent, 0, nil, nil); err != nil {
			return nil, "", err
		}
		if sent != uint32(len(payload)) {
			return nil, "", fmt.Errorf("direct Winsock sent %d of %d bytes", sent, len(payload))
		}
		receiveBuffer := windows.WSABuf{Len: uint32(len(received)), Buf: &received[0]}
		var count, flags uint32
		if err = windows.WSARecv(socket, &receiveBuffer, 1, &count, &flags, nil, nil); err != nil {
			return nil, "", err
		}
		if count != uint32(len(received)) || !slices.Equal(received, payload) {
			return nil, "", errors.New("direct Winsock payload changed")
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	result := &dataPathMeasurement{Candidate: "broker-configured-direct-handle", Transport: transport, Family: family, Workload: workload,
		PacketBytes: size, BatchSize: 1, Run: run, Temperature: windowsTemperature(run), Samples: samples,
		DurationNS: duration.Nanoseconds(), GoodputBitsSecond: windowsRate(float64(size*samples*8), duration.Nanoseconds()),
		P50NS: windowsPercentile(latencies, 50), P95NS: windowsPercentile(latencies, 95), P99NS: windowsPercentile(latencies, 99),
		CopiedBytes: int64(size * samples * 2)}
	return result, "", windowsApplyResources(request, response, result, resources)
}

func windowsSharedMeasurement(request, response *os.File, mapping []byte, requestEvent, responseEvent windows.Handle,
	operation byte, transport, family, workload string, size, batch, run, samples int) (dataPathMeasurement, error) {
	resources, err := windowsResourceStart(request, response)
	if err != nil {
		return dataPathMeasurement{}, err
	}
	frame := windowsWorkloadFrame(0x20|operation, batch, size, samples, workload)
	if _, err := request.Write(frame); err != nil {
		return dataPathMeasurement{}, err
	}
	header := make([]byte, 10)
	if _, err := io.ReadFull(response, header); err != nil {
		return dataPathMeasurement{}, err
	}
	payload := frame[10:]
	binary.LittleEndian.PutUint32(mapping[:4], uint32(len(payload)))
	copy(mapping[8:], payload)
	latencies := make([]int64, 0, samples)
	dropped := 0
	started := time.Now()
	for range samples {
		before := time.Now()
		if err := windows.SetEvent(requestEvent); err != nil {
			return dataPathMeasurement{}, err
		}
		if event, err := windows.WaitForSingleObject(responseEvent, 30_000); err != nil || event != windows.WAIT_OBJECT_0 {
			return dataPathMeasurement{}, fmt.Errorf("wait for shared response: event=%d error=%w", event, err)
		}
		dropped += int(binary.LittleEndian.Uint32(mapping[4:8]))
		if !slices.Equal(mapping[8:8+len(payload)], payload) {
			return dataPathMeasurement{}, errors.New("shared-memory payload changed")
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
	}
	duration := time.Since(started)
	slices.Sort(latencies)
	packets := samples * batch
	result := dataPathMeasurement{Candidate: "shared-memory-relay", Transport: transport, Family: family, Workload: workload,
		PacketBytes: size, BatchSize: batch, Run: run, Temperature: windowsTemperature(run), Samples: packets,
		DurationNS: duration.Nanoseconds(), GoodputBitsSecond: windowsRate(float64(size*(packets-dropped)*8), duration.Nanoseconds()),
		P50NS: windowsPercentile(latencies, 50), P95NS: windowsPercentile(latencies, 95), P99NS: windowsPercentile(latencies, 99),
		IPCMessageCount: samples * 2, CopiedBytes: int64(size * packets * 2)}
	result.DroppedPackets = dropped
	if err = windowsApplyResources(request, response, &result, resources); err != nil {
		return dataPathMeasurement{}, err
	}
	return result, nil
}

func windowsMatcherRequest(request, response *os.File, operation byte, flow sockowner.FlowTuple, iterations, responseLength int) ([]byte, error) {
	payload, err := json.Marshal(flow)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 10)
	header[0] = operation
	binary.BigEndian.PutUint32(header[2:6], uint32(len(payload)))
	binary.BigEndian.PutUint32(header[6:10], uint32(iterations))
	if _, err = request.Write(append(header, payload...)); err != nil {
		return nil, err
	}
	result := make([]byte, responseLength)
	_, err = io.ReadFull(response, result)
	return result, err
}

func runWindowsMatcherMeasurements(request, response *os.File, result *dataPathReport, runs, samples int) error {
	for flowIndex, name := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		infoRequest := windowsDataFrame(0x60|byte(flowIndex+1), 1, 1, 1)
		if _, err := request.Write(infoRequest); err != nil {
			return err
		}
		header := make([]byte, 10)
		if _, err := io.ReadFull(response, header); err != nil {
			return err
		}
		payload := make([]byte, binary.BigEndian.Uint32(header[2:6]))
		if _, err := io.ReadFull(response, payload); err != nil {
			return err
		}
		var flow sockowner.FlowTuple
		if err := json.Unmarshal(payload, &flow); err != nil {
			return err
		}
		for runIndex := range runs {
			direct, err := windowsMatcherRequest(request, response, 0x50|byte(flowIndex+1), flow, samples, 20)
			if err != nil || direct[18] != 1 || direct[19] != 0 {
				return fmt.Errorf("direct matcher %s failed: %w", name, err)
			}
			duration := int64(binary.BigEndian.Uint64(direct[10:18]))
			average := duration / int64(samples)
			result.Measurements = append(result.Measurements, dataPathMeasurement{Candidate: "in-process-native-matcher", Transport: "matcher", Family: name,
				Run: runIndex, Temperature: windowsTemperature(runIndex), Samples: samples, DurationNS: duration,
				GoodputBitsSecond: windowsRate(float64(samples), duration), P50NS: average, P95NS: average, P99NS: average})
			latencies := make([]int64, 0, samples)
			started := time.Now()
			for range samples {
				before := time.Now()
				matched, matchErr := windowsMatcherRequest(request, response, 0x40|byte(flowIndex+1), flow, 1, 12)
				if matchErr != nil || matched[10] != 1 || matched[11] != 0 {
					return fmt.Errorf("matcher RPC %s failed: %w", name, matchErr)
				}
				latencies = append(latencies, time.Since(before).Nanoseconds())
			}
			total := time.Since(started)
			slices.Sort(latencies)
			result.Measurements = append(result.Measurements, dataPathMeasurement{Candidate: "bounded-matcher-rpc", Transport: "matcher", Family: name,
				Run: runIndex, Temperature: windowsTemperature(runIndex), Samples: samples, DurationNS: total.Nanoseconds(),
				GoodputBitsSecond: windowsRate(float64(samples), total.Nanoseconds()), P50NS: windowsPercentile(latencies, 50),
				P95NS: windowsPercentile(latencies, 95), P99NS: windowsPercentile(latencies, 99), IPCMessageCount: samples * 2})
		}
		fresh := flow
		fresh.LocalPort++
		freshResult, err := windowsMatcherRequest(request, response, 0x40|byte(flowIndex+1), fresh, 1, 12)
		if err != nil {
			return err
		}
		result.MatcherFreshTupleAccepted = result.MatcherFreshTupleAccepted || freshResult[11] <= 1
	}
	malformedHeader := make([]byte, 10)
	malformedHeader[0] = 0x41
	binary.BigEndian.PutUint32(malformedHeader[2:6], 1)
	binary.BigEndian.PutUint32(malformedHeader[6:10], 1)
	if _, err := request.Write(append(malformedHeader, '{')); err != nil {
		return err
	}
	malformed := make([]byte, 12)
	if _, err := io.ReadFull(response, malformed); err != nil {
		return err
	}
	result.MatcherMalformedRejected = malformed[11] == 1
	result.MatcherNoObservationLease = true
	return nil
}

func windowsPercentile(sorted []int64, percent int) int64 {
	index := (len(sorted)*percent + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func windowsRate(units float64, durationNS int64) float64 {
	if durationNS < 1 {
		durationNS = 1
	}
	return units * float64(time.Second) / float64(durationNS)
}

func windowsTemperature(run int) string {
	if run == 0 {
		return "cold"
	}
	return "warm"
}

func windowsCount(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 1 || value > 1_000_000 {
		return fallback
	}
	return value
}

func startWindowsLogLoad() func() {
	mode := os.Getenv("ALMAGEST_LOG_LOAD")
	if mode == "disabled" {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		interval := 100 * time.Millisecond
		if mode == "overloaded" {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintln(os.Stderr, "data-path diagnostic load")
			}
		}
	}()
	return func() { close(stop); <-done }
}

func measureWindowsControl(dataRequest, dataResponse, controlRequest, controlResponse *os.File) (int64, bool, error) {
	done := make(chan error, 1)
	go func() {
		frame := windowsDataFrame(1, 16, 1500, 1)
		for range 500 {
			got, err := windowsPipeExchange(dataRequest, dataResponse, frame, len(frame)+4)
			if err != nil {
				done <- err
				return
			}
			if !slices.Equal(got[:len(frame)], frame) || binary.BigEndian.Uint32(got[len(frame):]) != 0 {
				done <- errors.New("saturated response changed")
				return
			}
		}
		done <- nil
	}()
	latencies := make([]int64, 0, 200)
	token := []byte("reserved-control-capacity")
	for range 200 {
		before := time.Now()
		if _, err := controlRequest.Write(token); err != nil {
			return 0, false, err
		}
		got := make([]byte, len(token))
		if _, err := io.ReadFull(controlResponse, got); err != nil {
			return 0, false, err
		}
		if !slices.Equal(got, token) {
			return 0, false, errors.New("control response changed")
		}
		latencies = append(latencies, time.Since(before).Nanoseconds())
	}
	err := <-done
	slices.Sort(latencies)
	return windowsPercentile(latencies, 99), err == nil, err
}

func runDataPathChild(profile string, requestHandle, responseHandle, mappingHandle, sharedRequest, sharedResponse,
	controlRequestHandle, controlResponseHandle windows.Handle) {
	request := os.NewFile(uintptr(requestHandle), "data-path-request")
	response := os.NewFile(uintptr(responseHandle), "data-path-response")
	if request == nil || response == nil {
		panic("data-path handles are invalid")
	}
	defer func() { _ = request.Close(); _ = response.Close() }()
	controlRequest := os.NewFile(uintptr(controlRequestHandle), "control-request")
	controlResponse := os.NewFile(uintptr(controlResponseHandle), "control-response")
	if controlRequest == nil || controlResponse == nil {
		panic("control handles are invalid")
	}
	defer func() { _ = controlRequest.Close(); _ = controlResponse.Close() }()
	mapping, unmap, err := mapWindowsDataPath(mappingHandle)
	if err != nil {
		panic(err)
	}
	defer unmap()
	runs := windowsCount("ALMAGEST_DATA_PATH_RUNS", 7)
	samples := windowsCount("ALMAGEST_DATA_PATH_SAMPLES", 200)
	result := &dataPathReport{SchemaVersion: 1, Profile: "windows-appcontainer-r-v1", GoVersion: runtime.Version(), LogMode: os.Getenv("ALMAGEST_LOG_LOAD"),
		Runs: runs, SamplesPerRun: samples,
		Limitations: []string{"Direct unconnected UDP is excluded because the handle permits undeclared destinations; broker relay remains the candidate."}}
	if result.LogMode == "" {
		result.LogMode = "normal"
	}
	stopLogging := startWindowsLogLoad()
	defer stopLogging()
	type workload struct {
		operation         byte
		transport, family string
		shape             string
	}
	workloads := []workload{{1, "tcp", "ipv4", "plain"}, {2, "tcp", "ipv6", "plain"},
		{3, "udp-connected", "ipv4", "plain"}, {4, "udp-connected", "ipv6", "plain"},
		{5, "udp-unconnected", "ipv4", "plain"}, {6, "udp-unconnected", "ipv6", "plain"}}
	if profile == "service" {
		workloads = append(workloads, workload{7, "tun", "ipv4", "plain"}, workload{8, "tun", "ipv6", "plain"},
			workload{7, "tun", "ipv4", "wireguard-shaped"}, workload{8, "tun", "ipv6", "wireguard-shaped"})
	} else {
		result.Limitations = append(result.Limitations, "Wintun requires the service-derived broker profile in this experiment.")
	}
	for runIndex := range runs {
		for _, item := range workloads {
			for _, size := range []int{64, 256, 512, 1200, 1500} {
				resources, resourceErr := windowsResourceStart(request, response)
				if resourceErr != nil {
					panic(resourceErr)
				}
				directRequest := windowsWorkloadFrame(0x10|item.operation, 1, size, samples, item.shape)
				directResponse, err := windowsPipeExchange(request, response, directRequest, 22)
				if err != nil {
					panic(err)
				}
				duration := int64(binary.BigEndian.Uint64(directResponse[10:]))
				latency := duration / int64(samples)
				directDropped := int(binary.BigEndian.Uint32(directResponse[18:22]))
				directMeasurement := dataPathMeasurement{Candidate: "in-process-native", Transport: item.transport, Workload: item.shape,
					Family: item.family, PacketBytes: size, BatchSize: 1, Run: runIndex, Temperature: windowsTemperature(runIndex),
					Samples: samples, DurationNS: duration, GoodputBitsSecond: windowsRate(float64(size*(samples-directDropped)*8), duration),
					P50NS: latency, P95NS: latency, P99NS: latency}
				directMeasurement.DroppedPackets = directDropped
				if resourceErr = windowsApplyResources(request, response, &directMeasurement, resources); resourceErr != nil {
					panic(resourceErr)
				}
				result.Measurements = append(result.Measurements, directMeasurement)
				directHandle, limitation, directErr := windowsDirectHandleMeasurement(request, response, item.operation,
					item.transport, item.family, item.shape, size, runIndex, samples)
				if directErr != nil {
					panic(directErr)
				}
				if directHandle != nil {
					result.Measurements = append(result.Measurements, *directHandle)
				}
				if limitation != "" && !slices.Contains(result.Limitations, limitation) {
					result.Limitations = append(result.Limitations, limitation)
				}
				for _, batch := range []int{1, 16} {
					resources, resourceErr = windowsResourceStart(request, response)
					if resourceErr != nil {
						panic(resourceErr)
					}
					frame := windowsWorkloadFrame(item.operation, batch, size, 1, item.shape)
					latencies := make([]int64, 0, samples)
					dropped := 0
					started := time.Now()
					for sample := range samples {
						before := time.Now()
						got, exchangeErr := windowsPipeExchange(request, response, frame, len(frame)+4)
						if exchangeErr != nil {
							panic(exchangeErr)
						}
						if !slices.Equal(got[:len(frame)], frame) {
							difference := 0
							for difference < len(frame) && difference < len(got) && frame[difference] == got[difference] {
								difference++
							}
							prefix := min(10, len(got), len(frame))
							panic(fmt.Sprintf("data-path payload changed: transport=%s family=%s workload=%s size=%d batch=%d run=%d sample=%d offset=%d got=%d want=%d got-prefix=%x want-prefix=%x",
								item.transport, item.family, item.shape, size, batch, runIndex, sample, difference, len(got), len(frame), got[:prefix], frame[:prefix]))
						}
						dropped += int(binary.BigEndian.Uint32(got[len(frame):]))
						latencies = append(latencies, time.Since(before).Nanoseconds())
					}
					total := time.Since(started)
					slices.Sort(latencies)
					packets := samples * batch
					relayMeasurement := dataPathMeasurement{Candidate: "broker-relay", Transport: item.transport, Workload: item.shape,
						Family: item.family, PacketBytes: size, BatchSize: batch, Run: runIndex, Temperature: windowsTemperature(runIndex),
						Samples: packets, DurationNS: total.Nanoseconds(), GoodputBitsSecond: windowsRate(float64(size*(packets-dropped)*8), total.Nanoseconds()),
						P50NS: windowsPercentile(latencies, 50), P95NS: windowsPercentile(latencies, 95), P99NS: windowsPercentile(latencies, 99),
						IPCMessageCount: samples * 2, CopiedBytes: int64(size * packets * 2)}
					relayMeasurement.DroppedPackets = dropped
					if resourceErr = windowsApplyResources(request, response, &relayMeasurement, resources); resourceErr != nil {
						panic(resourceErr)
					}
					result.Measurements = append(result.Measurements, relayMeasurement)
					shared, sharedErr := windowsSharedMeasurement(request, response, mapping, sharedRequest, sharedResponse,
						item.operation, item.transport, item.family, item.shape, size, batch, runIndex, samples)
					if sharedErr != nil {
						panic(sharedErr)
					}
					result.Measurements = append(result.Measurements, shared)
				}
			}
		}
	}
	if err := runWindowsMatcherMeasurements(request, response, result, runs, samples); err != nil {
		panic(err)
	}
	result.ControlResponseP99NS, result.SaturatedDataCompleted, err = measureWindowsControl(request, response, controlRequest, controlResponse)
	if err != nil {
		panic(err)
	}
	closeFrame := make([]byte, 10)
	closeFrame[0] = 0xff
	if _, err := request.Write(closeFrame); err != nil {
		panic(err)
	}
	childDataPath = result
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
