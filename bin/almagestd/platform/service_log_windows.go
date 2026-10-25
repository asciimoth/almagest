//go:build windows

package platform

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const windowsServiceLogName = "almagestd.log"

// OpenServiceLog opens the private rotating log only for an SCM service.
func OpenServiceLog() (io.WriteCloser, bool, error) {
	service, err := windowsServiceStatus()
	if err != nil {
		return nil, false, fmt.Errorf("detect Windows service execution: %w", err)
	}
	if !service {
		return nil, false, nil
	}
	programData, err := windowsKnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return nil, true, fmt.Errorf("resolve ProgramData for service log: %w", err)
	}
	logDirectory := filepath.Join(programData, windowsApplicationDirectory, "logs")
	if err := makeSurePrivateWindowsDirExists(logDirectory); err != nil {
		return nil, true, fmt.Errorf("prepare service log directory: %w", err)
	}
	path := filepath.Join(logDirectory, windowsServiceLogName)
	writer, err := newRotatingLogFile(path, WindowsServiceLogMaxBytes, WindowsServiceLogBackups, openPrivateWindowsAppendFile)
	if err != nil {
		return nil, true, fmt.Errorf("open service log: %w", err)
	}
	return writer, true, nil
}

func openPrivateWindowsAppendFile(path string) (*os.File, error) {
	clean, err := validateWindowsPath(path)
	if err != nil {
		return nil, err
	}
	attributes, err := privateWindowsSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE|windows.READ_CONTROL,
		windowsAllShares, attributes, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), clean)
	fail := func(cause error) (*os.File, error) {
		_ = file.Close()
		return nil, cause
	}
	if err := checkWindowsACL(handle, clean, windowsPathPrivateFile); err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fail(err)
	}
	return file, nil
}
