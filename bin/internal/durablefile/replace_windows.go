//go:build windows

// Package durablefile provides same-volume durable file replacement.
package durablefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

var callReplaceFile = func(destination, replacement, backup *uint16) (uintptr, error) {
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(destination)),
		uintptr(unsafe.Pointer(replacement)),
		uintptr(unsafe.Pointer(backup)),
		0, 0, 0,
	)
	return result, callErr
}

// Replace atomically replaces destination with replacement and preserves the
// destination ACL. A private backup lets this function put the prior file back
// if ReplaceFileW reports one of its partial-move errors.
func Replace(destination, replacement string) error {
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return moveFile(replacement, destination, windows.MOVEFILE_WRITE_THROUGH)
	} else if err != nil {
		return err
	}

	backup, err := unusedBackupPath(filepath.Dir(destination), filepath.Base(destination))
	if err != nil {
		return err
	}
	destinationName, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	replacementName, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	backupName, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}

	result, callErr := callReplaceFile(destinationName, replacementName, backupName)
	if result != 0 {
		_ = os.Remove(backup)
		return nil
	}
	if callErr == nil || callErr == windows.ERROR_SUCCESS {
		callErr = windows.ERROR_GEN_FAILURE
	}
	if _, statErr := os.Lstat(destination); errors.Is(statErr, os.ErrNotExist) {
		if _, backupErr := os.Lstat(backup); backupErr == nil {
			if restoreErr := moveFile(backup, destination, windows.MOVEFILE_WRITE_THROUGH); restoreErr != nil {
				return fmt.Errorf("replace file: %w; restore prior file: %v", callErr, restoreErr)
			}
		}
	}
	_ = os.Remove(backup)
	return fmt.Errorf("replace file: %w", callErr)
}

func moveFile(source, destination string, flags uint32) error {
	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationName, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourceName, destinationName, flags)
}

func unusedBackupPath(directory, base string) (string, error) {
	file, err := os.CreateTemp(directory, "."+base+".backup-*")
	if err != nil {
		return "", fmt.Errorf("reserve replacement backup: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}
