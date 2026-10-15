// almagest-config-writer-role is the minimal fixed-target writer entry point.
// Production role composition is enabled only after platform profiles qualify.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/asciimoth/almagest/bin/internal/isolation/configwriter"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	channel, err := roleentry.OpenBootstrap(protocol.RoleConfigWriter, os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	defer channel.Close() //nolint:errcheck
	if err := roleentry.SealConfigWriter(); err != nil {
		return fmt.Errorf("seal config writer: %w", err)
	}
	ctx := context.Background()
	bootstrap, err := roleentry.ReceiveBootstrap(ctx, channel, protocol.RoleConfigWriter)
	if err != nil {
		return fmt.Errorf("read trusted bootstrap: %w", err)
	}
	root := os.Getenv("ALMAGEST_STORAGE")
	if runtime.GOOS == "linux" {
		if os.Getenv("ALMAGEST_LINUX_PROFILE") == "linux-user-v1" {
			root = "."
		} else {
			root = "/state"
		}
	}
	if root == "" || root != "." && (!filepath.IsAbs(root) || filepath.Clean(root) != root) {
		return fmt.Errorf("invalid fixed config-writer storage")
	}
	target := filepath.Join(root, bootstrap.Target)
	if filepath.Dir(target) != root {
		return protocol.ErrInvalidContract
	}
	writer, err := configwriter.New(bootstrap.Session, target)
	if err != nil {
		return fmt.Errorf("open fixed config target: %w", err)
	}
	defer writer.Close() //nolint:errcheck
	if err := roleentry.AcknowledgeBootstrap(ctx, channel, bootstrap); err != nil {
		return fmt.Errorf("acknowledge bootstrap: %w", err)
	}
	return writer.Serve(ctx, transport.Lanes{Control: channel})
}
