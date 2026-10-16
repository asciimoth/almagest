package direct

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/lib/sandbox"
	"golang.org/x/sys/windows"
)

type processBoundary interface {
	Signal(sandbox.Signal) error
	Kill() error
	Close() error
}

type windowsProcessBoundary struct {
	mu  sync.Mutex
	job windows.Handle
}

func prepareProcess(command *exec.Cmd, instance *instance) {
	attributes := &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED, HideWindow: true}
	if instance.windowsBoundary != nil {
		attributes.Token = syscall.Token(instance.windowsBoundary.token)
	}
	command.SysProcAttr = attributes
}

func attachProcess(command *exec.Cmd, instance *instance) (processBoundary, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if instance.windowsBoundary != nil {
		err = windows.AssignProcessToJobObject(instance.windowsBoundary.job, handle)
		if err == nil {
			err = windows.AssignProcessToJobObject(job, handle)
		}
	} else {
		err = windows.AssignProcessToJobObject(job, handle)
	}
	_ = windows.CloseHandle(handle)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if err = resumeInitialThread(uint32(command.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &windowsProcessBoundary{job: job}, nil
}

func startProcess(command *exec.Cmd, instance *instance) (processBoundary, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	boundary, err := attachProcess(command, instance)
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}
	return boundary, err
}

func resumeInitialThread(processID uint32) error {
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
		if entry.OwnerProcessID == processID {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			previous, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if resumeErr != nil || previous != 1 || closeErr != nil {
				return errors.Join(resumeErr, closeErr, sandbox.ErrLaunch)
			}
			return nil
		}
		if err = windows.Thread32Next(snapshot, &entry); err != nil {
			return errors.Join(sandbox.ErrLaunch, err)
		}
	}
}

func (b *windowsProcessBoundary) Signal(signal sandbox.Signal) error {
	if signal == sandbox.Kill {
		return b.Kill()
	}
	return sandbox.ErrUnsupported
}
func (b *windowsProcessBoundary) Kill() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(b.job, 1)
}
func (b *windowsProcessBoundary) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.job == 0 {
		return nil
	}
	err := terminateAndWaitJob(b.job)
	err = errors.Join(err, windows.CloseHandle(b.job))
	b.job = 0
	return err
}

type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func activeJobProcesses(job windows.Handle) (uint32, error) {
	var accounting jobAccounting
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	return accounting.ActiveProcesses, err
}

func terminateAndWaitJob(job windows.Handle) error {
	active, err := activeJobProcesses(job)
	if err != nil {
		return err
	}
	if active != 0 {
		if err = windows.TerminateJobObject(job, 1); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for active != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		active, err = activeJobProcesses(job)
		if err != nil {
			return err
		}
	}
	if active != 0 {
		return errors.Join(sandbox.ErrLaunch, exec.ErrWaitDelay)
	}
	return nil
}
func exitSignal(any) sandbox.Signal { return "" }
