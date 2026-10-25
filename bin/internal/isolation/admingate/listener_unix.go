//go:build unix

package admingate

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

func listenUnix(address, group string, filters Filters) (net.Listener, error) {
	if !filepath.IsAbs(address) {
		return nil, fmt.Errorf("unix admin socket path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(address), 0o755); err != nil {
		return nil, fmt.Errorf("unix admin socket directory: %w", err)
	}
	if err := cleanupUnixSocket(address); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o660)
	if filters.enabled() {
		mode = 0o666
	}
	if err = os.Chmod(address, mode); err != nil {
		_ = listener.Close()
		_ = cleanupUnixSocket(address)
		return nil, fmt.Errorf("unix admin socket chmod: %w", err)
	}
	if group != "" {
		entry, lookupErr := user.LookupGroup(group)
		if lookupErr != nil {
			_ = listener.Close()
			_ = cleanupUnixSocket(address)
			return nil, fmt.Errorf("unix admin socket resolve group %q: %w", group, lookupErr)
		}
		gid, parseErr := strconv.ParseInt(entry.Gid, 10, 32)
		if parseErr != nil {
			_ = listener.Close()
			_ = cleanupUnixSocket(address)
			return nil, fmt.Errorf("unix admin socket parse group %q gid: %w", group, parseErr)
		}
		if chownErr := os.Chown(address, -1, int(gid)); chownErr != nil {
			_ = listener.Close()
			_ = cleanupUnixSocket(address)
			return nil, fmt.Errorf("unix admin socket chown group %q: %w", group, chownErr)
		}
	}
	if filters.enabled() {
		listener, err = protectUnixListener(listener, filters)
		if err != nil {
			_ = listener.Close()
			_ = cleanupUnixSocket(address)
			return nil, err
		}
	}
	return &cleanupListener{Listener: listener, path: address}, nil
}

func cleanupUnixSocket(address string) error {
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

type cleanupListener struct {
	net.Listener
	path string
}

func (l *cleanupListener) Close() error {
	closeErr := l.Listener.Close()
	cleanupErr := cleanupUnixSocket(l.path)
	if closeErr != nil && !isClosedError(closeErr) {
		return closeErr
	}
	return cleanupErr
}

func isClosedError(err error) bool { return err == nil || err == net.ErrClosed || os.IsNotExist(err) }
