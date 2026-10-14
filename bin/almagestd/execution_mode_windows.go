//go:build windows

package main

// Windows isolation is not yet a released profile. Keep the supported direct
// daemon as the default; --no-isolation is an accepted explicit no-op.
const isolationDefault = false
