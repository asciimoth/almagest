//go:build windows

package configwriter

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/durablefile"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/windows"
)

type fileToken struct {
	volume uint32
	index  uint64
}

type windowsTarget struct {
	directory *os.File
	path      string
	mode      os.FileMode
	volume    uint32
	security  [32]byte
}

func openTarget(path string) (fixedTarget, error) {
	clean, err := validateWindowsPath(path)
	if err != nil {
		return nil, err
	}
	if err := checkWindowsComponents(clean); err != nil {
		return nil, err
	}
	parent := filepath.Dir(clean)
	handle, info, err := openWindowsObject(parent, true)
	if err != nil {
		return nil, fmt.Errorf("open fixed config directory: %w", err)
	}
	directory := os.NewFile(uintptr(handle), parent)
	if _, err := windowsSecurityHash(handle); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("validate fixed config directory ACL: %w", err)
	}
	t := &windowsTarget{directory: directory, path: clean, volume: info.VolumeSerialNumber}
	f, targetInfo, err := t.openChecked(false)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = directory.Close()
		return nil, err
	}
	t.mode = stat.Mode().Perm()
	t.security, err = windowsSecurityHash(windows.Handle(f.Fd()))
	if err != nil {
		_ = f.Close()
		_ = directory.Close()
		return nil, fmt.Errorf("validate fixed config ACL: %w", err)
	}
	_ = f.Close()
	if targetInfo.VolumeSerialNumber != t.volume {
		_ = directory.Close()
		return nil, fmt.Errorf("fixed config target is on another volume")
	}
	return t, nil
}

func validateWindowsPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return "", fmt.Errorf("config target must be a local absolute drive path")
	}
	clean := filepath.Clean(path)
	if clean != path || len(clean) < 3 || clean[1] != ':' || strings.Contains(clean[2:], ":") {
		return "", fmt.Errorf("config target path is not canonical")
	}
	for _, component := range strings.Split(clean[3:], `\`) {
		trimmed := strings.TrimRight(component, " .")
		if component == "" || trimmed != component || windowsReservedName(component) {
			return "", fmt.Errorf("config target contains an unsafe name")
		}
	}
	return clean, nil
}

func windowsReservedName(name string) bool {
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func checkWindowsComponents(path string) error {
	volume := filepath.VolumeName(path) + `\`
	current := volume
	parts := strings.Split(strings.TrimPrefix(path, volume), `\`)
	for i, part := range parts {
		current = filepath.Join(current, part)
		handle, info, err := openWindowsObject(current, i != len(parts)-1)
		if err != nil {
			return err
		}
		_ = windows.CloseHandle(handle)
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("config target path contains a reparse point")
		}
	}
	return nil
}

func openWindowsObject(path string, directory bool) (windows.Handle, windows.ByHandleFileInformation, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, info, err
	}
	if directory != (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return 0, info, fmt.Errorf("fixed config object has an unsafe type")
	}
	return handle, info, nil
}

func (t *windowsTarget) openChecked(enforceMetadata bool) (*os.File, windows.ByHandleFileInformation, error) {
	handle, info, err := openWindowsObject(t.path, false)
	if err != nil {
		return nil, info, fmt.Errorf("open fixed config target: %w", err)
	}
	if info.NumberOfLinks != 1 || info.VolumeSerialNumber != t.volume {
		_ = windows.CloseHandle(handle)
		return nil, info, fmt.Errorf("fixed config target is not a single-link file on the approved volume")
	}
	f := os.NewFile(uintptr(handle), t.path)
	security, err := windowsSecurityHash(handle)
	if err != nil {
		_ = f.Close()
		return nil, info, err
	}
	if enforceMetadata {
		stat, err := f.Stat()
		if err != nil || stat.Mode().Perm() != t.mode || security != t.security {
			_ = f.Close()
			if err != nil {
				return nil, info, err
			}
			return nil, info, fmt.Errorf("fixed config target metadata changed")
		}
	}
	return f, info, nil
}

func windowsSecurityHash(handle windows.Handle) ([32]byte, error) {
	var zero [32]byte
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return zero, err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return zero, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return zero, err
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	if !trusted(owner) {
		return zero, fmt.Errorf("config object owner is not trusted")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return zero, fmt.Errorf("config object has no bounded DACL")
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return zero, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trusted(sid) {
			return zero, fmt.Errorf("config object grants access to an untrusted SID")
		}
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(descriptor)), descriptor.Length())
	return sha256.Sum256(data), nil
}

func windowsToken(info windows.ByHandleFileInformation) fileToken {
	return fileToken{info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}
}

func (t *windowsTarget) read(limit int) (targetSnapshot, error) {
	f, info, err := t.openChecked(true)
	if err != nil {
		return targetSnapshot{}, err
	}
	defer f.Close() //nolint:errcheck
	stat, err := f.Stat()
	if err != nil {
		return targetSnapshot{}, err
	}
	if stat.Size() < 0 || stat.Size() > int64(limit) {
		return targetSnapshot{}, protocol.ErrLimitExceeded
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return targetSnapshot{}, err
	}
	if len(data) > limit {
		return targetSnapshot{}, protocol.ErrLimitExceeded
	}
	return targetSnapshot{data: data, token: windowsToken(info)}, nil
}

func (t *windowsTarget) replace(ctx context.Context, expected fileToken, data []byte) (bool, error) {
	f, info, err := t.openChecked(true)
	if err != nil {
		return false, err
	}
	_ = f.Close()
	if windowsToken(info) != expected {
		return false, protocol.ErrOperationConflict
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(t.path), ".almagest-config-*.tmp")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err := temporary.Chmod(t.mode); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := writeAll(temporary, data); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f, info, err = t.openChecked(true)
	if err != nil {
		return false, err
	}
	_ = f.Close()
	if windowsToken(info) != expected {
		return false, protocol.ErrOperationConflict
	}
	if err := durablefile.Replace(t.path, temporaryPath); err != nil {
		return false, err
	}
	f, _, err = t.openChecked(true)
	if err != nil {
		return true, err
	}
	err = f.Sync()
	_ = f.Close()
	if err != nil {
		return true, err
	}
	return true, nil
}

func (t *windowsTarget) close() error { return t.directory.Close() }
