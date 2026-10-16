//go:build !linux

package direct

func helperLinuxSecurityResult() string { return "unsupported" }

func helperControllerSignalResult() string { return "unsupported" }
