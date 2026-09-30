//go:build !windows

package main

import (
	"fmt"
	"net"
)

func listenWindowsAdminPipe(string) (net.Listener, error) {
	return nil, fmt.Errorf("windows named pipes are not supported on this platform")
}
