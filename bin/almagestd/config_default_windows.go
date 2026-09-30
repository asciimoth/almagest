//go:build windows

package main

import (
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"golang.org/x/sys/windows/svc"
)

func defaultAdminAPIConfig() AdminAPIConfig {
	pipe := platform.DefaultWindowsUserAdminPipe()
	if service, err := svc.IsWindowsService(); err == nil && service {
		pipe = platform.DefaultWindowsMachineAdminPipe
	}
	return AdminAPIConfig{Pipe: pipe}
}
