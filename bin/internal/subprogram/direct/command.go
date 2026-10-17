package direct

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

type command struct {
	instance      *instance
	programID     sandbox.ProgramID
	executable    string
	arguments     []string
	directory     string
	directoryPath sandbox.Path
	generation    uint64

	mu           sync.Mutex
	cmd          *exec.Cmd
	boundary     processBoundary
	started      bool
	closed       bool
	waiting      bool
	done         chan struct{}
	exit         sandbox.Exit
	waitErr      error
	stdinUsed    bool
	stdoutUsed   bool
	stderrUsed   bool
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	stdoutChild  *os.File
	stderrChild  *os.File
	killRequest  bool
	native       bool
	nativeFiles  []*os.File
	nativeCgroup *nativeInstanceBoundary
	execStatus   *os.File
	setupErr     error
}

var _ sandbox.Command = (*command)(nil)

func newCommand(instance *instance, programID sandbox.ProgramID, executable string, arguments []string, directory string, directoryPath sandbox.Path, generation uint64) *command {
	return &command{instance: instance, programID: programID, executable: executable, arguments: arguments, directory: directory, directoryPath: directoryPath, generation: generation, done: make(chan struct{})}
}

func (c *command) StdinPipe() (io.WriteCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, sandbox.ErrClosed
	}
	if c.started || c.stdinUsed {
		return nil, sandbox.ErrConflict
	}
	c.stdinUsed = true
	writer, err := c.ensureCommandLocked().StdinPipe()
	if c.setupErr != nil {
		return nil, c.setupErr
	}
	if err != nil {
		return nil, err
	}
	c.stdin = writer
	return writer, nil
}

func (c *command) StdoutPipe() (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, sandbox.ErrClosed
	}
	if c.started || c.stdoutUsed {
		return nil, sandbox.ErrConflict
	}
	c.stdoutUsed = true
	reader, writer, err := os.Pipe()
	if c.setupErr != nil {
		if err == nil {
			_ = reader.Close()
			_ = writer.Close()
		}
		return nil, c.setupErr
	}
	if err != nil {
		return nil, err
	}
	c.ensureCommandLocked().Stdout = writer
	c.stdout = reader
	c.stdoutChild = writer
	return reader, nil
}

func (c *command) StderrPipe() (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, sandbox.ErrClosed
	}
	if c.started || c.stderrUsed {
		return nil, sandbox.ErrConflict
	}
	c.stderrUsed = true
	reader, writer, err := os.Pipe()
	if c.setupErr != nil {
		if err == nil {
			_ = reader.Close()
			_ = writer.Close()
		}
		return nil, c.setupErr
	}
	if err != nil {
		return nil, err
	}
	c.ensureCommandLocked().Stderr = writer
	c.stderr = reader
	c.stderrChild = writer
	return reader, nil
}

func (c *command) ensureCommandLocked() *exec.Cmd {
	if c.cmd != nil {
		return c.cmd
	}
	if c.instance.nativeProfile {
		c.native = true
		arguments := c.nativeArguments()
		if c.instance.nativeFull {
			arguments = c.fullNativeArguments()
		}
		c.cmd = exec.Command("/proc/self/fd/3", arguments...)
		c.cmd.Dir = "/"
	} else if heldPath := heldExecutablePath(); heldPath != "" {
		c.cmd = exec.Command(heldPath, c.arguments...)
		c.cmd.Dir = c.directory
	} else {
		c.cmd = exec.Command(c.executable, c.arguments...)
		c.cmd.Dir = c.directory
	}
	c.cmd.Env = c.environment()
	c.cmd.WaitDelay = 5 * time.Second
	prepareProcess(c.cmd, c.instance)
	return c.cmd
}

