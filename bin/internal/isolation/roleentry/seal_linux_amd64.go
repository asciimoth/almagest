//go:build linux && amd64

package roleentry

import "github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"

func SealConfigWriter() error  { return linuxprofile.Seal(linuxprofile.Writer) }
func SealCommandHelper() error { return linuxprofile.Seal(linuxprofile.Command) }
func SealRuntime() error       { return linuxprofile.Seal(linuxprofile.Runtime) }
