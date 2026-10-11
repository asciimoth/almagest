//go:build windows

package windowsprofile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"golang.org/x/sys/windows"
)

var (
	createRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	getMitigation         = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessMitigationPolicy")
)

// Request is a trusted, fixed launch description. Pipes contains only connected
// private byte channels, first the bootstrap channel. No native data, token,
// device, job, service, public admin listener or parent-process handle is valid.
// The launcher duplicates handles privately, without changing caller flags.
// Arguments are fixed by bootstrap; there is no shell or environment inheritance.
type Request struct {
	Plan       Plan
	Identity   *Identity
	Executable string
	Storage    string
	Arguments  []string
	Pipes      []windows.Handle
	// Lanes maps each connected pipe to one fixed role environment name. Nil
	// keeps the version-1 positional mapping for compatibility with installed
	// component launchers. Production composition supplies an explicit map so
	// an absent optional lane cannot shift a later handle into more authority.
	Lanes []roleentry.Lane
}

// Process owns a non-breakaway job. R, C and H allow one process; trusted N
// allows a bounded helper set. Close kills the role, including hung processes.
// Wait is cancellable and concurrent with Close.
type Process struct {
	mu           sync.Mutex
	process, job windows.Handle
	pid          uint32
	role         Role
	supervised   bool
}

func (p *Process) PID() uint32 { return p.pid }

// Kill terminates the complete job without discarding the process handle.
// Callers can still observe native exit with Wait before releasing handles.
func (p *Process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return fmt.Errorf("role process closed")
	}
	return windows.TerminateJobObject(p.job, 1)
}

func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return nil
	}
	err := windows.TerminateJobObject(p.job, 1)
	_ = windows.CloseHandle(p.job)
	_ = windows.CloseHandle(p.process)
	p.job, p.process = 0, 0
	return err
}

