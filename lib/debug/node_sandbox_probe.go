package debugnodes

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/pasta/pasta"
)

const (
	// NodeTypeSandboxProbe is a debug-only process-launch qualification node.
	NodeTypeSandboxProbe = "almagest/SandboxProbe"

	sandboxProbeTemplate = sandbox.TemplateID("qualification")
	sandboxProbeProgram  = sandbox.ProgramID("qualification-helper")
)

// SandboxProbeClass creates a debug-only node that starts one fixed program
// through its node-bound sandbox capability. Protected policy selects the
// executable and its arguments.
type SandboxProbeClass struct {
	Runtime nodekit.Runtime
}

func (SandboxProbeClass) ClassName() string { return NodeTypeSandboxProbe }
func (SandboxProbeClass) ShortDescription() string {
	return "DEBUG subprogram sandbox probe"
}
func (SandboxProbeClass) LongDescription() string {
	return "Tests the qualification sandbox."
}
func (SandboxProbeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{}}
}
func (c SandboxProbeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &sandboxProbeNode{Base: nodekit.Base{Runtime: c.Runtime}}, nil
}

type sandboxProbeNode struct {
	nodekit.Base

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (n *sandboxProbeNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	_ = n.SetLabel("sandbox probe")
	return nil
}

func (n *sandboxProbeNode) OnReady() error {
	ctx, cancel := context.WithCancel(n.Context())
	n.mu.Lock()
	if n.cancel != nil {
		n.cancel()
	}
	n.cancel = cancel
	n.mu.Unlock()
	return n.SpawnWorker("sandbox probe", func() { n.run(ctx) })
}

func (n *sandboxProbeNode) OnStop() {
	n.mu.Lock()
	cancel := n.cancel
	n.cancel = nil
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (n *sandboxProbeNode) run(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if n.Logger != nil {
		n.Logger.Debugf("sandbox probe started")
	}
	owners, err := n.Sandboxes(ctx)
	if err != nil {
		n.reportFailure("bind owner", err)
		return
	}
	instance, err := owners.Acquire(ctx, "probe", sandboxProbeTemplate)
	if err != nil {
		n.reportFailure("acquire sandbox", err)
		return
	}
	defer func() {
		if closeErr := instance.Close(); closeErr != nil && n.Logger != nil {
			n.Logger.Errf("sandbox probe cleanup failed: %v", closeErr)
		}
	}()
	command, err := instance.NewCommand(sandboxProbeProgram, sandbox.Launch{})
	if err != nil {
		n.reportFailure("create command", err)
		return
	}
	output, err := sandbox.CombinedOutput(ctx, command, 64<<10)
	if err != nil {
		if len(output) != 0 {
			n.reportFailure("run command", fmt.Errorf("%w; output: %s", err, output))
		} else {
			n.reportFailure("run command", err)
		}
		return
	}
	if n.Logger != nil {
		n.Logger.Infof("sandbox probe passed: %s", output)
	}
}

func (n *sandboxProbeNode) reportFailure(operation string, err error) {
	if n.Logger != nil {
		n.Logger.Errf("sandbox probe failed: %s: %v", operation, err)
	}
}
