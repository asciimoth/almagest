//go:build !unix

package admingate

import "net"

func listenUnix(string, Filters) (net.Listener, error) {
	return nil, errorsUnsupported("Unix admin sockets")
}