func (p *Process) Wait(ctx context.Context) (uint32, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		p.mu.Lock()
		if p.process == 0 {
			p.mu.Unlock()
			return 0, fmt.Errorf("role process closed")
		}
		status, err := windows.WaitForSingleObject(p.process, 0)
		var code uint32
		if err == nil && status == windows.WAIT_OBJECT_0 {
			err = windows.GetExitCodeProcess(p.process, &code)
		}
		p.mu.Unlock()
		if err != nil || status == windows.WAIT_OBJECT_0 {
			return code, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type securityCapabilities struct {
	SID             *windows.SID
	Capabilities    uintptr
	Count, Reserved uint32
}

// Launch never retries with weaker restrictions. Every error after process
// creation terminates the still-suspended child. No untrusted Go init runs
// before the exact token, mitigations and job have been checked.
func Launch(ctx context.Context, r Request) (*Process, error) {
	p, err := launch(ctx, r, nil)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return p, err
}

// beforeResume is a package-private fault/inspection seam. Production launch
// never supplies it; tests can inspect a child before any Go code executes.
func launch(ctx context.Context, r Request, beforeResume func(windows.Handle) error) (_ *Process, err error) {
	if err = r.Plan.Validate(); err != nil {
		return nil, err
	}
	if err = CheckPlatform(windows.RtlGetVersion().BuildNumber, runtime.GOARCH); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	maxPipes := 6
	switch r.Plan.Role {
	case Writer:
		maxPipes = 1
	case Command:
		maxPipes = 2
	}
	if len(r.Pipes) < 1 || len(r.Pipes) > maxPipes || len(r.Arguments) > 32 {
		return nil, fmt.Errorf("%w: invalid launch bounds", ErrUnavailable)
	}
	lanes, err := requestLanes(r)
	if err != nil {
		return nil, err
	}
	if r.Plan.Role == Network {
		if r.Identity != nil {
			return nil, fmt.Errorf("%w: trusted N must use the launch identity", ErrUnavailable)
		}
	} else if r.Identity == nil || r.Identity.sid == nil || r.Identity.role != r.Plan.Role {
		return nil, fmt.Errorf("%w: missing or foreign role identity", ErrUnavailable)
	}
	executable, err := lockPath(r.Executable, false, nil)
	if err != nil {
		return nil, err
	}
	defer closeHandles(executable)
	// The working directory is always the protected executable directory. It is
	// never runtime storage; no PATH or DLL-search override enters the child.
	var sid *windows.SID
	if r.Identity != nil {
		sid = r.Identity.sid
	}
	storage, err := lockPath(r.Storage, true, sid)
	if err != nil {
		return nil, err
	}
	defer closeHandles(storage)
	pipes, err := duplicatePipes(r.Pipes)
	if err != nil {
		return nil, err
	}
	defer closeHandles(pipes)
	attrs, err := windows.NewProcThreadAttributeList(3)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&pipes[0]), uintptr(len(pipes))*unsafe.Sizeof(pipes[0])); err != nil {
		return nil, err
	}
	caps := securityCapabilities{SID: sid}
	if sid != nil {
		if err = attrs.Update(0x00020009, unsafe.Pointer(&caps), unsafe.Sizeof(caps)); err != nil {
			return nil, err
		}
	}
	// DEP, SEHOP, strict handles, no remote/Low images and System32 preference.
	// No unsupported CFG or dynamic-code guarantee is claimed for Go binaries.
	mitigations := uint64(1 | 4 | 1<<24 | 1<<52 | 1<<56 | 1<<60)
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_MITIGATION_POLICY, unsafe.Pointer(&mitigations), unsafe.Sizeof(mitigations)); err != nil {
		return nil, err
	}
	job, err := newJob(r.Plan)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = windows.CloseHandle(job)
		}
	}()
	var source windows.Token
	if err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY, &source); err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	token := source
	if r.Plan.Role != Network {
		administrators, e := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
		if e != nil {
			return nil, e
		}
		denyAdmin := windows.SIDAndAttributes{Sid: administrators}
		token = 0
		ok, _, callErr := createRestrictedToken.Call(uintptr(source), 1, 1, uintptr(unsafe.Pointer(&denyAdmin)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&token)))
		if ok == 0 {
			return nil, fmt.Errorf("%w: restricted token: %v", ErrUnavailable, callErr)
		}
		defer func() { _ = token.Close() }()
	}
	args := append([]string{r.Executable}, r.Arguments...)
	command := windows.ComposeCommandLine(args)
	if len(command) > 16000 || strings.ContainsRune(command, 0) {
		return nil, fmt.Errorf("%w: invalid command line", ErrUnavailable)
	}
	cmd, err := windows.UTF16PtrFromString(command)
	if err != nil {
		return nil, err
	}
	app, _ := windows.UTF16PtrFromString(r.Executable)
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(r.Executable))
	systemRoot, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	localAppData, err := source.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return nil, err
	}
	// List actual child handle values, not the original handles. Environment is
	// constructed from scratch and never contains protected input or parent data.
	envValues := []string{"SystemRoot=" + systemRoot, "SystemDrive=" + filepath.VolumeName(systemRoot),
		"LOCALAPPDATA=" + localAppData, "TEMP=" + r.Storage, "TMP=" + r.Storage,
		"ALMAGEST_ROLE=" + string(r.Plan.Role), "ALMAGEST_STORAGE=" + r.Storage}
	for n, pipe := range pipes {
		envValues = append(envValues, "ALMAGEST_PIPE_"+strconv.Itoa(n)+"="+strconv.FormatUint(uint64(pipe), 10))
	}
	handles := make(map[roleentry.Lane]windows.Handle, len(lanes))
	for index, lane := range lanes {
		handles[lane] = pipes[index]
	}
	for _, lane := range []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData, roleentry.LaneDiagnostics,
		roleentry.LaneAdmin, roleentry.LaneConfig, roleentry.LaneCommand, roleentry.LaneCallback} {
		value := uint64(handles[lane])
		envValues = append(envValues, string(lane)+"="+strconv.FormatUint(value, 10))
	}
	slices.SortFunc(envValues, func(a, b string) int { return strings.Compare(strings.ToUpper(a), strings.ToUpper(b)) })
	env := utf16.Encode([]rune(strings.Join(envValues, "\x00") + "\x00\x00"))
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if r.Plan.Role == Network {
		err = windows.CreateProcess(app, cmd, nil, nil, true, flags, &env[0], cwd, &si.StartupInfo, &pi)
	} else {
		err = windows.CreateProcessAsUser(token, app, cmd, nil, nil, true, flags, &env[0], cwd, &si.StartupInfo, &pi)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: create suspended role: %v", ErrUnavailable, err)
	}
	defer func() {
		_ = windows.CloseHandle(pi.Thread)
		if !started {
			_ = windows.TerminateProcess(pi.Process, 1)
			_ = windows.CloseHandle(pi.Process)
		}
	}()
	if err = windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		return nil, err
	}
	if r.Plan.Role == Network {
		err = verifyNetworkProcess(pi.Process, source)
	} else {
		err = verifyRestrictedProcess(pi.Process, sid)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: suspended child: %v", ErrUnavailable, err)
	}
	if beforeResume != nil {
		if err = beforeResume(pi.Process); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		return nil, err
	}
	runtime.KeepAlive(caps)
	runtime.KeepAlive(pipes)
	started = true
	return &Process{process: pi.Process, job: job, pid: pi.ProcessId, role: r.Plan.Role}, nil
}

