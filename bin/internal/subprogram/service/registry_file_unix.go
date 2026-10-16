//go:build unix

package service

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func replaceRegistryFile(source, target string) error {
	return os.Rename(source, target)
}

func registryFileHasAliases(_ *os.File, info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink != 1
}

func syncRegistryDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