func (c *command) fullNativeArguments() []string {
	limits := c.instance.template.Limits
	descriptors := limits.Files + limits.Sockets + 16
	if descriptors < 32 {
		descriptors = 32
	}
	if descriptors > 65536 {
		descriptors = 65536
	}
	cwd, err := c.instance.ChildPath(c.directoryPath)
	if err != nil {
		c.setupErr = err
		cwd = "/almagest/temp"
	}
	programPath, err := c.instance.ProgramPath(c.programID)
	if err != nil {
		c.setupErr = err
		programPath = "/almagest/program/invalid"
	}
	arguments := []string{"launch-v3", strconv.FormatUint(uint64(c.instance.nativeUID), 10), strconv.FormatUint(uint64(c.instance.nativeGID), 10),
		strconv.Itoa(descriptors), strconv.FormatInt(limits.MemoryBytes, 10), strconv.Itoa(limits.Tasks), programPath, cwd,
		strconv.Itoa(len(c.allowedProgramIDs())), "--"}
	return append(arguments, c.arguments...)
}

func (c *command) nativeArguments() []string {
	limits := c.instance.template.Limits
	descriptors := limits.Files + limits.Sockets + 16
	if descriptors < 32 {
		descriptors = 32
	}
	if descriptors > 65536 {
		descriptors = 65536
	}
	writable := 1
	if c.instance.persistentPath != "" {
		writable++
	}
	arguments := []string{"v2", strconv.FormatUint(uint64(c.instance.nativeUID), 10), strconv.FormatUint(uint64(c.instance.nativeGID), 10),
		strconv.Itoa(descriptors), strconv.FormatInt(limits.MemoryBytes, 10), strconv.Itoa(limits.Tasks),
		strconv.Itoa(writable), strconv.Itoa(len(c.allowedProgramIDs())), strconv.Itoa(len(c.instance.template.ReadOnly)), "--"}
	return append(arguments, c.arguments...)
}

func (c *command) configureNativeLocked() error {
	if !c.native {
		heldPath := heldExecutablePath()
		if heldPath == "" {
			return nil
		}
		program := c.instance.backend.policy.Programs[c.programID]
		file, err := openVerifiedObject(program.Executable, c.instance.backend.programInfo[c.programID], false)
		if err != nil {
			return fmt.Errorf("%w: approved program identity", sandbox.ErrLaunch)
		}
		c.cmd.ExtraFiles = []*os.File{file}
		c.nativeFiles = []*os.File{file}
		return nil
	}
	launcher, err := openVerifiedObject(c.instance.backend.policy.LinuxLauncher, c.instance.backend.launcherInfo, false)
	if err != nil {
		return fmt.Errorf("%w: Linux launcher identity", sandbox.ErrLaunch)
	}
	files := []*os.File{launcher}
	cleanup := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		cleanup()
		return err
	}
	files = append(files, statusWriter)
	if c.instance.nativeFull {
		return c.configureFullNativeLocked(files, statusReader, cleanup)
	}
	cwd, err := c.openWorkingDirectory()
	if err != nil {
		_ = statusReader.Close()
		cleanup()
		return fmt.Errorf("%w: working directory identity", sandbox.ErrLaunch)
	}
	files = append(files, cwd)
	temporary, err := openVerifiedObject(c.instance.temporaryPath, c.instance.temporaryInfo, true)
	if err != nil {
		_ = statusReader.Close()
		cleanup()
		return fmt.Errorf("%w: temporary storage identity", sandbox.ErrLaunch)
	}
	files = append(files, temporary)
	if c.instance.persistentPath != "" {
		persistent, openErr := openVerifiedObject(c.instance.persistentPath, c.instance.persistentInfo, true)
		if openErr != nil {
			_ = statusReader.Close()
			cleanup()
			return fmt.Errorf("%w: persistent storage identity", sandbox.ErrLaunch)
		}
		files = append(files, persistent)
	}
	for _, id := range c.allowedProgramIDs() {
		program := c.instance.backend.policy.Programs[id]
		file, openErr := openVerifiedObject(program.Executable, c.instance.backend.programInfo[id], false)
		if openErr != nil {
			_ = statusReader.Close()
			cleanup()
			return fmt.Errorf("%w: approved program identity", sandbox.ErrLaunch)
		}
		files = append(files, file)
	}
	identities := c.instance.backend.readOnlyInfo[c.instance.templateID]
	for index, input := range c.instance.template.ReadOnly {
		file, openErr := openVerifiedObject(input.Source, identities[index], identities[index].IsDir())
		if openErr != nil {
			_ = statusReader.Close()
			cleanup()
			return fmt.Errorf("%w: read-only input identity", sandbox.ErrLaunch)
		}
		files = append(files, file)
	}
	c.cmd.ExtraFiles = files
	c.nativeFiles = files
	c.execStatus = statusReader
	return nil
}

