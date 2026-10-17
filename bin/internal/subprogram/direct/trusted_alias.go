package direct

import (
	"io/fs"
	"os"
	"runtime"
)

// A read-only Unix image can safely have another name. This is required for
// immutable content-addressed stores such as /nix/store, which can hard-link
// identical files. Writable files and Windows objects retain the stricter
// single-name rule.
func unsafeTrustedAlias(file *os.File, info fs.FileInfo) bool {
	return openedFileHasMultipleLinks(file, info) && (runtime.GOOS == "windows" || info.Mode().Perm()&0o222 != 0)
}
