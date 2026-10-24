//go:build windows

package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/snapshotlimits"
	"golang.org/x/sys/windows"
)

// PrepareRuntimeStorage replaces only R's fixed startup files in a deployment
// directory whose AppContainer ACL was already verified by windowsprofile.
// Windows has no UID/GID ownership inputs, so both identity numbers must be
// zero. The held directory stays open for the complete transaction.
func PrepareRuntimeStorage(directory *os.File, uid, gid uint32, input RuntimeStorageInput) error {
	if directory == nil || uid != 0 || gid != 0 || len(input.Regular) > protocol.MaxConfigBytes || len(input.TLSPlatform) > MaxImmutableInputBytes {
		return fmt.Errorf("%w: invalid runtime storage input", ErrInvalidInput)
	}
	root, err := validateRuntimeStorageDirectory(directory)
	if err != nil {
		return err
	}
	protected, err := input.Protected.RuntimeDocument()
	if err != nil {
		return fmt.Errorf("prepare runtime protected input: %w", err)
	}
	values := []struct {
		name   string
		reader io.Reader
		limit  int64
	}{
		{roleentry.RuntimeRegularConfigFile, bytes.NewReader(input.Regular), protocol.MaxConfigBytes},
		{roleentry.RuntimeProtectedConfigFile, bytes.NewReader(protected), MaxProtectedInputBytes},
	}
	if len(input.TLSPlatform) != 0 {
		values = append(values, struct {
			name   string
			reader io.Reader
			limit  int64
		}{roleentry.RuntimeTLSPlatformFile, bytes.NewReader(input.TLSPlatform), MaxImmutableInputBytes})
	}
	if input.Snapshot != nil {
		values = append(values, struct {
			name   string
			reader io.Reader
			limit  int64
		}{roleentry.RuntimeSnapshotFile, input.Snapshot, snapshotlimits.ArchiveBytes})
	}
	staged := make([]string, 0, len(values))
	defer func() {
		for _, path := range staged {
			if path != "" {
				_ = os.Remove(path)
			}
		}
	}()
	for _, value := range values {
		stage := filepath.Join(root, value.name+".bootstrap-new")
		if err := removeWindowsReserved(stage); err != nil {
			return err
		}
		if err := writeWindowsRuntimeFile(stage, value.reader, value.limit); err != nil {
			return fmt.Errorf("stage %s: %w", value.name, err)
		}
		staged = append(staged, stage)
	}
	for index, value := range values {
		target := filepath.Join(root, value.name)
		if err := replaceWindowsRuntimeFile(staged[index], target); err != nil {
			return fmt.Errorf("install %s: %w", value.name, err)
		}
		staged[index] = ""
	}
	if input.Snapshot == nil {
		if err := removeWindowsReserved(filepath.Join(root, roleentry.RuntimeSnapshotFile)); err != nil {
			return err
		}
	}
	if len(input.TLSPlatform) == 0 {
		if err := removeWindowsReserved(filepath.Join(root, roleentry.RuntimeTLSPlatformFile)); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeStorageDirectory(directory *os.File) (string, error) {
	path, err := validateWindowsTrustedPath(directory.Name())
	if err != nil {
		return "", err
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: runtime storage is not a directory", ErrUnsafeInput)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", fmt.Errorf("%w: runtime storage is unsafe", ErrUnsafeInput)
	}
	return path, nil
}

func removeWindowsReserved(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return fmt.Errorf("%w: reserved runtime input is a directory", ErrUnsafeInput)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeWindowsRuntimeFile(path string, reader io.Reader, limit int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(file, io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return ErrInputTooLarge
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	failed = false
	return nil
}

func replaceWindowsRuntimeFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
