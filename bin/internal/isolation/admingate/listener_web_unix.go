//go:build !windows

package admingate

import (
	"net"

	"github.com/asciimoth/gonnect/protected"
)

func listenWeb(address string, filters Filters) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if !filters.enabled() {
		return listener, nil
	}
	guarded, err := protected.NewListener(listener, protected.Rules{
		UIDs: filters.UIDs, GIDs: filters.GIDs, Usernames: filters.Usernames,
	})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return guarded, nil
}

func listenPipe(string, Filters) (net.Listener, error) {
	return nil, errorsUnsupported("Windows named pipes")
}
