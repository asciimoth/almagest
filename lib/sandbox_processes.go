package almagest

import (
	"context"
	"io"

	sandbox "github.com/asciimoth/gobox"
)

// trackedOwnerSandboxes decorates node capabilities with process lifecycle
// reporting. It does not change sandbox ownership or command behavior.
type trackedOwnerSandboxes struct {
	base    sandbox.OwnerSandboxes
	tracker *workerTracker
	node    uint64
}

func (s trackedOwnerSandboxes) Acquire(ctx context.Context, slot string, template sandbox.TemplateID) (sandbox.Sandbox, error) {
	instance, err := s.base.Acquire(ctx, slot, template)
	if err != nil {
		return nil, err
	}
	return trackedSandbox{Sandbox: instance, tracker: s.tracker, node: s.node}, nil
}

func (s trackedOwnerSandboxes) Remove(ctx context.Context, slot string) error {
	return s.base.Remove(ctx, slot)
}

type trackedSandbox struct {
	sandbox.Sandbox
	tracker *workerTracker
	node    uint64
}

func (s trackedSandbox) NewCommand(program sandbox.ProgramID, launch sandbox.Launch) (sandbox.Command, error) {
	command, err := s.Sandbox.NewCommand(program, launch)
	if err != nil {
		return nil, err
	}
	return &trackedSandboxCommand{base: command, tracker: s.tracker, node: s.node, program: string(program)}, nil
}

type trackedSandboxCommand struct {
	base    sandbox.Command
	tracker *workerTracker
	node    uint64
	program string
}

func (c *trackedSandboxCommand) StdinPipe() (io.WriteCloser, error) { return c.base.StdinPipe() }
func (c *trackedSandboxCommand) StdoutPipe() (io.ReadCloser, error) { return c.base.StdoutPipe() }
func (c *trackedSandboxCommand) StderrPipe() (io.ReadCloser, error) { return c.base.StderrPipe() }

func (c *trackedSandboxCommand) Start(ctx context.Context) error {
	if err := c.base.Start(ctx); err != nil {
		return err
	}
	c.tracker.trackSandboxProcess(c.node, c.program, c.base.Done())
	return nil
}

func (c *trackedSandboxCommand) Wait() (sandbox.Exit, error)        { return c.base.Wait() }
func (c *trackedSandboxCommand) Signal(signal sandbox.Signal) error { return c.base.Signal(signal) }
func (c *trackedSandboxCommand) Kill() error                        { return c.base.Kill() }
func (c *trackedSandboxCommand) Done() <-chan struct{}              { return c.base.Done() }
