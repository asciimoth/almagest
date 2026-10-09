//go:build !linux || !amd64

package main

import "fmt"

func main() { fmt.Println("Linux role profiles require linux/amd64") }
