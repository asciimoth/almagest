//go:build linux

package main

import (
	"fmt"
	"net"
	"os/user"
	"strconv"

	"github.com/asciimoth/gonnect/sockowner"
)

type unixAdminProtectedListener struct {
	net.Listener
	checker *adminPeerChecker
}

func newUnixAdminProtectedListener(l net.Listener, filters AdminPeerFiltersConfig) (net.Listener, error) {
	checker, err := newAdminPeerChecker(filters)
	if err != nil {
		return nil, err
	}
	return &unixAdminProtectedListener{Listener: l, checker: checker}, nil
}

func (l *unixAdminProtectedListener) Accept() (net.Conn, error) {
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

type adminPeerChecker struct {
	uids      map[uint32]struct{}
	gids      map[uint32]struct{}
	usernames []adminUsernameRule
}

type adminUsernameRule struct {
	name string
	uid  uint32
}

func newAdminPeerChecker(filters AdminPeerFiltersConfig) (*adminPeerChecker, error) {
	c := &adminPeerChecker{
		uids: make(map[uint32]struct{}, len(filters.UIDs)),
		gids: make(map[uint32]struct{}, len(filters.GIDs)),
	}
	for _, uid := range filters.UIDs {
		c.uids[uid] = struct{}{}
	}
	for _, gid := range filters.GIDs {
		c.gids[gid] = struct{}{}
	}
	for _, name := range filters.Usernames {
		uid, err := resolveAdminUsernameUID(name)
		if err != nil {
			return nil, err
		}
		c.usernames = append(c.usernames, adminUsernameRule{name: name, uid: uid})
	}
	return c, nil
}

func (c *adminPeerChecker) allow(conn net.Conn) bool {
	owner, err := sockowner.GetIncomingConnOwner(conn)
	if err != nil || owner == nil {
		return false
	}
	if owner.UID != nil {
		if _, ok := c.uids[*owner.UID]; ok {
			return true
		}
		for _, rule := range c.usernames {
			uid, err := resolveAdminUsernameUID(rule.name)
			if err == nil && uid == rule.uid && *owner.UID == uid {
				return true
			}
		}
	}
	if owner.GID != nil {
		if _, ok := c.gids[*owner.GID]; ok {
			return true
		}
	}
	return false
}

func resolveAdminUsernameUID(name string) (uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("unix admin filter resolve username %q: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("unix admin filter parse username %q uid: %w", name, err)
	}
	return uint32(uid), nil
}
