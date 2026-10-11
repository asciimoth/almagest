//go:build windows

package bootstrap

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func validateStorageParent(path string) error {
	return validateWindowsTrustedDirectory(path, windowsTrustParent)
}

func openMigrationSource(path string) (*os.File, error) {
	clean, err := validateWindowsTrustedPath(path)
	if err != nil {
		return nil, err
	}
	handle, err := openWindowsTrustedPath(clean, false)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("migration source is not a single-link regular file")
	}
	return os.NewFile(uintptr(handle), clean), nil
}

func migrationDirectoryIdentity(path string) (string, error) {
	clean, err := validateWindowsTrustedPath(path)
	if err != nil {
		return "", err
	}
	handle, err := openWindowsTrustedPath(clean, true)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", ErrUnsafeLayout
	}
	return fmt.Sprintf("windows:%x:%x:%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
