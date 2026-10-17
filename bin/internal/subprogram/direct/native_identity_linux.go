//go:build linux

package direct

import (
	"os"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func nativeIdentity(config policy.Linux) (uid, gid uint32, privilegeRemoval bool, err error) {
	currentUID, currentGID := uint32(os.Geteuid()), uint32(os.Getegid())
	switch {
	case config.IdentityPool != "":
		return 0, 0, false, sandbox.ErrUnavailable
	case config.UID != 0:
		if currentUID != 0 && (currentUID != config.UID || currentGID != config.GID) {
			return 0, 0, false, sandbox.ErrUnavailable
		}
		if currentUID == 0 {
			return config.UID, config.GID, true, nil
		}
		clean, groupErr := cleanCurrentGroups(currentGID)
		return config.UID, config.GID, clean, groupErr
	case config.CurrentUser || currentUID != 0:
		if currentUID == 0 {
			return 0, 0, false, sandbox.ErrUnavailable
		}
		clean, groupErr := cleanCurrentGroups(currentGID)
		return currentUID, currentGID, clean, groupErr
	default:
		return 0, 0, false, sandbox.ErrUnavailable
	}
}

func cleanCurrentGroups(primary uint32) (bool, error) {
	groups, err := os.Getgroups()
	if err != nil {
		return false, err
	}
	for _, group := range groups {
		if uint32(group) != primary {
			return false, nil
		}
	}
	return true, nil
}
