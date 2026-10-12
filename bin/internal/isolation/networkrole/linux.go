//go:build linux && amd64

// Package networkrole assembles trusted native brokers and restricted roles.
// It must never import the daemon runtime or node implementations.
package networkrole

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/callbackbridge"
	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/network"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	sysnetlinux "github.com/asciimoth/sysnet-linux"
)

// LinuxRole contains launch-selected held objects. Bootstrap owns their path
// trust checks, storage preparation, and separate cgroup provisioning. Inputs
// must not contain IPC files: this assembly creates all channels itself.
type LinuxRole struct {
	Plan   linuxprofile.Plan
	Inputs linuxprofile.Inputs
}

type LinuxWriter struct {
	LinuxRole
	Target string
}

type LinuxHelper struct {
	LinuxRole
	Config commandhelper.Config
}

// LinuxLaunch is trusted bootstrap state, not an RPC or regular config format.
// Call RunLinux only in the node-free process which will become N. It changes
// the current directory and process limits irreversibly; exit after it returns.
// The caller retains the held input files. Output must be a bounded, concurrent
// safe trusted sink; nil discards output. Children receive pipes, not the sink.
type LinuxLaunch struct {
	Runtime        LinuxRole
	Writer         *LinuxWriter
	Helper         *LinuxHelper
	RuntimeInput   roleentry.RuntimeInput
	RuntimeStorage *bootstrap.RuntimeStorageInput
	Policy         policy.Policy
	Native         sysnetlinux.SystemConfig
	Callbacks      callbackbridge.Bindings
	State          *os.File
	// Cgroups is the production role-cgroup owner. Cgroup is a qualification
	// fallback that places every process in one already bounded cgroup.
	Cgroups *linuxprofile.RoleCgroups
	Cgroup  *os.File
	Limits  linuxprofile.Limits
	Journal *supervision.Journal
	Admin   []admingate.EndpointConfig
	Output  io.Writer
}

