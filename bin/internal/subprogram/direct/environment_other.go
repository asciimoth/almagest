//go:build !windows

package direct

func platformEnvironment() map[string]string { return nil }
func platformProgramPath() string            { return "/usr/bin:/bin" }
