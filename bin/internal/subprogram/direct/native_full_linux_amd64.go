//go:build linux && amd64

package direct

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/asciimoth/almagest/lib/sandbox"
	"golang.org/x/sys/unix"
)

type nativeInstanceBoundary struct {
	path string
}

func (b *nativeInstanceBoundary) Child(name string) (*nativeInstanceBoundary, error) {
	if b == nil || b.path == "" || sandbox.ValidateName(name) != nil {
		return nil, sandbox.ErrInvalid
	}
	path := filepath.Join(b.path, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, err
	}
	return &nativeInstanceBoundary{path: path}, nil
}

func fullNativePrerequisites(launcher, configuredRoot string) error {
	if os.Geteuid() != 0 || launcher == "" {
		return sandbox.ErrUnavailable
	}
	const required = uint64(1)<<unix.CAP_CHOWN | uint64(1)<<unix.CAP_DAC_OVERRIDE |
		uint64(1)<<unix.CAP_FOWNER | uint64(1)<<unix.CAP_SYS_ADMIN | uint64(1)<<unix.CAP_NET_ADMIN |
		uint64(1)<<unix.CAP_SYS_CHROOT | uint64(1)<<unix.CAP_SETUID |
		uint64(1)<<unix.CAP_SETGID | uint64(1)<<unix.CAP_SETPCAP |
		uint64(1)<<unix.CAP_MKNOD | uint64(1)<<unix.CAP_SYS_RESOURCE
	status, err := os.Open("/proc/self/status")
	if err != nil {
		return sandbox.ErrUnavailable
	}
	defer func() { _ = status.Close() }()
	var effective uint64
	found := false
	scanner := bufio.NewScanner(status)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "CapEff:\t"); ok {
			effective, err = strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			found = err == nil
			break
		}
	}
	if scanner.Err() != nil || !found || effective&required != required {
		return sandbox.ErrUnavailable
	}
	parent, err := nativeCgroupParent(configuredRoot)
	if err != nil {
		return sandbox.ErrUnavailable
	}
	probe, err := os.MkdirTemp(parent, ".almagest-subprogram-probe-")
	if err != nil {
		return sandbox.ErrUnavailable
	}
	if err = os.Chmod(probe, 0o700); err != nil {
		_ = os.Remove(probe)
		return sandbox.ErrUnavailable
	}
	for file, value := range map[string]string{"memory.max": "33554432", "pids.max": "1", "cpu.max": "1000 100000"} {
		if err = os.WriteFile(filepath.Join(probe, file), []byte(value), 0o600); err != nil {
			_ = os.Remove(probe)
			return sandbox.ErrUnavailable
		}
	}
	return os.Remove(probe)
}

func probeFullNativeLauncher(path string, identity os.FileInfo) error {
	launcher, err := openVerifiedObject(path, identity, false)
	if err != nil {
		return err
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		_ = launcher.Close()
		return err
	}
	cmd := exec.Command("/proc/self/fd/3", "probe-v2")
	cmd.ExtraFiles = []*os.File{launcher, statusWriter}
	prepareProcess(cmd, nil)
	if err = cmd.Start(); err != nil {
		_ = launcher.Close()
		_ = statusWriter.Close()
		_ = statusReader.Close()
		return err
	}
	_ = launcher.Close()
	_ = statusWriter.Close()
	data, readErr := readBounded(statusReader, 512)
	readErr = errors.Join(readErr, statusReader.Close())
	waitErr := cmd.Wait()
	if len(data) != 0 {
		return fmt.Errorf("native probe: %s", sanitizeNativeText(string(data)))
	}
	return errors.Join(readErr, waitErr)
}

func nativeCgroupParent(configured string) (string, error) {
	if configured != "" {
		if err := validatePathComponents(configured); err != nil {
			return "", errors.Join(sandbox.ErrUnavailable, err)
		}
		info, err := os.Lstat(configured)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.Join(sandbox.ErrUnavailable, err)
		}
		return configured, nil
	}
	return currentCgroupPath()
}

func currentCgroupPath() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if value, ok := strings.CutPrefix(line, "0::"); ok && filepath.IsAbs(value) && filepath.Clean(value) == value {
			return filepath.Join("/sys/fs/cgroup", value), nil
		}
	}
	return "", sandbox.ErrUnavailable
}

func newNativeInstanceBoundary(root, name string, limits policy.TemplateLimits) (*nativeInstanceBoundary, error) {
	parent, err := nativeCgroupParent(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(parent, "almagest-subprogram-"+name)
	if err = os.Mkdir(path, 0o700); err != nil {
		return nil, err
	}
	boundary := &nativeInstanceBoundary{path: path}
	cleanup := true
	defer func() {
		if cleanup {
			_ = boundary.Close()
		}
	}()
	values := map[string]string{
		"memory.max": strconv.FormatInt(limits.MemoryBytes, 10),
		// Include one launcher for each command and both worker processes.
		"pids.max": strconv.Itoa(limits.Tasks + limits.Commands + 2),
		"cpu.max":  fmt.Sprintf("%d 100000", limits.CPUPercent*1000),
	}
	for file, value := range values {
		if err = os.WriteFile(filepath.Join(path, file), []byte(value), 0o600); err != nil {
			return nil, fmt.Errorf("set %s: %w", file, err)
		}
	}
	cleanup = false
	return boundary, nil
}

func (b *nativeInstanceBoundary) ProcsFile() (*os.File, error) {
	if b == nil {
		return nil, sandbox.ErrUnavailable
	}
	return os.OpenFile(filepath.Join(b.path, "cgroup.procs"), os.O_WRONLY, 0)
}

func (b *nativeInstanceBoundary) Close() error {
	if b == nil || b.path == "" {
		return nil
	}
	var result error
	if err := os.WriteFile(filepath.Join(b.path, "cgroup.kill"), []byte("1"), 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(b.path, "cgroup.events"))
		if err != nil || strings.Contains(string(data), "populated 0") {
			break
		}
		if time.Now().After(deadline) {
			result = errors.Join(result, sandbox.ErrUnavailable)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	b.path = ""
	return result
}
