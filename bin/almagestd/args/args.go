// Package args keeps the daemon's historical import path. The command-line
// contract lives outside the runtime tree so minimal trusted bootstrap can use
// it without importing node or daemon code.
package args

import "github.com/asciimoth/almagest/bin/internal/daemonargs"

type Args = daemonargs.Args
