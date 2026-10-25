//go:build windows

package main

import (
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/asciimoth/almagest/bin/internal/windowsadmin"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

const localPipePrefix = `\\.\pipe\`

func listenWindowsAdminPipe(path string) (net.Listener, error) {
	if err := validateLocalPipePath(path); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get windows pipe owner: %w", err)
	}
	service, err := svc.IsWindowsService()
	if err != nil {
		return nil, fmt.Errorf("detect Windows service for admin pipe: %w", err)
	}
	owner := user.User.Sid
	dacl := fmt.Sprintf("(A;;GA;;;SY)(A;;GA;;;%s)", owner.String())
	if service {
		if !owner.IsWellKnown(windows.WinLocalSystemSid) {
			return nil, fmt.Errorf("machine admin pipe requires the supported Local System service identity")
		}
		dacl, err = windowsadmin.MachinePipeDACL()
		if err != nil {
			return nil, err
		}
	}
	sddl := fmt.Sprintf("O:%sD:P%s", owner.String(), dacl)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build windows admin pipe ACL: %w", err)
	}
	listener, err := (&namedpipe.ListenConfig{
		SecurityDescriptor: descriptor,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	}).Listen(path)
	if err != nil {
		return nil, fmt.Errorf("listen on windows admin pipe %s: %w", path, err)
	}
	return listener, nil
}

func validateLocalPipePath(path string) error {
	if len(path) <= len(localPipePrefix) || !strings.EqualFold(path[:len(localPipePrefix)], localPipePrefix) {
		return fmt.Errorf("windows admin pipe must use the local \\.\\pipe\\ namespace")
	}
	name := path[len(localPipePrefix):]
	if name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("windows admin pipe name must be one component")
	}
	if len(name) > 200 {
		return fmt.Errorf("windows admin pipe name is too long")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("windows admin pipe name contains a control character")
	}
	return nil
}
