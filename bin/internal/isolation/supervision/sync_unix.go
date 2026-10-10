//go:build !windows

package supervision

import "os"

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	return directory.Sync()
}
