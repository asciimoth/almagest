//go:build !linux

package main

import "fmt"

func main() { fmt.Println("linux-isolation-matcher-broker requires Linux") }
