//go:build !unix && !windows

package service

import (
	"io/fs"
	"os"
)

func registryFileHasAliases(*os.File, fs.FileInfo) bool { return true }

func replaceRegistryFile(source, target string) error { return os.Rename(source, target) }
func syncRegistryDirectory(string) error              { return nil }
