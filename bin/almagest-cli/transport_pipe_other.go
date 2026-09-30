//go:build !windows

package main

import (
	"context"
	"fmt"
	"net"
)

func dialLocalPipe(context.Context, endpoint) (net.Conn, error) {
	return nil, fmt.Errorf("windows named pipes are not supported on this platform")
}
