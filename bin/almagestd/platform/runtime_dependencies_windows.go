//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

const windowsWintunVersion = "0.14.1"

// RuntimeDependencyReport describes native dependencies that the daemon has
// validated without changing host state.
type RuntimeDependencyReport struct {
	WintunPath    string
	WintunVersion string
}

// ValidateRuntimeDependencies loads Wintun only from the application directory
// or System32 and validates the loaded file. It does not create an adapter or
// install, start, stop, register, reset, or replace a driver or service.
func ValidateRuntimeDependencies() (RuntimeDependencyReport, error) {
	executable, err := os.Executable()
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency: locate daemon executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency: resolve daemon executable: %w", err)
	}
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency: locate System32: %w", err)
	}
	return validateWindowsWintun(
		filepath.Join(filepath.Dir(executable), "wintun.dll"),
		filepath.Join(systemDirectory, "wintun.dll"),
	)
}

func validateWindowsWintun(applicationPath, systemPath string) (RuntimeDependencyReport, error) {
	path := applicationPath
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency at %q: %w", path, err)
		}
		path = systemPath
		if _, err := os.Stat(path); err != nil {
			return RuntimeDependencyReport{}, fmt.Errorf(
				"validate Wintun dependency: architecture-matched wintun.dll %s is absent from the daemon executable directory and System32: %w",
				windowsWintunVersion,
				err,
			)
		}
	}

	// DLL_LOAD_DIR resolves dependencies beside an absolute DLL without adding
	// the process current directory. SYSTEM32 permits only Windows dependencies.
	handle, err := windows.LoadLibraryEx(
		path,
		0,
		windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32,
	)
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf(
			"validate Wintun dependency at %q: load the architecture-matched wintun.dll %s: %w",
			path,
			windowsWintunVersion,
			err,
		)
	}
	defer windows.FreeLibrary(handle) //nolint:errcheck // The validation handle owns no mutable state.

	loadedPath, err := windowsModulePath(handle)
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency: read loaded module path: %w", err)
	}
	trustedPath, err := sameWindowsPath(path, loadedPath)
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency: compare loaded module path: %w", err)
	}
	if !trustedPath {
		return RuntimeDependencyReport{}, fmt.Errorf(
			"validate Wintun dependency: loaded module %q does not match trusted path %q",
			loadedPath,
			path,
		)
	}
	version, err := windowsFileVersion(loadedPath)
	if err != nil {
		return RuntimeDependencyReport{}, fmt.Errorf("validate Wintun dependency at %q: read version: %w", loadedPath, err)
	}
	if version != windowsWintunVersion {
		return RuntimeDependencyReport{}, fmt.Errorf(
			"validate Wintun dependency at %q: version is %s, want %s",
			loadedPath,
			version,
			windowsWintunVersion,
		)
	}
	// Exercise the exact binding used by sysnet-windows while the absolute
	// trusted module is loaded. The binding uses application-directory and
	// System32 search flags and retains its own module reference.
	if bindingVersion := wintun.Version(); bindingVersion != version {
		return RuntimeDependencyReport{}, fmt.Errorf(
			"validate Wintun dependency at %q: backend binding reports version %s, want %s",
			loadedPath,
			bindingVersion,
			version,
		)
	}
	return RuntimeDependencyReport{WintunPath: loadedPath, WintunVersion: version}, nil
}

func windowsModulePath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_PATH)
	for {
		length, err := windows.GetModuleFileName(handle, &buffer[0], uint32(len(buffer)))
		if err == nil && length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) && length < uint32(len(buffer)) {
			return "", err
		}
		buffer = make([]uint16, len(buffer)*2)
	}
}

func sameWindowsPath(first, second string) (bool, error) {
	first, err := filepath.Abs(first)
	if err != nil {
		return false, err
	}
	second, err = filepath.Abs(second)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second)), nil
}

func windowsFileVersion(path string) (string, error) {
	var ignored windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &ignored)
	if err != nil {
		return "", err
	}
	if size == 0 {
		return "", errors.New("version resource is empty")
	}
	buffer := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buffer[0])); err != nil {
		return "", err
	}
	var info *windows.VS_FIXEDFILEINFO
	var infoSize uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&info), &infoSize); err != nil {
		return "", err
	}
	if info == nil || infoSize < uint32(unsafe.Sizeof(*info)) {
		return "", errors.New("fixed version information is truncated")
	}
	parts := []uint32{info.FileVersionMS >> 16, info.FileVersionMS & 0xffff, info.FileVersionLS >> 16, info.FileVersionLS & 0xffff}
	last := len(parts) - 1
	for last > 1 && parts[last] == 0 {
		last--
	}
	values := make([]string, last+1)
	for index := range values {
		values[index] = fmt.Sprint(parts[index])
	}
	return strings.Join(values, "."), nil
}
