//go:build windows

package experimentalnodes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	tor "github.com/asciimoth/tor-driver"
	"github.com/asciimoth/tor-driver/direct"
	"golang.org/x/sys/windows"
)

const (
	createRestrictedTokenDisableMaxPrivilege = 0x1
	createRestrictedTokenLUAToken            = 0x4
)

var createRestrictedTokenProc = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

type windowsXTorProcesses struct {
	base tor.Processes
}

func newXTorProcessPolicy(system *direct.BestEffortSystem) (tor.Processes, error) {
	return &windowsXTorProcesses{base: system}, nil
}

func (p *windowsXTorProcesses) PID() int         { return p.base.PID() }
func (p *windowsXTorProcesses) Platform() string { return p.base.Platform() }

func (p *windowsXTorProcesses) Start(ctx context.Context, spec tor.Launch) (tor.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Identity != nil {
		return nil, errors.New("XTorDriver Linux UID/GID is not supported on Windows")
	}

	var current windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY,
		&current,
	); err != nil {
		return nil, fmt.Errorf("open XTorDriver parent token: %w", err)
	}
	defer func() { _ = current.Close() }()
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("create Administrators SID: %w", err)
	}
	admin, err := xTorTokenIsMember(current, adminSID)
	if err != nil {
		return nil, fmt.Errorf("check XTorDriver parent token membership: %w", err)
	}
	if !current.IsElevated() && !admin {
		return p.base.Start(ctx, spec)
	}

	restricted, err := createRestrictedPrimaryToken(current)
	if err != nil {
		return nil, fmt.Errorf("drop XTorDriver administrator privileges: %w", err)
	}
	defer func() { _ = restricted.Close() }()
	if err = validateRestrictedXTorToken(restricted); err != nil {
		return nil, err
	}
	return startRestrictedXTorProcess(spec, restricted)
}

func createRestrictedPrimaryToken(current windows.Token) (windows.Token, error) {
	var restricted windows.Token
	result, _, callErr := createRestrictedTokenProc.Call(
		uintptr(current),
		createRestrictedTokenDisableMaxPrivilege|createRestrictedTokenLUAToken,
		0, 0,
		0, 0,
		0, 0,
		uintptr(unsafe.Pointer(&restricted)),
	)
	if result == 0 {
		return 0, callErr
	}
	return restricted, nil
}

func validateRestrictedXTorToken(token windows.Token) error {
	if token.IsElevated() {
		return errors.New("XTorDriver restricted token remains elevated")
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("create Administrators SID: %w", err)
	}
	admin, err := xTorTokenIsMember(token, adminSID)
	if err != nil {
		return fmt.Errorf("check XTorDriver restricted token membership: %w", err)
	}
	if admin {
		return errors.New("XTorDriver restricted token retains Administrators membership")
	}
	return nil
}

func xTorTokenIsMember(token windows.Token, sid *windows.SID) (bool, error) {
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(
		token,
		windows.TOKEN_QUERY,
		nil,
		windows.SecurityImpersonation,
		windows.TokenImpersonation,
		&impersonation,
	); err != nil {
		return false, err
	}
	defer func() { _ = impersonation.Close() }()
	return impersonation.IsMember(sid)
}

func startRestrictedXTorProcess(spec tor.Launch, token windows.Token) (tor.Process, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create XTorDriver Job Object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure XTorDriver Job Object: %w", err)
	}

	cmd := restrictedXTorCommand(spec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED,
		HideWindow:    true,
		Token:         syscall.Token(token),
	}
	if err = cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	fail := func(startErr error) (tor.Process, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return nil, startErr
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fail(fmt.Errorf("open suspended XTorDriver process: %w", err))
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return fail(fmt.Errorf("assign XTorDriver process to Job Object: %w", err))
	}
	if err = resumeXTorProcess(uint32(cmd.Process.Pid)); err != nil {
		return fail(fmt.Errorf("resume XTorDriver process: %w", err))
	}
	return &windowsXTorProcess{cmd: cmd, job: job}, nil
}

func restrictedXTorCommand(spec tor.Launch) *exec.Cmd {
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Directory
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	return cmd
}

func resumeXTorProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err = windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == pid {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			return errors.Join(resumeErr, closeErr)
		}
		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return errors.New("suspended XTorDriver process has no thread")
		}
		if err != nil {
			return err
		}
	}
}

type windowsXTorProcess struct {
	cmd         *exec.Cmd
	job         windows.Handle
	releaseOnce sync.Once
	releaseErr  error
}

func (p *windowsXTorProcess) Wait() error { return p.cmd.Wait() }
func (p *windowsXTorProcess) Kill() error { return windows.TerminateJobObject(p.job, 1) }
func (p *windowsXTorProcess) Release() error {
	p.releaseOnce.Do(func() { p.releaseErr = windows.CloseHandle(p.job) })
	return p.releaseErr
}

var _ tor.Processes = (*windowsXTorProcesses)(nil)
var _ tor.Process = (*windowsXTorProcess)(nil)
