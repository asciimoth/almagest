package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const MaxEnvironmentEntries = 128

// LaunchSpec contains only inputs fixed by trusted bootstrap. Executable and
// library lookup never use PATH or the runtime working directory.
type LaunchSpec struct {
	Role             protocol.Role
	Executable       string
	WorkingDirectory string
	Environment      []string
	LibraryPaths     []string
}

// OpenTrustedFile opens and returns the same held regular-file object used by
// trusted launch validation. The caller owns the descriptor. Role launch must
// use this descriptor instead of resolving the path again.
func OpenTrustedFile(path string) (*os.File, error) {
	return openTrustedFile(path)
}

// Validate checks the stable part of a role launch before any child starts.
func (s LaunchSpec) Validate(loader FileLoader) error {
	if !s.Role.Valid() || loader == nil {
		return fmt.Errorf("%w: invalid role launch", ErrInvalidInput)
	}
	if !fixedAbsolutePath(s.Executable) || !fixedAbsolutePath(s.WorkingDirectory) {
		return fmt.Errorf("%w: executable and working directory must be absolute clean paths", ErrInvalidInput)
	}
	if len(s.Environment) > MaxEnvironmentEntries {
		return ErrInputTooLarge
	}
	seen := make(map[string]struct{}, len(s.Environment))
	for _, item := range s.Environment {
		name, _, found := strings.Cut(item, "=")
		if !found || !validEnvironmentName(name) || strings.IndexByte(item, 0) >= 0 {
			return fmt.Errorf("%w: invalid environment entry", ErrInvalidInput)
		}
		if unsafeLoaderEnvironment(name) {
			return fmt.Errorf("%w: environment entry %q can change code or library lookup", ErrUnsafeInput, name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("%w: duplicate environment entry %q", ErrAmbiguousInput, name)
		}
		seen[name] = struct{}{}
	}
	file, err := openTrustedFile(s.Executable)
	if err != nil {
		return fmt.Errorf("validate role executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	for _, path := range s.LibraryPaths {
		if !fixedAbsolutePath(path) {
			return fmt.Errorf("%w: library path must be absolute and clean", ErrInvalidInput)
		}
		file, err := openTrustedFile(path)
		if err != nil {
			return fmt.Errorf("validate role library: %w", err)
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return validateWorkingDirectory(s.WorkingDirectory)
}

func unsafeLoaderEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	return upper == "PATH" || upper == "LD_PRELOAD" || upper == "LD_LIBRARY_PATH" ||
		strings.HasPrefix(upper, "DYLD_")
}

// Command constructs an exact-path command. The caller supplies inherited
// bootstrap handles separately and applies the platform restriction profile.
func (s LaunchSpec) Command(arguments ...string) *exec.Cmd {
	command := exec.Command(s.Executable, arguments...) //nolint:gosec // Executable is validated and fixed by trusted bootstrap.
	command.Path = s.Executable
	command.Dir = s.WorkingDirectory
	command.Env = slices.Clone(s.Environment)
	return command
}

func fixedAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !containsParentReference(path)
}

var errWorkingDirectory = errors.New("unsafe role working directory")