func (c *command) configureFullNativeLocked(files []*os.File, statusReader *os.File, cleanup func()) error {
	if c.instance.nativeWorkerRoot == nil || c.instance.nativeBoundary == nil {
		_ = statusReader.Close()
		cleanup()
		return sandbox.ErrUnavailable
	}
	root, err := duplicateFile(c.instance.nativeWorkerRoot)
	if err != nil {
		_ = statusReader.Close()
		cleanup()
		return err
	}
	files = append(files, root)
	for _, name := range []string{"mnt", "net", "ipc", "uts", "pid", "cgroup"} {
		file, ok := c.instance.nativeNamespaces[name]
		if !ok {
			_ = statusReader.Close()
			for _, value := range files[2:] {
				_ = value.Close()
			}
			cleanup()
			return sandbox.ErrUnavailable
		}
		copy, copyErr := duplicateFile(file)
		if copyErr != nil {
			_ = statusReader.Close()
			for _, value := range files[2:] {
				_ = value.Close()
			}
			cleanup()
			return copyErr
		}
		files = append(files, copy)
	}
	commandCgroup, err := c.instance.nativeBoundary.Child("command-" + strconv.FormatUint(c.generation, 10))
	if err != nil {
		_ = statusReader.Close()
		for _, value := range files[2:] {
			_ = value.Close()
		}
		cleanup()
		return err
	}
	c.nativeCgroup = commandCgroup
	procs, err := commandCgroup.ProcsFile()
	if err != nil {
		_ = commandCgroup.Close()
		c.nativeCgroup = nil
		_ = statusReader.Close()
		for _, value := range files[2:] {
			_ = value.Close()
		}
		cleanup()
		return err
	}
	files = append(files, procs)
	for _, id := range c.allowedProgramIDs() {
		program := c.instance.backend.policy.Programs[id]
		executable, openErr := openVerifiedObject(program.Executable, c.instance.backend.programInfo[id], false)
		if openErr != nil {
			_ = commandCgroup.Close()
			c.nativeCgroup = nil
			_ = statusReader.Close()
			for _, value := range files[2:] {
				_ = value.Close()
			}
			cleanup()
			return fmt.Errorf("%w: approved program identity", sandbox.ErrLaunch)
		}
		files = append(files, executable)
	}
	c.cmd.ExtraFiles = files
	c.nativeFiles = files
	c.execStatus = statusReader
	return nil
}

// allowedProgramIDs returns the selected image followed by its transitive,
// policy-declared descendant set. The order fixes the native descriptor ABI:
// descriptor 13 is always the selected image.
func (c *command) allowedProgramIDs() []sandbox.ProgramID {
	result := make([]sandbox.ProgramID, 0, 1+len(c.instance.backend.policy.Programs[c.programID].Descendants))
	seen := make(map[sandbox.ProgramID]bool)
	var add func(sandbox.ProgramID)
	add = func(id sandbox.ProgramID) {
		if seen[id] {
			return
		}
		seen[id] = true
		result = append(result, id)
		for _, descendant := range c.instance.backend.policy.Programs[id].Descendants {
			add(descendant)
		}
	}
	add(c.programID)
	return result
}

func (c *command) openWorkingDirectory() (*os.File, error) {
	root := c.instance.roots[c.directoryPath.Root]
	if root == nil {
		return nil, sandbox.ErrUnavailable
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	name, err := root.validate(c.directoryPath.Rel, true)
	if err != nil {
		return nil, err
	}
	info, err := root.root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(sandbox.ErrDenied, err)
	}
	return openVerifiedObject(c.directory, info, true)
}

