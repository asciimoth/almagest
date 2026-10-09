//go:build !linux && !windows

package bootstrap

import (
	"fmt"
	"os"
)

func validateWorkingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %v", errWorkingDirectory, err)
	}
	return nil
}
