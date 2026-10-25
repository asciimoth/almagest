//go:build windows

// Package windowsadmin contains the fixed Windows administration principals.
package windowsadmin

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// GroupName is the local group that can use the machine administration pipe.
const GroupName = "Almagest Administrators"

var lookupSID = windows.LookupSID

// MachinePipeDACL returns the protected DACL for a LocalSystem-owned machine
// administration pipe. A developer run can omit the package-created group.
func MachinePipeDACL() (string, error) {
	const base = "(A;;GA;;;SY)(A;;GA;;;S-1-5-32-544)"
	sid, _, accountType, err := lookupSID("", GroupName)
	if errors.Is(err, windows.ERROR_NONE_MAPPED) {
		return base, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve Windows administration group %q: %w", GroupName, err)
	}
	if sid == nil || accountType != windows.SidTypeAlias {
		return "", fmt.Errorf("windows administration principal %q is not a local group", GroupName)
	}
	return base + "(A;;GA;;;" + sid.String() + ")", nil
}
