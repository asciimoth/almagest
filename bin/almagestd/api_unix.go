//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/kataras/golog"
)

const DefaultUnixAdminSocket = "/run/almagest/admin.sock"

func (a *AdminAPI) serveUnix(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	engine almagest.Engine,
	sys sysnet.System,
	logf *LogFactory,
) {
	for {
		conn, err := a.unixListener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, net.ErrClosed) {
				golog.Errorf("Unix admin accept failed: %v", err)
				cancel()
			}
			return
		}
		wg.Go(func() {
			a.serveAPIStream(ctx, conn, engine, sys, logf)
		})
	}
}

func listenUnixAdmin(address string, filters AdminPeerFiltersConfig) (net.Listener, error) {
	if err := prepareUnixAdminSocket(address); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	socketMode := os.FileMode(0o660)
	if filters.Enabled() {
		// Let the credential filter, not the filesystem group, decide which
		// local users may connect when explicit peer rules are configured.
		socketMode = 0o666
	}
	if err := os.Chmod(address, socketMode); err != nil {
		_ = listener.Close()
		_ = cleanupUnixAdminSocket(address)
		return nil, fmt.Errorf("unix admin socket chmod: %w", err)
	}
	if !filters.Enabled() {
		return listener, nil
	}
	protected, err := newUnixAdminProtectedListener(listener, filters)
	if err != nil {
		_ = listener.Close()
		_ = cleanupUnixAdminSocket(address)
		return nil, err
	}
	return protected, nil
}

func prepareUnixAdminSocket(address string) error {
	if address == "" {
		return fmt.Errorf("unix admin socket path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(address), 0o755); err != nil {
		return fmt.Errorf("unix admin socket directory: %w", err)
	}
	return cleanupUnixAdminSocket(address)
}

func cleanupUnixAdminSocket(address string) error {
	info, err := os.Lstat(address)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to remove non-socket unix admin path %s", address)
		}
		return os.Remove(address)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
