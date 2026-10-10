//go:build windows

package roleentry

// Windows applies the AppContainer token, job and mitigations before resume.
func SealConfigWriter() error { return nil }