func requestLanes(r Request) ([]roleentry.Lane, error) {
	lanes := r.Lanes
	explicit := len(lanes) != 0
	if len(lanes) == 0 {
		lanes = []roleentry.Lane{roleentry.LaneBootstrap}
		switch r.Plan.Role {
		case Runtime:
			legacy := []roleentry.Lane{roleentry.LaneData, roleentry.LaneDiagnostics, roleentry.LaneAdmin, roleentry.LaneConfig, roleentry.LaneCommand}
			lanes = append(lanes, legacy[:len(r.Pipes)-1]...)
		case Command:
			if len(r.Pipes) == 2 {
				lanes = append(lanes, roleentry.LaneCallback)
			}
		}
	}
	if len(lanes) != len(r.Pipes) || len(lanes) == 0 || lanes[0] != roleentry.LaneBootstrap {
		return nil, fmt.Errorf("%w: invalid role lane mapping", ErrUnavailable)
	}
	seen := make(map[roleentry.Lane]bool, len(lanes))
	for _, lane := range lanes {
		allowed := lane == roleentry.LaneBootstrap
		switch r.Plan.Role {
		case Runtime:
			allowed = allowed || lane == roleentry.LaneData || lane == roleentry.LaneDiagnostics || lane == roleentry.LaneAdmin || lane == roleentry.LaneConfig || lane == roleentry.LaneCommand
		case Command:
			allowed = allowed || lane == roleentry.LaneCallback
		}
		if !allowed || seen[lane] {
			return nil, fmt.Errorf("%w: invalid role lane mapping", ErrUnavailable)
		}
		seen[lane] = true
	}
	if explicit && r.Plan.Role == Runtime && (!seen[roleentry.LaneData] || !seen[roleentry.LaneDiagnostics]) {
		return nil, fmt.Errorf("%w: runtime data lanes are required", ErrUnavailable)
	}
	return append([]roleentry.Lane(nil), lanes...), nil
}

func duplicatePipes(input []windows.Handle) ([]windows.Handle, error) {
	var copies []windows.Handle
	seen := make(map[windows.Handle]bool)
	for _, h := range input {
		if h == 0 || h == windows.InvalidHandle || seen[h] {
			closeHandles(copies)
			return nil, fmt.Errorf("%w: invalid or duplicate pipe", ErrUnavailable)
		}
		seen[h] = true
		kind, err := windows.GetFileType(h)
		if err != nil || kind != windows.FILE_TYPE_PIPE {
			closeHandles(copies)
			return nil, fmt.Errorf("%w: only private pipes can be inherited", ErrUnavailable)
		}
		// Winsock handles can also report FILE_TYPE_PIPE. Require a named/anonymous
		// pipe object; this rejects sockets and every device/control handle.
		var flags uint32
		if err = windows.GetNamedPipeInfo(h, &flags, nil, nil, nil); err != nil || flags&windows.PIPE_SERVER_END != 0 {
			closeHandles(copies)
			return nil, fmt.Errorf("%w: only a private pipe client can be inherited", ErrUnavailable)
		}
		var copy windows.Handle
		if err = windows.DuplicateHandle(windows.CurrentProcess(), h, windows.CurrentProcess(), &copy, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			closeHandles(copies)
			return nil, err
		}
		copies = append(copies, copy)
	}
	return copies, nil
}

func newJob(p Plan) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (windows.Handle, error) { _ = windows.CloseHandle(job); return 0, err }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	limits.BasicLimitInformation.ActiveProcessLimit = p.processLimit()
	limits.ProcessMemoryLimit = uintptr(p.MemoryBytes)
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fail(err)
	}
	cpu := struct{ Flags, Rate uint32 }{Flags: 1 | 4, Rate: p.CPUPercent * 100}
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation, uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu))); err != nil {
		return fail(err)
	}
	ui := uint32(0xff)
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), 4); err != nil {
		return fail(err)
	}
	return job, nil
}

func tokenInfo(token windows.Token, class uint32) ([]byte, error) {
	var size uint32
	// Some fixed-size classes return ERROR_BAD_LENGTH for a zero-size query.
	// Start with real storage and retry only an explicitly larger bounded result.
	data := make([]byte, 256)
	err := windows.GetTokenInformation(token, class, &data[0], uint32(len(data)), &size)
	if (err == windows.ERROR_INSUFFICIENT_BUFFER || err == windows.ERROR_BAD_LENGTH) && size > uint32(len(data)) && size <= 65536 {
		data = make([]byte, size)
		err = windows.GetTokenInformation(token, class, &data[0], size, &size)
	}
	return data, err
}