// RunLinux owns the entire launched process set and all IPC/listener lifetimes.
// ready is called only after native recovery and runtime application readiness.
// No error switches to direct mode or launches a replacement child.
func RunLinux(parent context.Context, launch LinuxLaunch, ready func()) (result error) {
	if parent == nil {
		return supervision.ErrInvalidComposition
	}
	if err := launch.bindCgroups(); err != nil {
		return err
	}
	if err := launch.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	adminLost := make(chan struct{})
	defer func() {
		select {
		case <-adminLost:
			result = errors.Join(result, fmt.Errorf("%w: admin relay stopped", supervision.ErrRuntimeLost))
		default:
		}
	}()
	resources := &linuxResources{}
	defer func() { result = errors.Join(result, resources.close()) }()
	digest, err := launch.Policy.Digest()
	if err != nil {
		return err
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		return err
	}
	session := protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}
	if launch.Writer != nil {
		if err := (roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleConfigWriter, Session: session, Target: launch.Writer.Target}).Validate(protocol.RoleConfigWriter); err != nil {
			return err
		}
	}
	input := launch.RuntimeInput
	input.ConfigWritable = launch.Writer != nil
	input.StartupSnapshot = launch.RuntimeStorage.Snapshot != nil
	input.AdminEndpoints = nil
	for _, endpoint := range launch.Admin {
		input.AdminEndpoints = append(input.AdminEndpoints, roleentry.RuntimeAdminEndpoint{ID: endpoint.ID, Kind: string(endpoint.Kind)})
	}
	if err := input.Validate(); err != nil {
		return err
	}
	if err := bootstrap.PrepareRuntimeStorage(launch.Runtime.Inputs.Directory, launch.Runtime.Plan.UID, launch.Runtime.Plan.GID, *launch.RuntimeStorage); err != nil {
		return fmt.Errorf("prepare runtime storage: %w", err)
	}
	lanes := []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData, roleentry.LaneDiagnostics}
	if len(launch.Admin) != 0 {
		lanes = append(lanes, roleentry.LaneAdmin)
	}
	if launch.Writer != nil {
		lanes = append(lanes, roleentry.LaneConfig)
	}
	if launch.Helper != nil {
		lanes = append(lanes, roleentry.LaneCommand)
	}
	runtime, streams, err := resources.start(ctx, launch.Runtime, lanes, launch.Output)
	if err != nil {
		return fmt.Errorf("launch runtime: %w", err)
	}
	proof, err := roleentry.SendRuntimeBootstrap(ctx, streams[roleentry.LaneBootstrap], roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleRuntime, Session: session}, input)
	if err != nil {
		return fmt.Errorf("runtime bootstrap: %w", err)
	}
	if err := resources.supervise(runtime, proof, streams[roleentry.LaneBootstrap]); err != nil {
		return err
	}
	if launch.Writer != nil {
		writer, endpoints, err := resources.start(ctx, launch.Writer.LinuxRole, []roleentry.Lane{roleentry.LaneBootstrap}, launch.Output)
		if err != nil {
			return fmt.Errorf("launch writer: %w", err)
		}
		stream := endpoints[roleentry.LaneBootstrap]
		proof, err := roleentry.SendBootstrap(ctx, stream, roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleConfigWriter, Session: session, Target: launch.Writer.Target})
		if err != nil {
			return fmt.Errorf("writer bootstrap: %w", err)
		}
		if err := resources.supervise(writer, proof, stream); err != nil {
			return err
		}
		resources.bridge(streams[roleentry.LaneConfig], stream)
	}
	var callbackDispatcher *callbackbridge.Dispatcher
	if launch.Helper != nil {
		helperLanes := []roleentry.Lane{roleentry.LaneBootstrap}
		if !launch.Callbacks.Empty() {
			helperLanes = append(helperLanes, roleentry.LaneCallback)
		}
		helper, endpoints, err := resources.start(ctx, launch.Helper.LinuxRole, helperLanes, launch.Output)
		if err != nil {
			return fmt.Errorf("launch helper: %w", err)
		}
		stream := endpoints[roleentry.LaneBootstrap]
		proof, err := commandhelper.SendBootstrap(ctx, stream, roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleCommandHelper, Session: session}, launch.Helper.Config)
		if err != nil {
			return fmt.Errorf("helper bootstrap: %w", err)
		}
		if err := resources.supervise(helper, proof, stream); err != nil {
			return err
		}
		resources.bridge(streams[roleentry.LaneCommand], stream)
		if callbackStream := endpoints[roleentry.LaneCallback]; callbackStream != nil {
			peer, peerErr := transport.NewControl(ctx, callbackStream, session, nil)
			if peerErr != nil {
				return fmt.Errorf("start callback helper client: %w", peerErr)
			}
			callbackDispatcher, peerErr = callbackbridge.New(ctx, peer, session)
			if peerErr != nil {
				_ = peer.Close()
				return peerErr
			}
			defer callbackDispatcher.Close()
		}
	}
	if err := launch.prepareBackend(); err != nil {
		return err
	}
	launch.Native.Callbacks = launch.Callbacks.Native(callbackDispatcher, func(format string, values ...any) {
		if launch.Output != nil {
			_, _ = fmt.Fprintf(launch.Output, "network: "+format+"\n", values...)
		}
	})
	system, err := sysnetlinux.New(launch.Native)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, system.Close()) }()
	backend, err := network.NewSystemBackend(system, network.CaptureNativeIdentity)
	if err != nil {
		return err
	}
	broker, err := network.NewWithJournal(session, launch.Policy, backend, launch.Journal)
	if err != nil {
		return err
	}
	service, err := network.NewService(broker, transport.Lanes{Control: streams[roleentry.LaneBootstrap], Data: streams[roleentry.LaneData], Diagnostic: streams[roleentry.LaneDiagnostics]}, supervision.NativeReconciler{}, time.Second,
		supervision.Options{StartupTimeout: 15 * time.Second, ShutdownTimeout: 5 * time.Second, RecoveryTimeout: 5 * time.Second}, nil, resources.supervised...)
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- service.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-served:
			result = errors.Join(result, err)
		case <-time.After(10 * time.Second):
			result = errors.Join(result, supervision.ErrShutdownIncomplete)
		}
	}()
	var adminCleanup func()
	defer func() {
		if adminCleanup != nil {
			adminCleanup()
		}
	}()
	var admin *admingate.Broker
	prepareAdmin := func() error {
		if len(launch.Admin) == 0 {
			return nil
		}
		ids := make([]uint16, len(launch.Admin))
		for i := range launch.Admin {
			ids[i] = launch.Admin[i].ID
		}
		var err error
		admin, err = admingate.NewPreparedBroker(ctx, streams[roleentry.LaneAdmin], session, ids)
		if err != nil {
			return err
		}
		adminCleanup = func() {
			cancel()
			_ = admin.Close()
			admin.Wait()
		}
		go func() {
			admin.Wait()
			if ctx.Err() == nil {
				close(adminLost)
				cancel()
			}
		}()
		return nil
	}
	if err := prepareAdmin(); err != nil {
		return err
	}
	startAdmin := func() error {
		if admin == nil {
			return nil
		}
		endpoints, err := admingate.ListenAll(launch.Admin)
		if err != nil {
			return err
		}
		if err = admin.Start(endpoints); err != nil {
			for _, endpoint := range endpoints {
				_ = endpoint.Listener.Close()
			}
			return err
		}
		return nil
	}
	select {
	case <-service.Ready():
		if err := ctx.Err(); err != nil {
			return err
		}
		if !service.Status().Ready {
			return supervision.ErrStartupFailed
		}
		// Do not publish an admin endpoint until recovery and every required
		// role are ready. This makes the status returned through R truthful.
		if err := startAdmin(); err != nil {
			return err
		}
		if ready != nil {
			ready()
		}
	case err := <-served:
		served <- err // Keep exactly one final result for the deferred join.
		return supervision.ErrStartupFailed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-served:
		served <- err
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l LinuxLaunch) validate() error {
	if l.State == nil || l.Cgroup == nil && l.Cgroups == nil || l.Journal == nil || l.RuntimeStorage == nil || l.Runtime.Plan.Role != linuxprofile.Runtime || l.RuntimeInput.Validate() != nil || l.Policy.Validate() != nil {
		return supervision.ErrInvalidComposition
	}
	if l.Policy.Launch.Backend != "linux" || (linuxprofile.Plan{Role: linuxprofile.Runtime, UID: 1, GID: 1, Limits: l.Limits}).Validate() != nil {
		return supervision.ErrInvalidComposition
	}
	if l.Helper == nil && (len(l.Policy.Commands.NodeCommands) != 0 || len(l.Policy.Commands.Callbacks) != 0) {
		return supervision.ErrInvalidComposition
	}
	if len(l.Policy.Commands.Callbacks) != len(l.Callbacks.IDs()) || l.Helper != nil && len(l.Helper.Config.Callbacks) != len(l.Callbacks.IDs()) {
		return supervision.ErrInvalidComposition
	}
	for _, id := range l.Callbacks.IDs() {
		if !slices.Contains(l.Policy.Commands.Callbacks, id) || l.Helper == nil {
			return supervision.ErrInvalidComposition
		}
	}
	roles := []LinuxRole{l.Runtime}
	if l.Writer != nil {
		if l.Writer.Plan.Role != linuxprofile.Writer {
			return supervision.ErrInvalidComposition
		}
		roles = append(roles, l.Writer.LinuxRole)
	}
	if l.Helper != nil {
		if l.Helper.Plan.Role != linuxprofile.Command {
			return supervision.ErrInvalidComposition
		}
		roles = append(roles, l.Helper.LinuxRole)
	}
	seenUID, seenGID := map[uint32]bool{}, map[uint32]bool{}
	for _, role := range roles {
		in := role.Inputs
		if role.Plan.Validate() != nil || seenUID[role.Plan.UID] || seenGID[role.Plan.GID] || in.Launcher == nil || in.Executable == nil || in.Directory == nil || in.Cgroup == nil || in.Control != nil || in.Data != nil || in.Diagnostics != nil || in.Admin != nil || in.Config != nil || in.Commands != nil || in.Callbacks != nil {
			return supervision.ErrInvalidComposition
		}
		if (role.Plan.Role == linuxprofile.Command) != (in.Command != nil) {
			return supervision.ErrInvalidComposition
		}
		seenUID[role.Plan.UID], seenGID[role.Plan.GID] = true, true
	}
	return nil
}

