//go:build windows

package windowsadmin

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMachinePipeDACLIncludesAdministrationGroup(t *testing.T) {
	original := lookupSID
	t.Cleanup(func() { lookupSID = original })
	group, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	lookupSID = func(_, account string) (*windows.SID, string, uint32, error) {
		if account != GroupName {
			t.Fatalf("account = %q, want %q", account, GroupName)
		}
		return group, "TEST", windows.SidTypeAlias, nil
	}
	dacl, err := MachinePipeDACL()
	if err != nil {
		t.Fatal(err)
	}
	for _, ace := range []string{
		"(A;;GA;;;SY)",
		"(A;;GA;;;S-1-5-32-544)",
		"(A;;GA;;;S-1-5-21-1-2-3-1001)",
	} {
		if !strings.Contains(dacl, ace) {
			t.Fatalf("DACL %q does not contain %q", dacl, ace)
		}
	}
}

func TestMachinePipeDACLAllowsAbsentAdministrationGroup(t *testing.T) {
	original := lookupSID
	t.Cleanup(func() { lookupSID = original })
	lookupSID = func(string, string) (*windows.SID, string, uint32, error) {
		return nil, "", 0, windows.ERROR_NONE_MAPPED
	}
	dacl, err := MachinePipeDACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl != "(A;;GA;;;SY)(A;;GA;;;S-1-5-32-544)" {
		t.Fatalf("DACL = %q", dacl)
	}
}

func TestMachinePipeDACLRejectsInvalidAdministrationPrincipal(t *testing.T) {
	original := lookupSID
	t.Cleanup(func() { lookupSID = original })
	group, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	lookupSID = func(string, string) (*windows.SID, string, uint32, error) {
		return group, "TEST", windows.SidTypeUser, nil
	}
	if _, err = MachinePipeDACL(); err == nil || !strings.Contains(err.Error(), "not a local group") {
		t.Fatalf("wrong account type error = %v", err)
	}
	lookupSID = func(string, string) (*windows.SID, string, uint32, error) {
		return nil, "", 0, errors.New("lookup sentinel")
	}
	if _, err = MachinePipeDACL(); err == nil || !strings.Contains(err.Error(), "lookup sentinel") {
		t.Fatalf("lookup error = %v", err)
	}
}
