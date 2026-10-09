//go:build !windows

package bootstrap

import "testing"

func hardenTestStorageRoot(*testing.T, string) {}
