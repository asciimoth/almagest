//go:build !windows

package commandhelper

func validatePlatformEnvironment(map[string]string) error { return nil }
