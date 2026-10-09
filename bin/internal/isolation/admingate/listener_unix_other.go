//go:build unix && !linux

package admingate

import "net"

func protectUnixListener(net.Listener, Filters) (net.Listener, error) {
	return nil, errorsUnsupported("Unix admin peer filters")
}
