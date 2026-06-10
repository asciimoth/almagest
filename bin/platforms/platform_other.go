//go:build !linux

package platforms

import (
	"os"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
)

var DefaultConfigText string = "{}"

var DefaultProtectedConfigText = "{}"

func DefaultConfig() string {
	panic(ErrPlatform.Error())
}

// MakeSureDirExists create dir with all it's parent dirs if not exists.
// When creating, it use most strict access rights meningfull for dirs.
func MakeSureDirExists(path string) error {
	panic(ErrPlatform.Error())
}

// MakeSureFileExists create fil with all it's parent dirs if not exists.
// When creating, it use most strict access rights meningfull for dirs/file.
// When creating file, fills it with initial.
func MakeSureSafeFileExists(path string, initial []byte) error {
	panic(ErrPlatform.Error())
}

// OpenSafeFile opens path only if the file and all parent directories are safe.
//
// Use this every time the config is loaded or reloaded.
func OpenSafeFile(path string) (*os.File, error) {
	panic(ErrPlatform.Error())
}

// ReadSafeFile safely opens and reads a config file.
//
// This should be used for every config reload. The file is opened, checked, and
// read through the same file descriptor.
func ReadSafeFile(path string) ([]byte, error) {
	panic(ErrPlatform.Error())
}

// IsDir reports if path is a dir.
// If path not exists, reports false.
func IsDir(path string) (bool, error) {
	panic(ErrPlatform.Error())
}

func SystemIntegration(cfg configer.Config) (sysnet.SysBuilder, error) {
	panic(ErrPlatform.Error())
}

func DropPrivileges() {
	panic(ErrPlatform.Error())
}
