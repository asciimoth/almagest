//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/asciimoth/gonnect/sockowner"
	"golang.org/x/sys/windows"
)

var (
	getWebAdminIncomingConnOwner = sockowner.GetIncomingConnOwner
	lookupWebAdminAccountSID     = windows.LookupSID
	getWebAdminProcessUserSID    = windowsProcessUserSID
)

func validateWebAdminConfig(config AdminAPIConfig) error {
	filters := config.WebFilters
	if len(filters.UIDs) != 0 || len(filters.GIDs) != 0 {
		return errors.New("windows web admin does not support UID or GID filters; use username filters")
	}
	return nil
}

type windowsWebAdminListener struct {
	net.Listener
	checker *windowsWebAdminPeerChecker
}

type windowsWebAdminPeerChecker struct {
	usernames []windowsWebAdminUsernameRule
}

type windowsWebAdminUsernameRule struct {
	name string
	sid  string
}

func newWebAdminProtectedListener(
	listener net.Listener,
	filters AdminPeerFiltersConfig,
) (net.Listener, error) {
	checker := &windowsWebAdminPeerChecker{}
	for _, name := range filters.Usernames {
		sid, _, _, err := lookupWebAdminAccountSID("", name)
		if err != nil {
			return nil, fmt.Errorf("windows web admin filter resolve username %q: %w", name, err)
		}
		if sid == nil || sid.String() == "" {
			return nil, fmt.Errorf("windows web admin filter resolve username %q: empty SID", name)
		}
		checker.usernames = append(checker.usernames, windowsWebAdminUsernameRule{
			name: name,
			sid:  sid.String(),
		})
	}
	return &windowsWebAdminListener{Listener: listener, checker: checker}, nil
}

func (l *windowsWebAdminListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.checker.allow(conn) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func (c *windowsWebAdminPeerChecker) allow(conn net.Conn) bool {
	owner, err := getWebAdminIncomingConnOwner(conn)
	if err != nil || owner == nil || len(owner.PIDs) != 1 || owner.PIDs[0] <= 0 {
		return false
	}
	ownerSID, err := getWebAdminProcessUserSID(uint32(owner.PIDs[0])) //nolint:gosec // Windows process IDs are uint32 values.
	if err != nil {
		return false
	}
	for _, rule := range c.usernames {
		currentSID, _, _, err := lookupWebAdminAccountSID("", rule.name)
		if err == nil && currentSID != nil && currentSID.String() == rule.sid && ownerSID == rule.sid {
			return true
		}
	}
	return false
}

func windowsProcessUserSID(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(process) }()

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	if user == nil || user.User.Sid == nil {
		return "", errors.New("process token has no user SID")
	}
	return user.User.Sid.String(), nil
}
