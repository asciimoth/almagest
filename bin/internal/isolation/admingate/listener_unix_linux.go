//go:build linux

package admingate

import (
	"fmt"
	"net"
	"os/user"
	"strconv"

	"github.com/asciimoth/gonnect/sockowner"
)

type unixPeerListener struct {
	net.Listener
	checker *unixPeerChecker
}

type unixPeerChecker struct {
	uids      map[uint32]struct{}
	gids      map[uint32]struct{}
	usernames []unixUsernameRule
}

type unixUsernameRule struct {
	name string
	uid  uint32
}

func protectUnixListener(listener net.Listener, filters Filters) (net.Listener, error) {
	checker := &unixPeerChecker{uids: make(map[uint32]struct{}, len(filters.UIDs)), gids: make(map[uint32]struct{}, len(filters.GIDs))}
	for _, uid := range filters.UIDs {
		checker.uids[uid] = struct{}{}
	}
	for _, gid := range filters.GIDs {
		checker.gids[gid] = struct{}{}
	}
	for _, name := range filters.Usernames {
		uid, err := resolveUsernameUID(name)
		if err != nil {
			return nil, err
		}
		checker.usernames = append(checker.usernames, unixUsernameRule{name: name, uid: uid})
	}
	return &unixPeerListener{Listener: listener, checker: checker}, nil
}

func (l *unixPeerListener) Accept() (net.Conn, error) {
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

func (c *unixPeerChecker) allow(conn net.Conn) bool {
	owner, err := sockowner.GetIncomingConnOwner(conn)
	if err != nil || owner == nil {
		return false
	}
	if owner.UID != nil {
		if _, ok := c.uids[*owner.UID]; ok {
			return true
		}
		for _, rule := range c.usernames {
			uid, err := resolveUsernameUID(rule.name)
			if err == nil && uid == rule.uid && uid == *owner.UID {
				return true
			}
		}
	}
	if owner.GID != nil {
		_, ok := c.gids[*owner.GID]
		return ok
	}
	return false
}

func resolveUsernameUID(name string) (uint32, error) {
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
