//go:build windows

package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateProcessConfig(cfg resolvedCommand) error {
	if cfg.UID == nil && cfg.GID == nil {
		return nil
	}
	return errors.New("uid/gid command credentials are unsupported on Windows")
}

func executeCommand(ctx context.Context, cfg resolvedCommand, env []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create command Job Object: %w", err)
	}
	defer func() { _ = windows.CloseHandle(job) }()

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		return fmt.Errorf("configure command Job Object: %w", err)
	}

	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED,
		HideWindow:    true,
	}
	if err = cmd.Start(); err != nil {
		return err
	}

	failStart := func(startErr error) error {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return startErr
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return failStart(fmt.Errorf("open suspended command process: %w", err))
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return failStart(fmt.Errorf("assign command process to Job Object: %w", err))
	}

	// Check cancellation after assignment and before resume. Thus the child is
	// either not started or is already tracked when cancellation can act on it.
	if err = ctx.Err(); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = cmd.Wait()
		return err
	}
	if err = resumeCommandProcess(uint32(cmd.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = cmd.Wait()
		return fmt.Errorf("resume command process: %w", err)
	}

	waitDone := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = windows.TerminateJobObject(job, 1)
		case <-waitDone:
		}
	}()
	err = cmd.Wait()
	close(waitDone)
	<-watchDone
	return err
}

func resumeCommandProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err = windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	resumed := false
	for {
		if entry.OwnerProcessID == pid {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if err = errors.Join(resumeErr, closeErr); err != nil {
				return err
			}
			resumed = true
		}
		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return err
		}
	}
	if !resumed {
		return os.ErrProcessDone
	}
	return nil
}
