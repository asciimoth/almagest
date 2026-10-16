//go:build unix

package direct

import (
	"io/fs"
	"os"
	"syscall"
)

func hasMultipleLinks(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink != 1 && !info.IsDir()
}

func openedFileHasMultipleLinks(_ *os.File, info fs.FileInfo) bool { return hasMultipleLinks(info) }

func pathComponentRedirects(fs.FileInfo) bool { return false }
