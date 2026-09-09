//go:build linux

package links

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func socketPathMTU(conn any, ipv6 bool) (int, error) {
	socket, ok := conn.(syscall.Conn)
	if !ok {
		return 0, nil
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	value := 0
	var queryErr error
	err = raw.Control(func(fd uintptr) {
		level, option := unix.IPPROTO_IP, unix.IP_MTU
		if ipv6 {
			level, option = unix.IPPROTO_IPV6, unix.IPV6_MTU
		}
		value, queryErr = unix.GetsockoptInt(int(fd), level, option)
	})
	return value, errors.Join(err, queryErr)
}
