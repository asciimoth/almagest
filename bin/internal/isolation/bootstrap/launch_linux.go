//go:build linux

package bootstrap

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func validateWorkingDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: %v", errWorkingDirectory, err)
	}
	defer unix.Close(fd) //nolint:errcheck
	if err := checkTrustedFD(fd, unix.S_IFDIR, path); err != nil {
		return fmt.Errorf("%w: %v", errWorkingDirectory, err)
	}
	return nil
}
