//go:build windows

package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsAllShares = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

type windowsPathKind uint8

const (
	windowsPathParent windowsPathKind = iota
	windowsPathPrivateDirectory
	windowsPathPrivateFile
)

// validateWindowsPath rejects path forms that can select a stream or a device
// instead of the named filesystem object. Safe daemon state must be local.
func validateWindowsPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %q", path)
	}
	if strings.HasPrefix(path, `\\`) {
		return "", fmt.Errorf("UNC and device paths are not permitted for private state: %q", path)
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return "", fmt.Errorf("path must use a local drive: %q", path)
	}
	remainder := strings.TrimPrefix(path, volume)
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool { return r == '\\' || r == '/' }) {
		if component == ".." {
			return "", fmt.Errorf("path must not contain '..': %q", path)
		}
		if strings.ContainsRune(component, ':') {
			return "", fmt.Errorf("alternate data streams are not permitted: %q", path)
		}
	}
	return filepath.Clean(path), nil
}

func currentWindowsUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func privateWindowsSecurityAttributes(directory bool) (*windows.SecurityAttributes, error) {
	user, err := currentWindowsUserSID()
	if err != nil {
		return nil, fmt.Errorf("get current Windows identity: %w", err)
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf("O:%sD:P(A;%s;FA;;;SY)(A;%s;FA;;;BA)(A;%s;FA;;;%s)",
		user.String(), inheritance, inheritance, inheritance, user.String())
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build private Windows ACL: %w", err)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, nil
}

func openWindowsPath(path string, access uint32, directory bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	return windows.CreateFile(name, access, windowsAllShares, nil, windows.OPEN_EXISTING, flags, 0)
}

func trustedWindowsSID(sid, user *windows.SID) bool {
	return sid != nil && (sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

func checkWindowsACL(handle windows.Handle, label string, kind windowsPathKind) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("%s: get file information: %w", label, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s: reparse points are not permitted", label)
	}
	if kind == windowsPathPrivateFile {
		if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
			return fmt.Errorf("%s: expected a regular file", label)
		}
		if info.NumberOfLinks != 1 {
			return fmt.Errorf("%s: hard-linked private files are not permitted", label)
		}
	} else if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s: expected a directory", label)
	}

	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%s: read security descriptor: %w", label, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("%s: read owner: %w", label, err)
	}
	user, err := currentWindowsUserSID()
	if err != nil {
		return fmt.Errorf("%s: get current Windows identity: %w", label, err)
	}
	if !trustedWindowsSID(owner, user) {
		return fmt.Errorf("%s: owner %s is not trusted", label, owner)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("%s: DACL is missing: %w", label, err)
	}
	if dacl == nil {
		return fmt.Errorf("%s: null DACL grants unrestricted access", label)
	}

	const fileDeleteChild windows.ACCESS_MASK = 0x00000040
	parentDangerous := windows.ACCESS_MASK(windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
		windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL)
	parentDangerous |= fileDeleteChild
	privateChildren := kind != windowsPathPrivateDirectory
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return fmt.Errorf("%s: read DACL entry %d: %w", label, i, err)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		// Object and callback allow ACEs have a different layout. Reject them
		// instead of interpreting their object metadata as a SID.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			if ace.Header.AceType == 4 || ace.Header.AceType == 5 || ace.Header.AceType == 9 || ace.Header.AceType == 11 {
				return fmt.Errorf("%s: unsupported access-allow ACE type %d", label, ace.Header.AceType)
			}
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if trustedWindowsSID(sid, user) {
			if kind == windowsPathPrivateDirectory && sid.Equals(user) &&
				ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) ==
					windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE &&
				ace.Mask&windows.FILE_GENERIC_WRITE == windows.FILE_GENERIC_WRITE {
				privateChildren = true
			}
			continue
		}
		if kind != windowsPathParent || ace.Mask&parentDangerous != 0 {
			return fmt.Errorf("%s: access entry grants untrusted SID %s mask %#x", label, sid, ace.Mask)
		}
	}
	if !privateChildren {
		return fmt.Errorf("%s: DACL does not give the current identity inheritable private access", label)
	}
	return nil
}

