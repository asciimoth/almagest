package main

import (
	"fmt"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

// resolveCLIPathArgs makes path-valued CLI arguments absolute before the daemon
// changes its working directory to file storage. Without this step, relative
// config, snapshot, or socket paths would be interpreted relative to storage
// instead of the directory where almagestd was started.
func resolveCLIPathArgs(a *args.Args) error {
	if a == nil {
		return nil
	}

	var err error
	if a.Config, err = absCLIPath("config", a.Config); err != nil {
		return err
	}
	if a.ProtectedConfig, err = absCLIPath("protected config", a.ProtectedConfig); err != nil {
		return err
	}
	if a.FileStorage, err = absCLIPath("file storage", a.FileStorage); err != nil {
		return err
	}
	if a.Snapshot != "-" {
		if a.Snapshot, err = absCLIPath("snapshot archive", a.Snapshot); err != nil {
			return err
		}
	}
	if a.UnixAdmin != nil && *a.UnixAdmin != "" {
		unixAdmin, err := absCLIPath("unix admin socket", *a.UnixAdmin)
		if err != nil {
			return err
		}
		*a.UnixAdmin = unixAdmin
	}

	return nil
}

func absCLIPath(label, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	out, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%s path %q: %w", label, value, err)
	}
	return out, nil
}
