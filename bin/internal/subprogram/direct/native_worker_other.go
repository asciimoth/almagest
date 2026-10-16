//go:build !linux || !amd64

package direct

import "github.com/asciimoth/almagest/lib/sandbox"

func (s *instance) startNativeWorker() error { return sandbox.ErrUnavailable }
func (s *instance) closeNativeWorker() error {
	// Keep the cross-platform instance layout visible to platform lint. These
	// values are never populated when the Linux full backend is unavailable.
	_ = s.nativeWorker
	_ = s.nativeWorkerDone
	_ = s.nativeWorkerWaitErr
	_ = s.nativeWorkerLife
	_ = s.nativeMountName
	return nil
}
