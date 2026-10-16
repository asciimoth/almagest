//go:build !unix

package direct

import (
	"io/fs"
	"os"
)

func safeOpen(name string) (*os.File, error) { return os.Open(name) }
func safeRootOpen(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
func safeRootOpenFile(root *os.Root, name string, flag int, mode fs.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag, mode)
}