func checkWindowsDirectory(path string, kind windowsPathKind) error {
	handle, err := openWindowsPath(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, true)
	if err != nil {
		return fmt.Errorf("%s: open directory without following reparse points: %w", path, err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	return checkWindowsACL(handle, path, kind)
}

// makeSurePrivateWindowsDirExists creates each missing directory with a
// protected inheritable ACL. Existing parents must not give an unrelated SID
// replacement rights, and the final directory must not grant unrelated access.
func makeSurePrivateWindowsDirExists(path string) error {
	return makeSureWindowsDirExists(path, windowsPathPrivateDirectory)
}

func makeSureWindowsConfigParentExists(path string) error {
	return makeSureWindowsDirExists(path, windowsPathParent)
}

func makeSureWindowsDirExists(path string, finalKind windowsPathKind) error {
	clean, err := validateWindowsPath(path)
	if err != nil {
		return err
	}
	attributes, err := privateWindowsSecurityAttributes(true)
	if err != nil {
		return err
	}
	current := filepath.VolumeName(clean) + `\`
	relative := strings.TrimPrefix(clean, current)
	parts := strings.FieldsFunc(relative, func(r rune) bool { return r == '\\' || r == '/' })
	for i, part := range parts {
		current = filepath.Join(current, part)
		kind := windowsPathParent
		if i == len(parts)-1 {
			kind = finalKind
		}
		err := checkWindowsDirectory(current, kind)
		if err == nil {
			continue
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return err
		}
		name, nameErr := windows.UTF16PtrFromString(current)
		if nameErr != nil {
			return nameErr
		}
		if createErr := windows.CreateDirectory(name, attributes); createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("%s: create private directory: %w", current, createErr)
		}
		if err := checkWindowsDirectory(current, kind); err != nil {
			return err
		}
	}
	return nil
}

func createPrivateWindowsFile(path string, initial []byte) error {
	clean, err := validateWindowsPath(path)
	if err != nil {
		return err
	}
	if err := makeSureWindowsConfigParentExists(filepath.Dir(clean)); err != nil {
		return err
	}
	attributes, err := privateWindowsSecurityAttributes(false)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
		windowsAllShares, attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), clean)
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(clean)
		}
	}()
	if err := checkWindowsACL(handle, clean, windowsPathPrivateFile); err != nil {
		return err
	}
	if _, err := file.Write(initial); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func createPrivateWindowsTempFile(directory, prefix string) (*os.File, error) {
	clean, err := validateWindowsPath(directory)
	if err != nil {
		return nil, err
	}
	if err := checkWindowsDirectory(clean, windowsPathParent); err != nil {
		return nil, err
	}
	attributes, err := privateWindowsSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	for range 100 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("generate private temporary file name: %w", err)
		}
		path := filepath.Join(clean, prefix+hex.EncodeToString(random[:]))
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		handle, err := windows.CreateFile(name,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
			windowsAllShares, attributes, windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := checkWindowsACL(handle, path, windowsPathPrivateFile); err != nil {
			_ = windows.CloseHandle(handle)
			_ = os.Remove(path)
			return nil, err
		}
		return os.NewFile(uintptr(handle), path), nil
	}
	return nil, fmt.Errorf("create private temporary file: too many name collisions")
}

func readPrivateWindowsFile(path string) ([]byte, error) {
	clean, err := validateWindowsPath(path)
	if err != nil {
		return nil, err
	}
	if err := checkWindowsDirectory(filepath.Dir(clean), windowsPathParent); err != nil {
		return nil, err
	}
	handle, err := openWindowsPath(clean, windows.GENERIC_READ|windows.READ_CONTROL, false)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), clean)
	defer file.Close() //nolint:errcheck
	if err := checkWindowsACL(handle, clean, windowsPathPrivateFile); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return data, nil
}
