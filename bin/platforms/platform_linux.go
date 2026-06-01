//go:build linux

package platforms

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
)

//go:embed default_linux_config.hujson
var DefaultConfigText string

//go:embed default_linux_protected_config.hujson
var DefaultProtectedConfigText string

func DefaultConfig() string {
	return "/etc/almagest/alm.hujson"
}

// IsDir reports if path is a dir.
// If path not exists, reports false.
func IsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else {
			return false, err
		}
	}
	return info.IsDir(), nil
}

// MakeSureDirExists create dir with all it's parent dirs if not exists.
// When creating, it use most strict access rights meningfull for dirs.
func MakeSureDirExists(path string) error {
	return os.MkdirAll(path, 0700)
}

// MakeSureFileExists create fil with all it's parent dirs if not exists.
// When creating, it use most strict access rights meningfull for dirs/file.
// When creating file, fills it with initial.
func MakeSureFileExists(path string, initial []byte) error {
	// Ensure parent directory exists with strict permissions
	dir := filepath.Dir(path)
	if err := MakeSureDirExists(dir); err != nil {
		return err
	}

	// Check if file already exists
	if _, err := os.Stat(path); err == nil {
		// File exists: do nothing, leave it unchanged
		return nil
	} else if !os.IsNotExist(err) {
		// Some other error occurred
		return err
	}

	// File does not exist: create it with strict permissions and write initial content
	// O_EXCL ensures we only write if file truly didn't exist (atomic check+create)
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		// File may have been created by another process between Stat and OpenFile;
		// if it now exists, that's fine - just return success without writing.
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer f.Close() //nolint

	// Write initial content only for newly created file
	if len(initial) > 0 {
		if _, err := f.Write(initial); err != nil {
			return err
		}
	}

	return nil
}

func SystemIntegration(cfg configer.Config) (sysnet.SysBuilder, error) {
	panic(ErrPlatform.Error())
}
