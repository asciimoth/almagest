//go:build windows

package platform

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDefaultWindowsUserAdminPipeIsIdentityScoped(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	path := DefaultWindowsUserAdminPipe()
	if !strings.HasPrefix(path, `\\.\pipe\almagest-admin-user-`) || !strings.HasSuffix(path, user.User.Sid.String()) {
		t.Fatalf("DefaultWindowsUserAdminPipe() = %q", path)
	}
	if path == DefaultWindowsMachineAdminPipe {
		t.Fatal("user and machine default pipes are equal")
	}
}
