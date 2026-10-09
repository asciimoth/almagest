//go:build !linux || !amd64

package linuxprofile

func Seal(Role) error { return ErrUnavailable }
