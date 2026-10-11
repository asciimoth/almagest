//go:build !linux || !amd64

package main

import "errors"

func runNetworkRole() error {
	return errors.New("isolated network role is not qualified on this platform; use almagestd --no-isolation")
}
