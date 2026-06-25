//go:build unix && !linux

package main

import (
	"fmt"
	"net"
)

func newUnixAdminProtectedListener(net.Listener, AdminPeerFiltersConfig) (net.Listener, error) {
	return nil, fmt.Errorf("unix admin peer filters are not supported on this platform")
}
