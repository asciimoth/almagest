//go:build !linux

package platforms

import (
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
func MakeSureFileExists(path string, initial []byte) error {
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
