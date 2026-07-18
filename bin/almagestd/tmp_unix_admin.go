package main

import (
	"fmt"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
)

// resolveTmpUnixAdminArg converts --tmp-unix-admin into the ordinary --unix
// override path used by protected config merging. The generated path must be
// selected before Merge so it replaces config defaults without adding a
// persistent protected-config field for this runtime-only convenience.
func resolveTmpUnixAdminArg(a *args.Args) error {
	if a == nil || !a.TmpUnixAdmin {
		return nil
	}
	if a.UnixAdmin != nil {
		return fmt.Errorf("--tmp-unix-admin cannot be combined with --unix")
	}

	socketPath, err := platform.TmpAdminUnixSocketPath()
	if err != nil {
		return err
	}
	a.UnixAdmin = &socketPath
	return nil
}
