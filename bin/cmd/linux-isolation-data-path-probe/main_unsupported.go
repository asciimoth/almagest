//go:build !linux || !amd64

package main

import "fmt"

func main() {
	fmt.Println("linux-isolation-data-path-probe requires linux/amd64")
}
