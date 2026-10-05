//go:build windows

package platform

import (
	"golang.org/x/sys/windows"
)

// DefaultWindowsMachineAdminPipe is the administration endpoint for a daemon
// that runs as a Local System service. Its listener permits Local System and
// elevated administrators; clients verify that Local System owns the pipe.
const DefaultWindowsMachineAdminPipe = `\\.\pipe\almagest-admin`

// DefaultWindowsUserAdminPipe separates interactive daemon state by Windows
// identity. A machine service uses DefaultWindowsMachineAdminPipe instead.
func DefaultWindowsUserAdminPipe() string {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return ""
	}
	return `\\.\pipe\almagest-admin-user-` + user.User.Sid.String()
}
