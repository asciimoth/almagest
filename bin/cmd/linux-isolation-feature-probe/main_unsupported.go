//go:build !linux

package main

import "fmt"

func main() {
	fmt.Println("linux-isolation-feature-probe requires Linux")
}
