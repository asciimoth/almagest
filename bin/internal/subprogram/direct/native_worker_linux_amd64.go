//go:build linux && amd64

package direct

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	sandbox "github.com/asciimoth/gobox"
)

func (s *instance) startNativeWorker() error {
	boundary, err := newNativeInstanceBoundary(s.backend.policy.LinuxCgroupRoot,
		storageName(s.temporaryName, string(s.templateID), strconv.FormatUint(s.status.Generation, 10)), s.template.Limits)
	if err != nil {
		return fmt.Errorf("%w: create cgroup: %v", sandbox.ErrUnavailable, err)
	}
	s.nativeBoundary = boundary
	mountName := "mount-" + storageName(s.owner.Workspace, s.owner.Key, string(s.templateID), strconv.FormatUint(s.status.Generation, 10))
	mountPath := filepath.Join(s.backend.policy.RegistryRoot, mountName)
	if err = os.Mkdir(mountPath, 0o700); err != nil {
		return fmt.Errorf("%w: create mount root: %v", sandbox.ErrUnavailable, err)
	}
	s.nativeMountName = mountName
	launcher, err := openVerifiedObject(s.backend.policy.LinuxLauncher, s.backend.launcherInfo, false)
	if err != nil {
		return fmt.Errorf("%w: Linux launcher identity", sandbox.ErrLaunch)
	}
	files := []*os.File{launcher}
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		closeFiles()
		return err
	}
	files = append(files, statusWriter)
	lifeReader, lifeWriter, err := os.Pipe()
	if err != nil {
		_ = statusReader.Close()
		closeFiles()
		return err
	}
	files = append(files, lifeReader)
	procs, err := boundary.ProcsFile()
	if err != nil {
		_ = statusReader.Close()
		_ = lifeWriter.Close()
		closeFiles()
		return err
	}
	files = append(files, procs)
	mountRoot, err := safeOpen(mountPath)
	if err != nil {
		_ = statusReader.Close()
		_ = lifeWriter.Close()
		closeFiles()
		return err
	}
	files = append(files, mountRoot)
	temporary, err := openVerifiedObject(s.temporaryPath, s.temporaryInfo, true)
	if err != nil {
		_ = statusReader.Close()
		_ = lifeWriter.Close()
		closeFiles()
		return err
	}
	files = append(files, temporary)
	writable := 1
	if s.persistentPath != "" {
		persistent, openErr := openVerifiedObject(s.persistentPath, s.persistentInfo, true)
		if openErr != nil {
			_ = statusReader.Close()
			_ = lifeWriter.Close()
			closeFiles()
			return openErr
		}
		files = append(files, persistent)
		writable++
	}
	for _, id := range s.template.Programs {
		program := s.backend.policy.Programs[id]
		file, openErr := openVerifiedObject(program.Executable, s.backend.programInfo[id], false)
		if openErr != nil {
			_ = statusReader.Close()
			_ = lifeWriter.Close()
			closeFiles()
			return openErr
		}
		files = append(files, file)
	}
	identities := s.backend.readOnlyInfo[s.templateID]
	for index, input := range s.template.ReadOnly {
		file, openErr := openVerifiedObject(input.Source, identities[index], identities[index].IsDir())
		if openErr != nil {
			_ = statusReader.Close()
			_ = lifeWriter.Close()
			closeFiles()
			return openErr
		}
		files = append(files, file)
	}
	executableReadOnly := make([]byte, len(s.template.ReadOnly))
	for index, input := range s.template.ReadOnly {
		executableReadOnly[index] = '0'
		if input.Executable {
			executableReadOnly[index] = '1'
		}
	}
	arguments := []string{"worker-v3", strconv.FormatUint(uint64(s.nativeUID), 10), strconv.FormatUint(uint64(s.nativeGID), 10),
		strconv.Itoa(writable), strconv.Itoa(len(s.template.Programs)), strconv.Itoa(len(s.template.ReadOnly)),
		strconv.FormatInt(s.template.Limits.TemporaryBytes, 10), mountPath, string(executableReadOnly), "--"}
	for _, id := range s.template.Programs {
		arguments = append(arguments, "/almagest/program/"+string(id))
	}
	for _, input := range s.template.ReadOnly {
		arguments = append(arguments, filepath.ToSlash(filepath.Clean(input.Target)))
	}
	cmd := exec.Command("/proc/self/fd/3", arguments...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = files
	prepareProcess(cmd, s)
	if err = cmd.Start(); err != nil {
		_ = statusReader.Close()
		_ = lifeWriter.Close()
		closeFiles()
		return fmt.Errorf("%w: start namespace worker: %v", sandbox.ErrLaunch, sanitizeNativeError(err))
	}
	closeFiles()
	data, readErr := readWorkerReady(statusReader)
	readErr = errors.Join(readErr, statusReader.Close())
	if readErr != nil {
		_ = lifeWriter.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("%w: namespace worker: %v", sandbox.ErrLaunch, readErr)
	}
	pidText, ok := strings.CutPrefix(string(data), "READY ")
	pid, parseErr := strconv.Atoi(strings.TrimSpace(pidText))
	if !ok || parseErr != nil || pid <= 0 {
		_ = lifeWriter.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("%w: invalid namespace worker response", sandbox.ErrLaunch)
	}
	root, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "root"))
	if err != nil {
		_ = lifeWriter.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("%w: open worker root", sandbox.ErrLaunch)
	}
	names := []string{"mnt", "net", "ipc", "uts", "pid", "cgroup"}
	namespaces := make(map[string]*os.File, len(names))
	for _, name := range names {
		file, openErr := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "ns", name))
		if openErr != nil {
			_ = root.Close()
			for _, held := range namespaces {
				_ = held.Close()
			}
			_ = lifeWriter.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("%w: open worker namespace", sandbox.ErrLaunch)
		}
		namespaces[name] = file
	}
	s.nativeWorker = cmd
	s.nativeWorkerDone = make(chan struct{})
	go s.observeNativeWorker(cmd, s.nativeWorkerDone)
	s.nativeWorkerLife = lifeWriter
	s.nativeWorkerRoot = root
	s.nativeNamespaces = namespaces
	nativeTemporary, err := os.OpenRoot(filepath.Join("/proc", strconv.Itoa(pid), "root", "almagest", "temp"))
	if err != nil {
		_ = s.closeNativeWorker()
		return fmt.Errorf("%w: open quota-limited temporary storage", sandbox.ErrLaunch)
	}
	if old := s.roots[sandbox.Temporary]; old != nil {
		if err = old.Close(); err != nil {
			_ = nativeTemporary.Close()
			_ = s.closeNativeWorker()
			return err
		}
	}
	s.roots[sandbox.Temporary] = &confinedRoot{root: nativeTemporary}
	return nil
}

