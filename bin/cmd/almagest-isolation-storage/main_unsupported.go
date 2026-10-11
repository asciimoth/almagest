//go:build !linux

package main

import "errors"

func run() error { return errors.New("isolation storage preparation is supported only on Linux") }
