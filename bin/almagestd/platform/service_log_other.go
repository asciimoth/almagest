//go:build !windows

package platform

import "io"

// OpenServiceLog keeps console logging unchanged on non-Windows hosts.
func OpenServiceLog() (io.WriteCloser, bool, error) {
	return nil, false, nil
}
