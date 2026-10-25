//go:build !unix

package admingate

import "net"

func listenUnix(string, string, Filters) (net.Listener, error) {
	return nil, errorsUnsupported("Unix admin sockets")
}
