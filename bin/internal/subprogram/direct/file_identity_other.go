//go:build !unix && !windows

package direct

import (
	"io/fs"
	"os"
)

func hasMultipleLinks(fs.FileInfo) bool                     { return true }
func openedFileHasMultipleLinks(*os.File, fs.FileInfo) bool { return true }
func pathComponentRedirects(fs.FileInfo) bool               { return false }
