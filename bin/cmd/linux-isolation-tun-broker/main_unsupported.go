//go:build !linux

package main

import "fmt"

func main() { fmt.Println("linux-isolation-tun-broker requires Linux") }
