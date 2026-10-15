//go:build !linux

package main

import "os"

func chmodDirectoryNoFollow(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
