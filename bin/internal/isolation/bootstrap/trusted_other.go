//go:build !linux && !windows

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func openTrustedFile(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || containsParentReference(path) {
		return nil, fmt.Errorf("%w: trusted path must be absolute and clean: %q", ErrUnsafeInput, path)
	}
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(clean, current)
	parts := strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' })
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: trusted path contains a link: %s", ErrUnsafeInput, current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%w: trusted parent is not a directory: %s", ErrUnsafeInput, current)
		}
	}
	file, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: trusted input is not a regular file", ErrUnsafeInput)
	}
	return file, nil
}

func openUntrustedFile(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || containsParentReference(path) {
		return nil, fmt.Errorf("%w: untrusted input path must be absolute and clean", ErrInvalidInput)
	}
	current := filepath.VolumeName(path) + string(filepath.Separator)
	parts := strings.FieldsFunc(strings.TrimPrefix(path, current), func(r rune) bool { return r == '/' || r == '\\' })
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: untrusted input contains a link", ErrUnsafeInput)
		}
	}
	return os.Open(path)
}

func containsParentReference(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}
