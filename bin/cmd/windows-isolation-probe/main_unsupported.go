//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("windows-isolation-probe is only supported on Windows")
}