func verifyRestrictedProcess(process windows.Handle, sid *windows.SID) error {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer func() { _ = token.Close() }()
	// IsTokenRestricted only reports restricting SIDs; a privilege-filtered
	// token instead reports TokenHasRestrictions. AppContainer supplies the
	// additional object boundary for R/C/H.
	restricted, err := tokenDWORD(token, windows.TokenHasRestrictions)
	if err != nil || restricted == 0 {
		return fmt.Errorf("token has no restrictions: %v", err)
	}
	elevated, err := tokenElevated(token)
	if err != nil || elevated {
		return fmt.Errorf("elevated token or failed elevation query: %v", err)
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			return fmt.Errorf("enabled administrator group")
		}
	}
	uiAccess, err := tokenDWORD(token, windows.TokenUIAccess)
	if err != nil || uiAccess != 0 {
		return fmt.Errorf("UI-access token or failed query: %v", err)
	}
	privs, err := tokenInfo(token, windows.TokenPrivileges)
	if err != nil {
		return err
	}
	p := (*windows.Tokenprivileges)(unsafe.Pointer(&privs[0]))
	// DISABLE_MAX_PRIVILEGE keeps SeChangeNotifyPrivilege for directory traversal.
	for _, privilege := range p.AllPrivileges() {
		if privilege.Luid.HighPart != 0 || privilege.Luid.LowPart != 23 {
			return fmt.Errorf("unexpected token privilege")
		}
	}
	if sid != nil {
		appContainer, err := tokenDWORD(token, 29) // TokenIsAppContainer
		if err != nil || appContainer != 1 {
			return fmt.Errorf("not an AppContainer: %v", err)
		}
		data, err := tokenInfo(token, 31) // TokenAppContainerSid
		if err != nil {
			return err
		}
		actual := *(**windows.SID)(unsafe.Pointer(&data[0]))
		if actual == nil || !actual.Equals(sid) {
			return fmt.Errorf("foreign AppContainer SID")
		}
		data, err = tokenInfo(token, 30) // TokenCapabilities
		if err != nil || *(*uint32)(unsafe.Pointer(&data[0])) != 0 {
			return fmt.Errorf("AppContainer has capabilities: %v", err)
		}
		data, err = tokenInfo(token, windows.TokenIntegrityLevel)
		if err != nil {
			return err
		}
		label := (*windows.SIDAndAttributes)(unsafe.Pointer(&data[0]))
		count := label.Sid.SubAuthorityCount()
		if count == 0 || label.Sid.SubAuthority(uint32(count-1)) > 0x1000 {
			return fmt.Errorf("integrity is above Low")
		}
	}
	return verifyMitigations(process)
}

func verifyNetworkProcess(process windows.Handle, source windows.Token) error {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer func() { _ = token.Close() }()
	for _, class := range []uint32{
		windows.TokenUser,
		windows.TokenGroups,
		windows.TokenPrivileges,
		windows.TokenOwner,
		windows.TokenPrimaryGroup,
		windows.TokenRestrictedSids,
		windows.TokenIntegrityLevel,
		30, // TokenCapabilities
	} {
		if err := equalTokenInformation(source, token, class); err != nil {
			return err
		}
	}
	for _, class := range []uint32{
		windows.TokenType,
		windows.TokenSessionId,
		windows.TokenSandBoxInert,
		windows.TokenElevationType,
		windows.TokenElevation,
		windows.TokenHasRestrictions,
		windows.TokenVirtualizationAllowed,
		windows.TokenVirtualizationEnabled,
		windows.TokenUIAccess,
		29, // TokenIsAppContainer
	} {
		want, err := tokenDWORD(source, class)
		if err != nil {
			return fmt.Errorf("source token class %d: %w", class, err)
		}
		got, err := tokenDWORD(token, class)
		if err != nil {
			return fmt.Errorf("child token class %d: %w", class, err)
		}
		if got != want {
			return fmt.Errorf("network token class %d changed", class)
		}
	}
	if err := equalTokenAuthentication(source, token); err != nil {
		return err
	}
	return verifyMitigations(process)
}