func (l *LinuxLaunch) bindCgroups() error {
	if l == nil || l.Cgroups == nil {
		return nil
	}
	if l.Cgroup != nil || l.Runtime.Inputs.Cgroup != nil || l.Writer != nil && l.Writer.Inputs.Cgroup != nil || l.Helper != nil && l.Helper.Inputs.Cgroup != nil {
		return supervision.ErrInvalidComposition
	}
	var err error
	if l.Cgroup, err = l.Cgroups.File(linuxprofile.Network); err != nil {
		return err
	}
	if l.Runtime.Inputs.Cgroup, err = l.Cgroups.File(linuxprofile.Runtime); err != nil {
		return err
	}
	if l.Writer != nil {
		if l.Writer.Inputs.Cgroup, err = l.Cgroups.File(linuxprofile.Writer); err != nil {
			return err
		}
	}
	if l.Helper != nil {
		if l.Helper.Inputs.Cgroup, err = l.Cgroups.File(linuxprofile.Command); err != nil {
			return err
		}
	}
	return nil
}

func (l LinuxLaunch) prepareBackend() error {
	if l.Cgroups != nil {
		return l.Cgroups.EnterBackend(l.State, l.Limits)
	}
	return linuxprofile.PrepareBackend(l.State, l.Cgroup, l.Limits)
}