func (s *instance) observeNativeWorker(command *exec.Cmd, done chan struct{}) {
	err := command.Wait()
	s.mu.Lock()
	s.nativeWorkerWaitErr = err
	unexpected := s.status.State == sandbox.Ready
	if unexpected {
		s.status.State = sandbox.Failed
		s.status.Cleanup = "worker-exited"
		s.err = errors.Join(sandbox.ErrUnavailable, err)
	}
	s.mu.Unlock()
	close(done)
	if unexpected {
		s.cancel(sandbox.ErrUnavailable)
	}
}

func readWorkerReady(reader *os.File) ([]byte, error) {
	data, err := readBounded(reader, 512)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(string(data), "READY ") {
		return nil, errors.New(sanitizeNativeText(string(data)))
	}
	return data, nil
}

func (s *instance) closeNativeWorker() error {
	var result error
	if s.nativeWorkerLife != nil {
		result = errors.Join(result, s.nativeWorkerLife.Close())
		s.nativeWorkerLife = nil
	}
	if s.nativeBoundary != nil {
		result = errors.Join(result, s.nativeBoundary.Close())
		s.nativeBoundary = nil
	}
	if s.nativeWorker != nil {
		if s.nativeWorkerDone != nil {
			<-s.nativeWorkerDone
		}
		waitErr := s.nativeWorkerWaitErr
		var exitErr *exec.ExitError
		if waitErr != nil && !errors.As(waitErr, &exitErr) {
			result = errors.Join(result, waitErr)
		}
		s.nativeWorker = nil
		s.nativeWorkerDone = nil
	}
	if s.nativeWorkerRoot != nil {
		result = errors.Join(result, s.nativeWorkerRoot.Close())
		s.nativeWorkerRoot = nil
	}
	for name, file := range s.nativeNamespaces {
		result = errors.Join(result, file.Close())
		delete(s.nativeNamespaces, name)
	}
	if s.nativeMountName != "" {
		root, err := os.OpenRoot(s.backend.policy.RegistryRoot)
		if err == nil {
			err = root.RemoveAll(s.nativeMountName)
			err = errors.Join(err, root.Close())
		}
		result = errors.Join(result, err)
		s.nativeMountName = ""
	}
	return result
}
