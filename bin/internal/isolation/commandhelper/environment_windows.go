//go:build windows

package commandhelper

import (
	"errors"
	"strings"
)

func validatePlatformEnvironment(environment map[string]string) error {
	seen := make(map[string]struct{}, len(environment))
	hasSystemRoot := false
	for key := range environment {
		folded := strings.ToUpper(key)
		if _, exists := seen[folded]; exists {
			return errors.New("environment contains case-insensitive duplicate names")
		}
		seen[folded] = struct{}{}
		if folded == "SYSTEMROOT" {
			hasSystemRoot = true
		}
	}
	if !hasSystemRoot {
		return errors.New("windows profile must fix SYSTEMROOT explicitly")
	}
	return nil
}