type linuxResources struct {
	files      []*os.File
	streams    []transport.Stream
	processes  []*linuxprofile.Process
	supervised []supervision.Process
	bridges    sync.WaitGroup
}

func (r *linuxResources) start(ctx context.Context, role LinuxRole, lanes []roleentry.Lane, output io.Writer) (*linuxprofile.Process, map[roleentry.Lane]transport.Stream, error) {
	parents, children := map[roleentry.Lane]*os.File{}, []*os.File{}
	inputs := role.Inputs
	for _, lane := range lanes {
		parent, child, err := transport.SocketPair()
		if err != nil {
			return nil, nil, err
		}
		r.files = append(r.files, parent, child)
		parents[lane], children = parent, append(children, child)
		switch lane {
		case roleentry.LaneBootstrap:
			inputs.Control = child
		case roleentry.LaneData:
			inputs.Data = child
		case roleentry.LaneDiagnostics:
			inputs.Diagnostics = child
		case roleentry.LaneAdmin:
			inputs.Admin = child
		case roleentry.LaneConfig:
			inputs.Config = child
		case roleentry.LaneCommand:
			inputs.Commands = child
		case roleentry.LaneCallback:
			inputs.Callbacks = child
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Supervision, not exec.CommandContext, owns shutdown after launch. Parent
	// cancellation must first request graceful stop, not kill R during a save.
	// The resource owner kills and joins children on every startup error.
	process, err := linuxprofile.Start(context.WithoutCancel(ctx), role.Plan, inputs, output, output)
	if err != nil {
		return nil, nil, err
	}
	r.processes = append(r.processes, process)
	for _, child := range children {
		_ = child.Close()
	}
	peer, err := process.Peer(ctx)
	if err != nil {
		return nil, nil, err
	}
	streams := map[roleentry.Lane]transport.Stream{}
	for lane, parent := range parents {
		stream, err := transport.BindSocket(parent, peer)
		if err != nil {
			return nil, nil, err
		}
		r.streams = append(r.streams, stream)
		streams[lane] = stream
	}
	return process, streams, nil
}

func (r *linuxResources) supervise(process *linuxprofile.Process, proof roleentry.Ready, stream transport.Stream) error {
	ready := make(chan supervision.ReadyResult, 1)
	ready <- supervision.ReadyResult{Role: proof.Role, Version: proof.Version, PolicyDigest: proof.PolicyDigest, RestrictionsApplied: proof.RestrictionsApplied}
	supervised, err := process.Supervise(ready, func(context.Context) error {
		// The broker or bridge may already have closed this lane. The adapter
		// still waits for both native exits, so closing is only a stop request.
		_ = stream.Close()
		return nil
	})
	if err == nil {
		r.supervised = append(r.supervised, supervised)
	}
	return err
}

func (r *linuxResources) bridge(a, b transport.Stream) {
	bridgeStreams(&r.bridges, a, b)
}

func (r *linuxResources) close() error {
	for _, stream := range r.streams {
		_ = stream.Close()
	}
	for _, file := range r.files {
		_ = file.Close()
	}
	for _, process := range r.processes {
		_ = process.Kill()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result error
	for _, process := range r.processes {
		if err := process.WaitStopped(ctx); err != nil {
			result = errors.Join(result, supervision.ErrShutdownIncomplete, err)
		}
	}
	r.bridges.Wait()
	return result
}