func equalTokenAuthentication(source, child windows.Token) error {
	type tokenStatistics struct {
		TokenID, AuthenticationID windows.LUID
		ExpirationTime            int64
		TokenType, Impersonation  uint32
		DynamicCharged            uint32
		DynamicAvailable          uint32
		GroupCount                uint32
		PrivilegeCount            uint32
		ModifiedID                windows.LUID
	}
	want, err := tokenInfo(source, windows.TokenStatistics)
	if err != nil {
		return fmt.Errorf("source token statistics: %w", err)
	}
	got, err := tokenInfo(child, windows.TokenStatistics)
	if err != nil {
		return fmt.Errorf("child token statistics: %w", err)
	}
	a := (*tokenStatistics)(unsafe.Pointer(&want[0]))
	b := (*tokenStatistics)(unsafe.Pointer(&got[0]))
	if a.AuthenticationID != b.AuthenticationID {
		return fmt.Errorf("network authentication identity changed")
	}
	return nil
}

func equalTokenInformation(source, child windows.Token, class uint32) error {
	want, err := tokenInfo(source, class)
	if err != nil {
		return fmt.Errorf("source token class %d: %w", class, err)
	}
	got, err := tokenInfo(child, class)
	if err != nil {
		return fmt.Errorf("child token class %d: %w", class, err)
	}
	// Variable token information contains process-local pointers. Compare the
	// stable semantic forms for those classes instead of their raw storage.
	switch class {
	case windows.TokenUser:
		wantSID := (*windows.Tokenuser)(unsafe.Pointer(&want[0])).User.Sid
		gotSID := (*windows.Tokenuser)(unsafe.Pointer(&got[0])).User.Sid
		if wantSID == nil || gotSID == nil || !wantSID.Equals(gotSID) {
			return fmt.Errorf("network token user changed")
		}
	case windows.TokenGroups, windows.TokenRestrictedSids, 30: // TokenCapabilities
		if !equalTokenGroups(want, got) {
			return fmt.Errorf("network token groups changed for class %d", class)
		}
	case windows.TokenPrivileges:
		if !equalTokenPrivileges(want, got) {
			return fmt.Errorf("network token privileges changed")
		}
	case windows.TokenIntegrityLevel:
		wantLabel := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&want[0])).Label.Sid
		gotLabel := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&got[0])).Label.Sid
		if wantLabel == nil || gotLabel == nil || !wantLabel.Equals(gotLabel) {
			return fmt.Errorf("network token integrity changed")
		}
	case windows.TokenOwner:
		type tokenOwner struct{ SID *windows.SID }
		wantSID := (*tokenOwner)(unsafe.Pointer(&want[0])).SID
		gotSID := (*tokenOwner)(unsafe.Pointer(&got[0])).SID
		if wantSID == nil || gotSID == nil || !wantSID.Equals(gotSID) {
			return fmt.Errorf("network token owner changed")
		}
	case windows.TokenPrimaryGroup:
		wantSID := (*windows.Tokenprimarygroup)(unsafe.Pointer(&want[0])).PrimaryGroup
		gotSID := (*windows.Tokenprimarygroup)(unsafe.Pointer(&got[0])).PrimaryGroup
		if wantSID == nil || gotSID == nil || !wantSID.Equals(gotSID) {
			return fmt.Errorf("network token primary group changed")
		}
	default:
		if !slices.Equal(want, got) {
			return fmt.Errorf("network token class %d changed", class)
		}
	}
	return nil
}

func equalTokenGroups(first, second []byte) bool {
	a := (*windows.Tokengroups)(unsafe.Pointer(&first[0])).AllGroups()
	b := (*windows.Tokengroups)(unsafe.Pointer(&second[0])).AllGroups()
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n].Attributes != b[n].Attributes || a[n].Sid == nil || b[n].Sid == nil || !a[n].Sid.Equals(b[n].Sid) {
			return false
		}
	}
	return true
}

func equalTokenPrivileges(first, second []byte) bool {
	a := (*windows.Tokenprivileges)(unsafe.Pointer(&first[0])).AllPrivileges()
	b := (*windows.Tokenprivileges)(unsafe.Pointer(&second[0])).AllPrivileges()
	return slices.Equal(a, b)
}

func verifyMitigations(process windows.Handle) error {
	for _, policy := range []struct{ class, mask uint32 }{{3, 1}, {10, 7}} { // strict handles, image loading
		var flags uint32
		ok, _, e := getMitigation.Call(uintptr(process), uintptr(policy.class), uintptr(unsafe.Pointer(&flags)), 4)
		if ok == 0 || flags&policy.mask != policy.mask {
			return fmt.Errorf("mitigation %d missing: %v", policy.class, e)
		}
	}
	return nil
}

func tokenElevated(token windows.Token) (bool, error) {
	value, err := tokenDWORD(token, windows.TokenElevation)
	return value != 0, err
}

func tokenDWORD(token windows.Token, class uint32) (uint32, error) {
	var value, size uint32
	err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), 4, &size)
	return value, err
}
