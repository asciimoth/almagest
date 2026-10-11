// almagest-network-role is the minimal trusted bootstrap and network broker.
// It imports no daemon runtime or node packages.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := runNetworkRole(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
