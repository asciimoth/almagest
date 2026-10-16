//go:build unix

package direct

import (
	"io/fs"
	"os"
	"syscall"
)

func safeOpen(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func safeRootOpen(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func safeRootOpenFile(root *os.Root, name string, flag int, mode fs.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag|syscall.O_NONBLOCK, mode)
}
