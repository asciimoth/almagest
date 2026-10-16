// Command sandbox-e2e-helper verifies the fixed environment used by sandbox
// end-to-end tests. It is built only as a test payload.
package main

import (
	"fmt"
	"os"
)

const markerEnvironment = "ALMAGEST_WINDOWS_SANDBOX_MATRIX_CHILD"

func main() {
	if os.Getenv(markerEnvironment) != "1" {
		fmt.Fprintln(os.Stderr, "sandbox marker environment is absent")
		os.Exit(2)
	}
	fmt.Print("windows sandbox child passed")
}
