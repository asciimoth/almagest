//go:build linux

package discovery

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
)

func defaultLinuxIdentity() policy.Linux {
	if os.Geteuid() == 0 {
		return policy.Linux{UID: 65534, GID: 65534}
	}
	return policy.Linux{CurrentUser: true}
}

// discoverRuntimeInputs computes the ELF dependency closure without executing
// the discovered image. Each dependency is mounted at the absolute name the
// dynamic loader will use. This supports store-based layouts such as Nix,
// whose ELF RUNPATH entries point into /nix/store.
func discoverRuntimeInputs(images ...string) ([]policy.ReadOnly, error) {
	r := elfResolver{seenImages: make(map[string]bool), libraries: make(map[string]string), targets: make(map[string]policy.ReadOnly)}
	for _, image := range images {
		if err := r.inspect(image, image); err != nil {
			return nil, err
		}
		if err := r.addTorData(image); err != nil {
			return nil, err
		}
	}
	targets := make([]string, 0, len(r.targets))
	for target := range r.targets {
		targets = append(targets, target)
	}
	slices.Sort(targets)
	result := make([]policy.ReadOnly, 0, len(targets))
	for _, target := range targets {
		result = append(result, r.targets[target])
	}
	return result, nil
}

type elfResolver struct {
	seenImages map[string]bool
	libraries  map[string]string
	targets    map[string]policy.ReadOnly
}

func (r *elfResolver) inspect(source, logicalPath string) error {
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	if r.seenImages[canonical] {
		return nil
	}
	r.seenImages[canonical] = true
	file, err := elf.Open(canonical)
	if err != nil {
		// A transport can be a script. Its interpreter is outside the fixed Tor
		// policy and will fail closed at launch unless explicitly configured.
		return nil
	}
	defer file.Close() //nolint:errcheck

	if interpreter, ok, readErr := elfInterpreter(file); readErr != nil {
		return fmt.Errorf("read ELF interpreter for %s: %w", canonical, readErr)
	} else if ok {
		if err := r.addFile(interpreter, interpreter); err != nil {
			return fmt.Errorf("add ELF interpreter %s: %w", interpreter, err)
		}
		if err := r.inspect(interpreter, interpreter); err != nil {
			return err
		}
	}

	runpaths := elfSearchPaths(file, filepath.Dir(logicalPath))
	needed, err := file.ImportedLibraries()
	if err != nil {
		return fmt.Errorf("read ELF dependencies for %s: %w", canonical, err)
	}
	dependencies := make([]string, 0, len(needed))
	for _, name := range needed {
		dependency, resolveErr := resolveLibrary(name, runpaths)
		if resolveErr != nil {
			// Some store libraries omit a transitive RUNPATH because the same
			// SONAME was already resolved by their executable.
			dependency = r.libraries[name]
			if dependency == "" {
				return fmt.Errorf("resolve %s required by %s: %w", name, canonical, resolveErr)
			}
		}
		r.libraries[name] = dependency
		if err := r.addFile(dependency, dependency); err != nil {
			return err
		}
		dependencies = append(dependencies, dependency)
	}
	for _, dependency := range dependencies {
		if err := r.inspect(dependency, dependency); err != nil {
			return err
		}
	}
	return nil
}

func elfInterpreter(file *elf.File) (string, bool, error) {
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(program.Open(), 4097))
		if err != nil {
			return "", false, err
		}
		value := strings.TrimRight(string(data), "\x00")
		if len(data) > 4096 || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return "", false, errors.New("invalid ELF interpreter")
		}
		return value, true, nil
	}
	return "", false, nil
}

func elfSearchPaths(file *elf.File, origin string) []string {
	values, _ := file.DynString(elf.DT_RUNPATH)
	if len(values) == 0 {
		values, _ = file.DynString(elf.DT_RPATH)
	}
	result := make([]string, 0, len(values)+10)
	appendList := func(value string) {
		for part := range strings.SplitSeq(value, ":") {
			part = strings.ReplaceAll(part, "${ORIGIN}", origin)
			part = strings.ReplaceAll(part, "$ORIGIN", origin)
			if filepath.IsAbs(part) && !slices.Contains(result, filepath.Clean(part)) {
				result = append(result, filepath.Clean(part))
			}
		}
	}
	for _, value := range values {
		appendList(value)
	}
	for _, directory := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64", "/usr/local/lib"} {
		if !slices.Contains(result, directory) {
			result = append(result, directory)
		}
	}
	for _, pattern := range []string{"/lib/*-linux-gnu", "/usr/lib/*-linux-gnu"} {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if !slices.Contains(result, match) {
				result = append(result, match)
			}
		}
	}
	return result
}

func resolveLibrary(name string, search []string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		if filepath.IsAbs(name) {
			if info, err := os.Stat(name); err == nil && info.Mode().IsRegular() {
				return filepath.Clean(name), nil
			}
		}
		return "", os.ErrNotExist
	}
	for _, directory := range search {
		candidate := filepath.Join(directory, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", os.ErrNotExist
}

func (r *elfResolver) addFile(source, target string) error {
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	target = filepath.Clean(target)
	if !filepath.IsAbs(target) {
		return errors.New("runtime input target is not absolute")
	}
	if current, exists := r.targets[target]; exists && current.Source != canonical {
		return fmt.Errorf("runtime input target %s is ambiguous", target)
	}
	r.targets[target] = policy.ReadOnly{Source: canonical, Target: target, Executable: true}
	return nil
}

func (r *elfResolver) addTorData(image string) error {
	prefix := filepath.Dir(filepath.Dir(image))
	for _, directory := range []string{filepath.Join(prefix, "share", "tor"), "/usr/share/tor", "/usr/local/share/tor"} {
		for _, name := range []string{"geoip", "geoip6"} {
			target := filepath.Join(directory, name)
			if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
				if err := r.addFile(target, target); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
