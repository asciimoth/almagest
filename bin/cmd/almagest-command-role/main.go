// almagest-command-role executes only startup-selected command profiles inside
// the restricted H process.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
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
	control, err := roleentry.OpenBootstrap(protocol.RoleCommandHelper, os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	defer control.Close() //nolint:errcheck
	var callback transport.Stream
	if os.Getenv(string(roleentry.LaneCallback)) != "" && os.Getenv(string(roleentry.LaneCallback)) != "0" {
		callback, err = roleentry.OpenLane(roleentry.LaneCallback, os.Getenv)
		if err != nil {
			return err
		}
		defer callback.Close() //nolint:errcheck
	}
	if err := roleentry.SealCommandHelper(); err != nil {
		return fmt.Errorf("seal command helper: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bootstrap, err := roleentry.ReceiveBootstrap(ctx, control, protocol.RoleCommandHelper)
	if err != nil {
		return fmt.Errorf("read trusted bootstrap: %w", err)
	}
	var config commandhelper.Config
	if err := transport.ReadFrameDeadline(control, &config, time.Now().Add(transport.FrameTimeout)); err != nil {
		return fmt.Errorf("read command profiles: %w", err)
	}
	helper, err := commandhelper.New(bootstrap.Session, config, commandhelper.NewProcessLauncher("fixed"))
	if err != nil {
		return fmt.Errorf("prepare command helper: %w", err)
	}
	defer helper.Close() //nolint:errcheck
	if err := roleentry.AcknowledgeBootstrap(ctx, control, bootstrap); err != nil {
		return fmt.Errorf("acknowledge bootstrap: %w", err)
	}
	results := make(chan error, 2)
	go func() { results <- helper.ServeRuntime(ctx, transport.Lanes{Control: control}) }()
	count := 1
	if callback != nil {
		count++
		go func() { results <- helper.ServeCallbacks(ctx, transport.Lanes{Control: callback}) }()
	}
	err = <-results
	cancel()
	if count == 2 {
		if callbackErr := <-results; err == nil {
			err = callbackErr
		}
	}
	return err
}
