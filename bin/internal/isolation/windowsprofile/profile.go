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

// Plan selects only restrictions that the launcher implements. Network is the
// trusted native backend. It retains its launch token and can start a bounded
// number of backend-selected helpers. The other roles use separate
// AppContainers and cannot start child processes or use native networking.
type Plan struct {
	Role        Role
	MemoryBytes uint64
	CPUPercent  uint32
}

// DeploymentPlan fixes the persistent AppContainer names, executables and
// private directories installed for R, C and H. N uses the service or caller
// identity and is not part of this AppContainer layout.
type DeploymentPlan struct {
	Instance string
	Runtime  RolePath
	Writer   RolePath
	Command  RolePath
}

type RolePath struct {
	Executable string
	Storage    string
}

func (p DeploymentPlan) Validate() error {
	if err := validateInstance(p.Instance); err != nil {
		return err
	}
	paths := []string{
		p.Runtime.Executable, p.Runtime.Storage,
		p.Writer.Executable, p.Writer.Storage,
		p.Command.Executable, p.Command.Storage,
	}
	for _, path := range paths {
		if err := validatePath(path); err != nil {
			return err
		}
	}
	stores := []string{p.Runtime.Storage, p.Writer.Storage, p.Command.Storage}
	for n, first := range stores {
		for _, second := range stores[n+1:] {
			if strings.EqualFold(first, second) || pathContains(first, second) || pathContains(second, first) {
				return fmt.Errorf("%w: role storage paths overlap", ErrUnavailable)
			}
		}
		for _, executable := range []string{p.Runtime.Executable, p.Writer.Executable, p.Command.Executable} {
			if pathContains(first, executable) {
				return fmt.Errorf("%w: trusted executable is inside role storage", ErrUnavailable)
			}
		}
	}
	return nil
}

func validateInstance(instance string) error {
	if len(instance) < 1 || len(instance) > 64 {
		return fmt.Errorf("%w: invalid instance name", ErrUnavailable)
	}
	for _, c := range instance {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return fmt.Errorf("%w: invalid instance name", ErrUnavailable)
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	parent = strings.TrimRight(parent, `\`)
	return len(child) > len(parent) && strings.EqualFold(child[:len(parent)], parent) && child[len(parent)] == '\\'
}

const networkProcessLimit = 16

func (p Plan) processLimit() uint32 {
	if p.Role == Network {
		return networkProcessLimit
	}
	return 1
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

// CheckFeatures is separate from native capability discovery. N can use
// service authority and already-provisioned backend drivers or helpers. This
// check does not install a driver, attach to another service, or add authority.
func CheckFeatures(serviceNetwork, driverLoading, nativeHelper bool) error {
	_ = serviceNetwork
	_ = driverLoading
	_ = nativeHelper
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
