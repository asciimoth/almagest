//go:build !windows

package main

import (
	"os"
	"testing"
)

func webAdminTestFilters(t *testing.T) AdminPeerFiltersConfig {
	t.Helper()
	return AdminPeerFiltersConfig{UIDs: []uint32{uint32(os.Getuid())}}
}
