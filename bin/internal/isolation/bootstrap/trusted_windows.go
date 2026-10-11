//go:build windows

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTrustKind uint8

const (
	windowsTrustParent windowsTrustKind = iota
	windowsTrustDirectory
	windowsTrustFile
)

func validateWindowsTrustedPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || containsParentReference(path) || strings.HasPrefix(path, `\\`) {
		return "", fmt.Errorf("%w: trusted path must be an absolute local path: %q", ErrUnsafeInput, path)
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return "", fmt.Errorf("%w: trusted path must use a local drive", ErrUnsafeInput)
	}
	for _, part := range strings.FieldsFunc(strings.TrimPrefix(path, volume), windowsPathSeparator) {
		if strings.ContainsRune(part, ':') {
			return "", fmt.Errorf("%w: alternate data streams are not permitted", ErrUnsafeInput)
		}
	}
	return filepath.Clean(path), nil
}

func windowsPathSeparator(r rune) bool { return r == '/' || r == '\\' }

func containsParentReference(path string) bool {
	for _, part := range strings.FieldsFunc(path, windowsPathSeparator) {
		if part == ".." {
			return true
		}
	}
	return false
}

func openWindowsTrustedPath(path string, directory bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	return windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, flags, 0)
}

func currentWindowsSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func trustedWindowsSID(sid, user *windows.SID) bool {
	return sid != nil && (sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

func checkWindowsTrustedHandle(handle windows.Handle, label string, kind windowsTrustKind) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s is a reparse point", ErrUnsafeInput, label)
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if kind == windowsTrustFile && (isDirectory || info.NumberOfLinks != 1) {
		return fmt.Errorf("%w: %s is not a single-link regular file", ErrUnsafeInput, label)
	}
	if kind != windowsTrustFile && !isDirectory {
		return fmt.Errorf("%w: %s is not a directory", ErrUnsafeInput, label)
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	user, err := currentWindowsSID()
	if err != nil {
		return err
	}
	if !trustedWindowsSID(owner, user) {
		return fmt.Errorf("%w: %s has an untrusted owner", ErrUnsafeInput, label)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%w: %s has no bounded DACL", ErrUnsafeInput, label)
	}
	const fileDeleteChild windows.ACCESS_MASK = 0x00000040
	parentDangerous := windows.ACCESS_MASK(fileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL)
	objectDangerous := parentDangerous | windows.FILE_GENERIC_WRITE | windows.GENERIC_WRITE
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			if ace.Header.AceType == 4 || ace.Header.AceType == 5 || ace.Header.AceType == 9 || ace.Header.AceType == 11 {
				return fmt.Errorf("%w: %s has an unsupported allow ACE", ErrUnsafeInput, label)
			}
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if trustedWindowsSID(sid, user) {
			continue
		}
		dangerous := objectDangerous
		if kind == windowsTrustParent {
			dangerous = parentDangerous
		}
		if ace.Mask&dangerous != 0 {
			return fmt.Errorf("%w: %s is replaceable by an untrusted identity", ErrUnsafeInput, label)
		}
	}
	return nil
}

func validateWindowsTrustedDirectory(path string, finalKind windowsTrustKind) error {
	clean, err := validateWindowsTrustedPath(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(clean)
	current := volume + `\`
	parts := strings.FieldsFunc(strings.TrimPrefix(clean, current), windowsPathSeparator)
	for index, part := range parts {
		current = filepath.Join(current, part)
		handle, err := openWindowsTrustedPath(current, true)
		if err != nil {
			return err
		}
		kind := windowsTrustParent
		if index == len(parts)-1 {
			kind = finalKind
		}
		err = checkWindowsTrustedHandle(handle, current, kind)
		_ = windows.CloseHandle(handle)
		if err != nil {
			return err
		}
	}
	return nil
}

func openTrustedFile(path string) (*os.File, error) {
	clean, err := validateWindowsTrustedPath(path)
	if err != nil {
		return nil, err
	}
	if err := validateWindowsTrustedDirectory(filepath.Dir(clean), windowsTrustParent); err != nil {
		return nil, err
	}
	handle, err := openWindowsTrustedPath(clean, false)
	if err != nil {
		return nil, err
	}
	if err := checkWindowsTrustedHandle(handle, clean, windowsTrustFile); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), clean), nil
}

func openUntrustedFile(path string) (*os.File, error) {
	clean, err := validateWindowsTrustedPath(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(clean)
	current := volume + `\`
	parts := strings.FieldsFunc(strings.TrimPrefix(clean, current), windowsPathSeparator)
	for index, part := range parts {
		current = filepath.Join(current, part)
		handle, err := openWindowsTrustedPath(current, index < len(parts)-1)
		if err != nil {
			return nil, err
		}
		var info windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(handle, &info)
		if index < len(parts)-1 {
			_ = windows.CloseHandle(handle)
		}
		if infoErr != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
			(index < len(parts)-1) != (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) {
			if index == len(parts)-1 {
				_ = windows.CloseHandle(handle)
			}
			return nil, fmt.Errorf("%w: untrusted input has an unsafe type", ErrUnsafeInput)
		}
		if index == len(parts)-1 {
			return os.NewFile(uintptr(handle), clean), nil
		}
	}
	return nil, fmt.Errorf("%w: untrusted input is not a file", ErrInvalidInput)
}
