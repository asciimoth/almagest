//go:build unix

package direct

// Pathname Unix sockets require a worker in the child's confined filesystem
// view. Host-path resolution cannot safely race an untrusted child.
func supportsUnixSockets() bool { return false }
