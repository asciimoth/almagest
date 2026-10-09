//go:build !linux || !amd64

package main

import "fmt"

func main() { fmt.Println("linux-isolation-matcher-probe requires linux/amd64") }
