//go:build !linux

package localizedcontent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func openRelative(root, relative string) (*os.File, error) {
	path, err := safePath(root, relative)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func safePath(root, relative string) (string, error) {
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink is not permitted")
		}
	}
	return current, nil
}
