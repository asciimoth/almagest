//go:build !unix

package direct

func supportsUnixSockets() bool { return false }
