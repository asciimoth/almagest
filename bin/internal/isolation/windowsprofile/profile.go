// Package windowsprofile implements fixed Windows role restrictions. It does
// not enable isolated daemon startup. Inputs belong to trusted bootstrap, never
// to regular configuration or a runtime request.
package windowsprofile

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type Role string

const (
	Runtime Role = "runtime"
	Network Role = "network"
	Writer  Role = "writer"
	Command Role = "command"
)

var ErrUnavailable = errors.New("windows isolation profile unavailable; use explicit --no-isolation")

// Plan selects only restrictions that the launcher implements. Service N,
// driver loading, and native helpers need separate deployment qualification.
// Command is a single fixed executable, with no children or network access.
type Plan struct {
	Role        Role
	MemoryBytes uint64
	CPUPercent  uint32
}

func DefaultPlan(role Role) Plan {
	return Plan{Role: role, MemoryBytes: 512 << 20, CPUPercent: 25}
}

func (p Plan) Validate() error {
	if p.Role != Runtime && p.Role != Network && p.Role != Writer && p.Role != Command {
		return fmt.Errorf("%w: unknown role", ErrUnavailable)
	}
	if p.MemoryBytes < 64<<20 || p.MemoryBytes > 1<<40 || p.CPUPercent == 0 || p.CPUPercent > 100 {
		return fmt.Errorf("%w: invalid job limits", ErrUnavailable)
	}
	return nil
}

// CheckPlatform deliberately limits the component to the experimental matrix.
// A newer build is not assumed to have the same security properties.
func CheckPlatform(build uint32, arch string) error {
	if build != 20348 || arch != "amd64" {
		return fmt.Errorf("%w: qualify this Windows build and architecture first", ErrUnavailable)
	}
	return nil
}

// CheckFeatures is separate from native capability discovery. An unsupported
// isolated profile must not be reported as an unavailable native feature.
func CheckFeatures(serviceNetwork, driverLoading, nativeHelper bool) error {
	if serviceNetwork || driverLoading || nativeHelper {
		return fmt.Errorf("%w: service N, driver loading and native helpers need separate qualification", ErrUnavailable)
	}
	return nil
}

// validatePath rejects Windows aliases on every build host, before Win32 can
// normalize them. Object identity, reparse points and ACLs need native checks.
func validatePath(path string) error {
	if len(path) < 4 || len(path) > 240 || (path[0] < 'A' || path[0] > 'Z') && (path[0] < 'a' || path[0] > 'z') || path[1:3] != `:\` || strings.ContainsAny(path, "/\x00") || !utf8.ValidString(path) {
		return fmt.Errorf("%w: use a local absolute Windows path", ErrUnavailable)
	}
	for _, c := range path {
		if c < 32 {
			return fmt.Errorf("%w: control character in path", ErrUnavailable)
		}
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part || strings.ContainsAny(part, ":*?\"<>|~") {
			return fmt.Errorf("%w: ambiguous path component", ErrUnavailable)
		}
		base, _, _ := strings.Cut(strings.ToUpper(part), ".")
		if base == "CON" || base == "CONIN$" || base == "CONOUT$" || base == "PRN" || base == "AUX" || base == "NUL" || strings.HasPrefix(base, "COM") && utf8.RuneCountInString(base) == 4 || strings.HasPrefix(base, "LPT") && utf8.RuneCountInString(base) == 4 {
			return fmt.Errorf("%w: device path component", ErrUnavailable)
		}
	}
	return nil
}
