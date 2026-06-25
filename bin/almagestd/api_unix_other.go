//go:build !unix

package main

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/sysnet"
)

const DefaultUnixAdminSocket = "/run/almagest/admin.sock"

func (a *AdminAPI) serveUnix(
	context.Context,
	context.CancelFunc,
	*sync.WaitGroup,
	almagest.Engine,
	sysnet.System,
	*LogFactory,
) {
}

func listenUnixAdmin(string, AdminPeerFiltersConfig) (net.Listener, error) {
	return nil, fmt.Errorf("unix admin sockets are not supported on this platform")
}

func cleanupUnixAdminSocket(string) error {
	return nil
}
