package bootstrap

import "io"

// RuntimeStorageInput is copied into fixed reserved files in R's already
// provisioned private directory. Snapshot is opaque and optional. The caller
// retains it. No path or file name comes from regular or protected config.
type RuntimeStorageInput struct {
	Regular   []byte
	Protected RuntimeProtected
	// TLSPlatform is the validated, serialized broker-owned snapshot. It has
	// literal data only and no host source selectors.
	TLSPlatform []byte
	Snapshot    io.Reader
}