func openVerifiedObject(path string, expected os.FileInfo, directory bool) (*os.File, error) {
	if expected == nil {
		return nil, sandbox.ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, errors.Join(sandbox.ErrDenied, err)
	}
	file, err := safeOpen(path)
	if err != nil {
		return nil, err
	}
	held, statErr := file.Stat()
	aliases := statErr == nil && openedFileHasMultipleLinks(file, held)
	if statErr != nil || !os.SameFile(info, held) || !os.SameFile(expected, held) || aliases {
		_ = file.Close()
		return nil, errors.Join(sandbox.ErrDenied, statErr)
	}
	return file, nil
}

func (c *command) closeNativeFilesLocked() error {
	var result error
	for _, file := range c.nativeFiles {
		result = errors.Join(result, file.Close())
	}
	c.nativeFiles = nil
	return result
}

func (c *command) closeBeforeStart() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.closeNativeFilesLocked()
	if c.nativeCgroup != nil {
		result = errors.Join(result, c.nativeCgroup.Close())
		c.nativeCgroup = nil
	}
	if c.execStatus != nil {
		result = errors.Join(result, c.execStatus.Close())
		c.execStatus = nil
	}
	if c.stdin != nil {
		result = errors.Join(result, c.stdin.Close())
		c.stdin = nil
	}
	if c.stdout != nil {
		result = errors.Join(result, c.stdout.Close())
		c.stdout = nil
	}
	if c.stderr != nil {
		result = errors.Join(result, c.stderr.Close())
		c.stderr = nil
	}
	result = errors.Join(result, c.closeOutputWritersLocked())
	return result
}

func (c *command) closeOutputWritersLocked() error {
	var result error
	if c.stdoutChild != nil {
		result = errors.Join(result, c.stdoutChild.Close())
		c.stdoutChild = nil
	}
	if c.stderrChild != nil {
		result = errors.Join(result, c.stderrChild.Close())
		c.stderrChild = nil
	}
	return result
}

