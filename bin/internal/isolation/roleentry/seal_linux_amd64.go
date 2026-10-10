//go:build linux && amd64

package roleentry

import "github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"

func SealConfigWriter() error { return linuxprofile.Seal(linuxprofile.Writer) }
