//go:build linux && amd64

package direct

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/asciimoth/almagest/lib/sandbox"
	"golang.org/x/sys/unix"
)

type socketResult struct {
	file *os.File
	err  error
}

func (s *instance) nativeSocket(ctx context.Context, operation, network, address string) (*os.File, error) {
	s.mu.Lock()
	if s.status.State != sandbox.Ready || s.nativeWorkerRoot == nil {
		s.mu.Unlock()
		return nil, sandbox.ErrClosed
	}
	root, rootErr := duplicateFile(s.nativeWorkerRoot)
	mountNamespace, mountErr := duplicateFile(s.nativeNamespaces["mnt"])
	networkNamespace, networkErr := duplicateFile(s.nativeNamespaces["net"])
	s.mu.Unlock()
	if err := errors.Join(rootErr, mountErr, networkErr); err != nil {
		if root != nil {
			_ = root.Close()
		}
		if mountNamespace != nil {
			_ = mountNamespace.Close()
		}
		if networkNamespace != nil {
			_ = networkNamespace.Close()
		}
		return nil, sandbox.ErrUnavailable
	}
	launcher, err := openVerifiedObject(s.backend.policy.LinuxLauncher, s.backend.launcherInfo, false)
	if err != nil {
		_ = root.Close()
		_ = mountNamespace.Close()
		_ = networkNamespace.Close()
		return nil, fmt.Errorf("%w: Linux launcher identity", sandbox.ErrLaunch)
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		_ = launcher.Close()
		_ = root.Close()
		_ = mountNamespace.Close()
		_ = networkNamespace.Close()
		return nil, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = launcher.Close()
		_ = statusReader.Close()
		_ = statusWriter.Close()
		_ = root.Close()
		_ = mountNamespace.Close()
		_ = networkNamespace.Close()
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "sandbox-socket-parent")
	child := os.NewFile(uintptr(pair[1]), "sandbox-socket-child")
	cmd := exec.Command("/proc/self/fd/3", "socket-v2", strconv.FormatUint(uint64(s.nativeUID), 10),
		strconv.FormatUint(uint64(s.nativeGID), 10), operation, network, address)
	cmd.ExtraFiles = []*os.File{launcher, statusWriter, child, root, mountNamespace, networkNamespace}
	prepareProcess(cmd, s)
	if err = cmd.Start(); err != nil {
		for _, file := range cmd.ExtraFiles {
			_ = file.Close()
		}
		_ = statusReader.Close()
		_ = parent.Close()
		return nil, fmt.Errorf("%w: start socket helper: %v", sandbox.ErrLaunch, sanitizeNativeError(err))
	}
	for _, file := range cmd.ExtraFiles {
		_ = file.Close()
	}
	result := make(chan socketResult, 1)
	go func() {
		data := make([]byte, 1)
		control := make([]byte, unix.CmsgSpace(4))
		count, controlCount, _, _, receiveErr := unix.Recvmsg(int(parent.Fd()), data, control, 0)
		if receiveErr != nil || count != 1 || data[0] != '1' {
			result <- socketResult{err: errors.Join(sandbox.ErrLaunch, receiveErr)}
			return
		}
		messages, parseErr := unix.ParseSocketControlMessage(control[:controlCount])
		if parseErr != nil || len(messages) != 1 {
			result <- socketResult{err: errors.Join(sandbox.ErrLaunch, parseErr)}
			return
		}
		rights, parseErr := unix.ParseUnixRights(&messages[0])
		if parseErr != nil || len(rights) != 1 {
			for _, fd := range rights {
				_ = unix.Close(fd)
			}
			result <- socketResult{err: errors.Join(sandbox.ErrLaunch, parseErr)}
			return
		}
		result <- socketResult{file: os.NewFile(uintptr(rights[0]), "sandbox-socket")}
	}()
	select {
	case value := <-result:
		_ = parent.Close()
		waitErr := cmd.Wait()
		status, statusErr := readBounded(statusReader, 512)
		statusErr = errors.Join(statusErr, statusReader.Close())
		if value.err != nil || waitErr != nil || statusErr != nil || len(status) != 0 {
			if value.file != nil {
				_ = value.file.Close()
			}
			if len(status) != 0 {
				value.err = errors.Join(value.err, errors.New(sanitizeNativeText(string(status))))
			}
			return nil, errors.Join(sandbox.ErrLaunch, value.err, waitErr, statusErr)
		}
		return value.file, nil
	case <-ctx.Done():
		_ = parent.Close()
		_ = statusReader.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	case <-s.ctx.Done():
		_ = parent.Close()
		_ = statusReader.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, sandbox.ErrClosed
	}
}
