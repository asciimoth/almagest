//go:build !windows

package tlsplatform

// SystemRevocationChecker is unavailable off Windows. A Windows-specific
// protected setting must not silently select another trust engine.
func SystemRevocationChecker() RevocationChecker { return nil }
