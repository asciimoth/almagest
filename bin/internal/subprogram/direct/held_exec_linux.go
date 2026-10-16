//go:build linux

package direct

func heldExecutablePath() string { return "/proc/self/fd/3" }
