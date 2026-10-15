//go:build !linux || !amd64

package linuxprofile

import "os"

func ProbeUserPrerequisites(*os.File, ...*os.File) error { return ErrUnavailable }
