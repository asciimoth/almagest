package platform

import (
	"io"
	"os"

	"github.com/kataras/golog"
)

type Agent interface {
	// ConfRO reports whether config is read-only.
	ConfRO() bool

	// ReadConfig reads config and protected config
	ReadConfig() (cfg, prt []byte, err error)

	// WriteConfig writes cfg to config file.
	WriteConfig(cfg []byte) error
}

// ReadStdinIfPiped returns fallback immediately if stdin is a terminal.
// Otherwise, reads all piped/file input until EOF.
func readStdinIfPiped(fallback []byte) ([]byte, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}

	if stat.Mode()&os.ModeCharDevice != 0 {
		return fallback, nil
	}

	// Stdin is a pipe or redirected file → read until EOF
	return io.ReadAll(os.Stdin)
}

func DropPrivileges() {
	golog.Warn("Privileges dropping not yet implemented for this platform")
}