func (c *command) environment() []string {
	if c.instance.backend.policy.Mode == sandbox.Off {
		result := os.Environ()
		slices.Sort(result)
		return result
	}
	values := map[string]string{}
	for _, name := range c.instance.template.Environment.Inherit {
		if value, ok := os.LookupEnv(name); ok {
			values[name] = value
		}
	}
	for name, value := range c.instance.template.Environment.Set {
		values[name] = value
	}
	for name, value := range platformEnvironment() {
		values[name] = value
	}
	values["PATH"] = defaultProgramPath()
	temporary := c.instance.temporaryPath
	persistent := c.instance.persistentPath
	if c.instance.nativeFull {
		temporary = "/almagest/temp"
		persistent = "/almagest/state"
		values["ALMAGEST_SANDBOX_PROGRAM_SELF"] = "/almagest/program/" + string(c.programID)
	}
	values["ALMAGEST_SANDBOX_TEMP"] = temporary
	values["TMPDIR"] = temporary
	values["TMP"] = temporary
	values["TEMP"] = temporary
	values["HOME"] = temporary
	if c.instance.persistentPath != "" {
		values["ALMAGEST_SANDBOX_STATE"] = persistent
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	slices.Sort(result)
	return result
}

func defaultProgramPath() string {
	return platformProgramPath()
}

func (c *command) Start(ctx context.Context) error {
	if ctx == nil {
		return sandbox.ErrInvalid
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return sandbox.ErrConflict
	}
	if c.closed {
		c.mu.Unlock()
		return sandbox.ErrClosed
	}
	if err := c.instance.reserveCommand(c); err != nil {
		c.mu.Unlock()
		return err
	}
	c.started = true
	cmd := c.ensureCommandLocked()
	if c.setupErr == nil {
		c.setupErr = c.configureNativeLocked()
	}
	setupErr := c.setupErr
	c.mu.Unlock()
	if setupErr != nil {
		setupErr = errors.Join(setupErr, c.closeBeforeStart())
		c.finish(sandbox.Exit{}, setupErr)
		return setupErr
	}

	if err := ctx.Err(); err != nil {
		err = errors.Join(err, c.closeBeforeStart())
		c.finish(sandbox.Exit{Killed: true}, err)
		return err
	}
	if err := c.instance.ctx.Err(); err != nil {
		err = errors.Join(sandbox.ErrClosed, c.closeBeforeStart())
		c.finish(sandbox.Exit{Killed: true}, err)
		return err
	}
	boundary, err := startProcess(cmd, c.instance)
	if err != nil {
		c.mu.Lock()
		closeErr := errors.Join(c.closeNativeFilesLocked(), c.closeOutputWritersLocked())
		if c.execStatus != nil {
			closeErr = errors.Join(closeErr, c.execStatus.Close())
			c.execStatus = nil
		}
		c.mu.Unlock()
		err = errors.Join(fmt.Errorf("%w: %v", sandbox.ErrLaunch, sanitizeNativeError(err)), closeErr)
		if c.nativeCgroup != nil {
			err = errors.Join(err, c.nativeCgroup.Close())
			c.nativeCgroup = nil
		}
		c.finish(sandbox.Exit{}, err)
		return err
	}
	c.mu.Lock()
	outputCloseErr := c.closeOutputWritersLocked()
	c.mu.Unlock()
	if outputCloseErr != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		c.mu.Lock()
		cleanupErr := c.closeNativeFilesLocked()
		if c.execStatus != nil {
			cleanupErr = errors.Join(cleanupErr, c.execStatus.Close())
			c.execStatus = nil
		}
		c.mu.Unlock()
		if c.nativeCgroup != nil {
			cleanupErr = errors.Join(cleanupErr, c.nativeCgroup.Close())
			c.nativeCgroup = nil
		}
		launchErr := errors.Join(sandbox.ErrLaunch, outputCloseErr, cleanupErr)
		c.finish(sandbox.Exit{Killed: true}, launchErr)
		return launchErr
	}
	if !c.native {
		c.mu.Lock()
		closeErr := c.closeNativeFilesLocked()
		c.mu.Unlock()
		if closeErr != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			launchErr := errors.Join(sandbox.ErrLaunch, closeErr)
			c.finish(sandbox.Exit{Killed: true}, launchErr)
			return launchErr
		}
	}
	if c.nativeCgroup != nil {
		boundary = &commandProcessBoundary{processBoundary: boundary, cgroup: c.nativeCgroup}
		c.nativeCgroup = nil
	}
	c.mu.Lock()
	c.boundary = boundary
	c.mu.Unlock()
	if err = c.awaitNativeExec(ctx, boundary); err != nil {
		_ = boundary.Kill()
		waitErr := cmd.Wait()
		closeErr := boundary.Close()
		exit := decodeExit(cmd.ProcessState)
		c.mu.Lock()
		c.boundary = nil
		c.mu.Unlock()
		err = errors.Join(err, waitErr, closeErr)
		c.finish(exit, err)
		return err
	}
	if err = policy.Verify(c.instance.Status(), appliedFeatures(c.instance.Status())); err != nil {
		_ = boundary.Kill()
		waitErr := cmd.Wait()
		closeErr := boundary.Close()
		c.mu.Lock()
		c.boundary = nil
		c.mu.Unlock()
		err = errors.Join(err, waitErr, closeErr)
		c.finish(decodeExit(cmd.ProcessState), err)
		return err
	}
	go c.watch(ctx)
	return nil
}

type execStatusResult struct {
	data []byte
	err  error
}

