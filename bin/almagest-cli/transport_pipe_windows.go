//go:build windows

package main

import (
	"context"
	"fmt"
	"net"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

const machinePipeName = "almagest-admin"

func dialLocalPipe(ctx context.Context, ep endpoint) (net.Conn, error) {
	owner, name, err := trustedPipeOwnerAndName(ep)
	if err != nil {
		return nil, err
	}
	path := `\\.\pipe\` + name
	conn, err := (&namedpipe.DialConfig{ExpectedOwner: owner}).DialContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("connect to trusted %s pipe %s: %w", ep.pipeTrust, path, err)
	}
	return conn, nil
}

func trustedPipeOwnerAndName(ep endpoint) (*windows.SID, string, error) {
	name := ep.pipeName
	switch ep.pipeTrust {
	case pipeTrustUser:
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return nil, "", fmt.Errorf("get current Windows identity: %w", err)
		}
		owner, err := user.User.Sid.Copy()
		if err != nil {
			return nil, "", fmt.Errorf("copy current Windows SID: %w", err)
		}
		if name == "" {
			name = "almagest-admin-user-" + owner.String()
		}
		return owner, name, nil
	case pipeTrustMachine:
		owner, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
		if err != nil {
			return nil, "", fmt.Errorf("get Local System SID: %w", err)
		}
		if name == "" {
			name = machinePipeName
		}
		return owner, name, nil
	default:
		return nil, "", fmt.Errorf("unknown pipe trust class %q", ep.pipeTrust)
	}
}
