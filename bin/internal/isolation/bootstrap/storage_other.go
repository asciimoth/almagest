//go:build !linux && !windows

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func migrationDirectoryIdentity(string) (string, error) {
	return "", fmt.Errorf("%w: migration identity is unsupported on this platform", ErrUnsafeLayout)
}

func validateStorageParent(path string) error {
	current := filepath.VolumeName(path) + string(filepath.Separator)
	for _, part := range strings.FieldsFunc(strings.TrimPrefix(path, current), func(r rune) bool { return r == '/' || r == '\\' }) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("storage parent is not a direct directory")
		}
	}
	return nil
}

func openMigrationSource(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("migration source is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, fmt.Errorf("migration source changed while it was opened")
	}
	return file, nil
}
