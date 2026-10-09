// almagest-network-role is the minimal network-broker process entry point.
// Production role composition is enabled only after platform profiles qualify.
package main

import (
	"fmt"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
)

func main() {
	channel, err := roleentry.OpenBootstrap(protocol.RoleNetworkBroker, os.Args[1:], os.Getenv)
	if err == nil {
		_ = channel.Close()
		err = fmt.Errorf("isolated network role composition is not enabled")
	}
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
