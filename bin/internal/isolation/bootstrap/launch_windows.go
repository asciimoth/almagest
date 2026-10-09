//go:build windows

package bootstrap

import "fmt"

func validateWorkingDirectory(path string) error {
	if err := validateWindowsTrustedDirectory(path, windowsTrustDirectory); err != nil {
		return fmt.Errorf("%w: %v", errWorkingDirectory, err)
	}
	return nil
}
