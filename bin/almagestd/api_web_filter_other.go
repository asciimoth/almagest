//go:build !windows

package main

import (
	"net"

	"github.com/asciimoth/gonnect/protected"
)

func validateWebAdminConfig(AdminAPIConfig) error {
	return nil
}

func newWebAdminProtectedListener(
	listener net.Listener,
	filters AdminPeerFiltersConfig,
) (net.Listener, error) {
	return protected.NewListener(listener, protected.Rules{
		UIDs:      filters.UIDs,
		GIDs:      filters.GIDs,
		Usernames: filters.Usernames,
	})
}
