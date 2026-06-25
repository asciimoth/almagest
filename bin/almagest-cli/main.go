package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, realDial); err != nil {
		if !errors.Is(err, errHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
