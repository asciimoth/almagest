package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil { //nolint:staticcheck // The Linux implementation can return nil.
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