func (c *command) awaitNativeExec(ctx context.Context, boundary processBoundary) error {
	c.mu.Lock()
	if !c.native {
		c.mu.Unlock()
		return nil
	}
	reader := c.execStatus
	c.execStatus = nil
	closeErr := c.closeNativeFilesLocked()
	c.mu.Unlock()
	if closeErr != nil {
		_ = reader.Close()
		return errors.Join(sandbox.ErrLaunch, closeErr)
	}
	result := make(chan execStatusResult, 1)
	go func() {
		data, err := readBounded(reader, 512)
		err = errors.Join(err, reader.Close())
		result <- execStatusResult{data: data, err: err}
	}()
	select {
	case value := <-result:
		if value.err != nil {
			return errors.Join(sandbox.ErrLaunch, value.err)
		}
		if len(value.data) != 0 {
			return fmt.Errorf("%w: %s", sandbox.ErrLaunch, sanitizeNativeText(string(value.data)))
		}
		return nil
	case <-ctx.Done():
		_ = reader.Close()
		_ = boundary.Kill()
		return ctx.Err()
	case <-c.instance.ctx.Done():
		_ = reader.Close()
		_ = boundary.Kill()
		return sandbox.ErrClosed
	}
}

func (c *command) watch(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			_ = c.kill(true)
		case <-c.instance.ctx.Done():
			_ = c.kill(true)
		case <-c.done:
		}
	}()
	_, _ = c.wait()
}

func (c *command) Wait() (sandbox.Exit, error) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return sandbox.Exit{}, sandbox.ErrConflict
	}
	done := c.done
	c.mu.Unlock()
	<-done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit, c.waitErr
}

func (c *command) wait() (sandbox.Exit, error) {
	c.mu.Lock()
	if c.waiting {
		done := c.done
		c.mu.Unlock()
		<-done
		return c.Wait()
	}
	c.waiting = true
	cmd := c.cmd
	c.mu.Unlock()
	err := cmd.Wait()
	exit := decodeExit(cmd.ProcessState)
	c.mu.Lock()
	if c.killRequest {
		exit.Killed = true
	}
	boundary := c.boundary
	c.mu.Unlock()
	if boundary != nil {
		err = errors.Join(err, boundary.Close())
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			err = &sandbox.ExitError{Exit: exit}
		} else if errors.Is(err, exec.ErrWaitDelay) {
			err = errors.Join(sandbox.ErrLaunch, err)
		}
	}
	c.finish(exit, err)
	return exit, err
}

func (c *command) finish(exit sandbox.Exit, err error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	c.exit, c.waitErr = exit, err
	close(c.done)
	c.mu.Unlock()
	c.instance.forgetCommand(c)
}

func (c *command) closePending() error {
	c.mu.Lock()
	if c.started || c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.closeBeforeStart()
	c.mu.Lock()
	c.exit = sandbox.Exit{Killed: true}
	c.waitErr = errors.Join(sandbox.ErrClosed, err)
	close(c.done)
	c.mu.Unlock()
	c.instance.forgetPendingCommand(c)
	return err
}

func (c *command) Signal(signal sandbox.Signal) error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return sandbox.ErrConflict
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		return sandbox.ErrClosed
	default:
	}
	boundary := c.boundary
	c.mu.Unlock()
	if signal == sandbox.Kill {
		return c.kill(true)
	}
	if boundary == nil {
		return sandbox.ErrUnavailable
	}
	return boundary.Signal(signal)
}

func (c *command) Kill() error { return c.kill(true) }

func (c *command) kill(mark bool) error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return sandbox.ErrConflict
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil
	default:
	}
	if mark {
		c.killRequest = true
	}
	boundary, process := c.boundary, c.cmd.Process
	c.mu.Unlock()
	if boundary != nil {
		return boundary.Kill()
	}
	if process != nil {
		return process.Kill()
	}
	return sandbox.ErrUnavailable
}

func (c *command) Done() <-chan struct{} { return c.done }
func (c *command) OutputLimit() int64    { return c.instance.template.Limits.OutputBytes }

func decodeExit(state *os.ProcessState) sandbox.Exit {
	if state == nil {
		return sandbox.Exit{}
	}
	exit := sandbox.Exit{Code: state.ExitCode()}
	exit.Signal = exitSignal(state.Sys())
	return exit
}

func sanitizeNativeError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "approved object unavailable"
	case errors.Is(err, os.ErrPermission):
		return "native operation denied"
	default:
		return "native operation failed"
	}
}

func sanitizeNativeText(value string) string {
	text := strings.Map(func(char rune) rune {
		if char < 32 || char == 127 {
			return -1
		}
		return char
	}, value)
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
