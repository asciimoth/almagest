package nodekit

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	sandbox "github.com/asciimoth/gobox"
)

// These tests keep the portable gobox contract in the native Windows
// qualification suite. The gobox module has the complete independent tests.
func TestGoboxPortablePaths(t *testing.T) {
	for _, name := range []string{"../x", "a/../b", "/x", "C:/x", `a\b`, "a//b", "a/./b", "a/", "a:", "NUL.txt", "com1", "COM¹", "lpt9.ext", "foo.", "foo ", "a\x00b", "a\nb", "CONIN$"} {
		if err := sandbox.ValidatePath(sandbox.Path{Rel: name}, true); !errors.Is(err, sandbox.ErrInvalid) {
			t.Errorf("accepted %q: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "a", "a/b.txt", "commander", "snow/a", "two words"} {
		if err := sandbox.ValidatePath(sandbox.Path{Rel: name}, true); err != nil {
			t.Errorf("rejected %q: %v", name, err)
		}
	}
	if sandbox.ValidatePath(sandbox.Path{}, false) == nil || sandbox.ValidatePath(sandbox.Path{Root: 9, Rel: "a"}, true) == nil {
		t.Fatal("accepted root mutation or unknown root")
	}
}

func TestGoboxLoopbackEndpoints(t *testing.T) {
	for _, address := range []string{"localhost:80", "0.0.0.0:80", "[::]:80", "[::ffff:127.0.0.1]:80", "[::1%eth0]:80", "192.0.2.1:80", "127.0.0.1:0", "127.1:80"} {
		if err := sandbox.ValidateEndpoint("tcp", address, false); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:80", "[::1]:80"} {
		if err := sandbox.ValidateEndpoint("tcp", address, false); err != nil {
			t.Error(err)
		}
	}
	if sandbox.ValidateEndpoint("udp4", "127.0.0.1:0", true) != nil ||
		sandbox.ValidateEndpoint("tcp4", "[::1]:80", true) == nil ||
		sandbox.ValidateEndpoint("unix", "127.0.0.1:80", true) == nil {
		t.Fatal("incorrect protocol validation")
	}
}

func TestGoboxTypedInputs(t *testing.T) {
	valid := []sandbox.Value{
		{Kind: sandbox.TextValue, Text: "hello"},
		{Kind: sandbox.IntegerValue, Int: -1},
		{Kind: sandbox.BooleanValue, Bool: true},
		{Kind: sandbox.PathValue, Path: sandbox.Path{Root: sandbox.Persistent, Rel: "state"}},
		{Kind: sandbox.ProgramValue, Program: "tor"},
		{Kind: sandbox.EndpointValue, Endpoint: "127.0.0.1:9050"},
	}
	for _, value := range valid {
		if err := value.Validate(); err != nil {
			t.Errorf("%+v: %v", value, err)
		}
	}
	invalid := []sandbox.Value{
		{},
		{Kind: sandbox.TextValue, Int: 1},
		{Kind: sandbox.IntegerValue, Bool: true},
		{Kind: sandbox.ProgramValue, Program: "/bin/sh"},
		{Kind: sandbox.PathValue, Path: sandbox.Path{Rel: "../x"}},
		{Kind: sandbox.TextValue, Text: "a\x00b"},
	}
	for _, value := range invalid {
		if value.Validate() == nil {
			t.Errorf("accepted %+v", value)
		}
	}
}

func TestGoboxOpenOptions(t *testing.T) {
	invalid := []sandbox.OpenOptions{
		{},
		{Read: true, Create: true},
		{Write: true, Exclusive: true},
		{Write: true, Append: true, Truncate: true},
		{Write: true, Mode: 0o4000},
	}
	for _, options := range invalid {
		if options.Validate() == nil {
			t.Errorf("accepted %+v", options)
		}
	}
	valid := sandbox.OpenOptions{Read: true, Write: true, Create: true, Exclusive: true, Mode: 0o600}
	if valid.Validate() != nil {
		t.Fatal("valid options rejected")
	}
}

type contractOutputCommand struct {
	sandbox.Command
	output           []byte
	stdoutR, stderrR *io.PipeReader
	stdoutW, stderrW *io.PipeWriter
	done             chan struct{}
	once             sync.Once
	killed           bool
	mu               sync.Mutex
}

func (command *contractOutputCommand) StdoutPipe() (io.ReadCloser, error) {
	command.stdoutR, command.stdoutW = io.Pipe()
	return command.stdoutR, nil
}

func (command *contractOutputCommand) StderrPipe() (io.ReadCloser, error) {
	command.stderrR, command.stderrW = io.Pipe()
	return command.stderrR, nil
}

func (command *contractOutputCommand) Start(context.Context) error {
	command.done = make(chan struct{})
	go func() {
		var group sync.WaitGroup
		for _, writer := range []*io.PipeWriter{command.stdoutW, command.stderrW} {
			if writer != nil {
				group.Go(func() {
					_, _ = writer.Write(command.output)
					_ = writer.Close()
				})
			}
		}
		group.Wait()
		close(command.done)
	}()
	return nil
}

func (command *contractOutputCommand) Wait() (sandbox.Exit, error) {
	<-command.done
	command.mu.Lock()
	defer command.mu.Unlock()
	return sandbox.Exit{Killed: command.killed}, nil
}

func (command *contractOutputCommand) Kill() error {
	command.mu.Lock()
	command.killed = true
	command.mu.Unlock()
	command.once.Do(func() {
		for _, writer := range []*io.PipeWriter{command.stdoutW, command.stderrW} {
			if writer != nil {
				_ = writer.Close()
			}
		}
	})
	return nil
}

func TestGoboxBoundedOutput(t *testing.T) {
	for _, combined := range []bool{false, true} {
		command := &contractOutputCommand{output: []byte("hello")}
		var data []byte
		var err error
		if combined {
			data, err = sandbox.CombinedOutput(context.Background(), command, 100)
		} else {
			data, err = sandbox.Output(context.Background(), command, 100)
		}
		expected := "hello"
		if combined {
			expected += "hello"
		}
		if err != nil || string(data) != expected {
			t.Fatalf("%q %v", data, err)
		}

		command = &contractOutputCommand{output: make([]byte, 10000)}
		if combined {
			data, err = sandbox.CombinedOutput(context.Background(), command, 5)
		} else {
			data, err = sandbox.Output(context.Background(), command, 5)
		}
		if len(data) != 5 || !errors.Is(err, sandbox.ErrQuota) || !command.killed {
			t.Fatalf("length=%d killed=%t error=%v", len(data), command.killed, err)
		}
	}
}
