//go:build windows

package main

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/asciimoth/gonnect/sockowner"
	"golang.org/x/sys/windows"
)

func TestValidateWindowsWebAdminConfig(t *testing.T) {
	for _, test := range []struct {
		name   string
		config AdminAPIConfig
		want   string
	}{
		{
			name:   "UID",
			config: AdminAPIConfig{WebFilters: AdminPeerFiltersConfig{UIDs: []uint32{1}}},
			want:   "does not support UID or GID",
		},
		{
			name:   "GID",
			config: AdminAPIConfig{WebFilters: AdminPeerFiltersConfig{GIDs: []uint32{1}}},
			want:   "does not support UID or GID",
		},
		{
			name:   "enabled without username",
			config: AdminAPIConfig{Web: 8080},
		},
		{
			name: "enabled with username",
			config: AdminAPIConfig{
				Web:        8080,
				WebFilters: AdminPeerFiltersConfig{Usernames: []string{"alice"}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateWebAdminConfig(test.config)
			if test.want == "" && err != nil {
				t.Fatalf("validateWebAdminConfig() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateWebAdminConfig() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func webAdminTestFilters(t *testing.T) AdminPeerFiltersConfig {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		t.Fatal(err)
	}
	if domain != "" {
		account = domain + `\` + account
	}
	return AdminPeerFiltersConfig{Usernames: []string{account}}
}

func TestWindowsWebAdminPeerChecker(t *testing.T) {
	allowedSID, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	otherSID, err := windows.StringToSid("S-1-5-21-1-2-3-1002")
	if err != nil {
		t.Fatal(err)
	}

	originalOwner := getWebAdminIncomingConnOwner
	originalLookup := lookupWebAdminAccountSID
	originalProcessUser := getWebAdminProcessUserSID
	t.Cleanup(func() {
		getWebAdminIncomingConnOwner = originalOwner
		lookupWebAdminAccountSID = originalLookup
		getWebAdminProcessUserSID = originalProcessUser
	})

	lookupWebAdminAccountSID = func(_, name string) (*windows.SID, string, uint32, error) {
		if name != "alice" {
			return nil, "", 0, errors.New("unknown account")
		}
		return allowedSID, "TEST", windows.SidTypeUser, nil
	}
	getWebAdminIncomingConnOwner = func(net.Conn) (*sockowner.SocketOwner, error) {
		return &sockowner.SocketOwner{PIDs: []int{42}}, nil
	}

	checker := &windowsWebAdminPeerChecker{
		usernames: []windowsWebAdminUsernameRule{{name: "alice", sid: allowedSID.String()}},
	}
	getWebAdminProcessUserSID = func(uint32) (string, error) {
		return allowedSID.String(), nil
	}
	if !checker.allow(nil) {
		t.Fatal("allowed owner was denied")
	}

	getWebAdminProcessUserSID = func(uint32) (string, error) {
		return otherSID.String(), nil
	}
	if checker.allow(nil) {
		t.Fatal("wrong owner was allowed")
	}

	getWebAdminIncomingConnOwner = func(net.Conn) (*sockowner.SocketOwner, error) {
		return &sockowner.SocketOwner{PIDs: []int{42, 43}}, nil
	}
	if checker.allow(nil) {
		t.Fatal("ambiguous owner was allowed")
	}

	getWebAdminIncomingConnOwner = func(net.Conn) (*sockowner.SocketOwner, error) {
		return &sockowner.SocketOwner{PIDs: []int{42}}, nil
	}
	getWebAdminProcessUserSID = func(uint32) (string, error) {
		return allowedSID.String(), nil
	}
	lookupWebAdminAccountSID = func(_, _ string) (*windows.SID, string, uint32, error) {
		return otherSID, "TEST", windows.SidTypeUser, nil
	}
	if checker.allow(nil) {
		t.Fatal("remapped username was allowed")
	}
}
