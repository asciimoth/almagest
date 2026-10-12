//go:build windows

package networkrole

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/network"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/almagest/bin/internal/isolation/windowsprofile"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/sys/windows"
)

type WindowsRole struct {
	Plan       windowsprofile.Plan
	Identity   *windowsprofile.Identity
	Executable string
	Storage    string
}

type WindowsWriter struct {
	WindowsRole
	Target string
}

// WindowsLaunch is trusted, resolved bootstrap state. It contains no mutable
// regular-config selector. RunWindows must execute in the trusted N process.
type WindowsLaunch struct {
	Runtime        WindowsRole
	Writer         *WindowsWriter
	RuntimeInput   roleentry.RuntimeInput
	RuntimeStorage *bootstrap.RuntimeStorageInput
	RuntimeDir     *os.File
	Policy         policy.Policy
	Native         sysnetwindows.SystemConfig
	Journal        *supervision.Journal
	Admin          []admingate.EndpointConfig
}

// RunWindows launches restricted R and optional C, owns their private pipes,
// runs the native backend in trusted N, and publishes admin listeners only
// after recovery and application readiness.
func RunWindows(parent context.Context, launch WindowsLaunch, ready func()) (result error) {
	if parent == nil {
		return supervision.ErrInvalidComposition
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
	resources := &windowsResources{}
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
	if err := bootstrap.PrepareRuntimeStorage(launch.RuntimeDir, 0, 0, *launch.RuntimeStorage); err != nil {
		return fmt.Errorf("prepare runtime storage: %w", err)
	}
	lanes := []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData, roleentry.LaneDiagnostics}
	if len(launch.Admin) != 0 {
		lanes = append(lanes, roleentry.LaneAdmin)
	}
	if launch.Writer != nil {
		lanes = append(lanes, roleentry.LaneConfig)
	}
	runtime, streams, err := resources.start(ctx, launch.Runtime, lanes)
	if err != nil {
		return fmt.Errorf("launch runtime: %w", err)
	}
	proof, err := roleentry.SendRuntimeBootstrap(ctx, streams[roleentry.LaneBootstrap], roleentry.Bootstrap{
		Version: protocol.Version, Role: protocol.RoleRuntime, Session: session,
	}, input)
	if err != nil {
		return fmt.Errorf("runtime bootstrap: %w", err)
	}
	if err := resources.supervise(runtime, proof, streams[roleentry.LaneBootstrap]); err != nil {
		return err
	}
	if launch.Writer != nil {
		writer, endpoints, startErr := resources.start(ctx, launch.Writer.WindowsRole, []roleentry.Lane{roleentry.LaneBootstrap})
		if startErr != nil {
			return fmt.Errorf("launch writer: %w", startErr)
		}
		stream := endpoints[roleentry.LaneBootstrap]
		writerProof, bootstrapErr := roleentry.SendBootstrap(ctx, stream, roleentry.Bootstrap{
			Version: protocol.Version, Role: protocol.RoleConfigWriter, Session: session, Target: launch.Writer.Target,
		})
		if bootstrapErr != nil {
			return fmt.Errorf("writer bootstrap: %w", bootstrapErr)
		}
		if err := resources.supervise(writer, writerProof, stream); err != nil {
			return err
		}
		resources.bridge(streams[roleentry.LaneConfig], stream)
	}
	system, err := sysnetwindows.New(launch.Native)
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
	service, err := network.NewService(broker, transport.Lanes{Control: streams[roleentry.LaneBootstrap], Data: streams[roleentry.LaneData], Diagnostic: streams[roleentry.LaneDiagnostics]},
		supervision.NativeReconciler{}, time.Second, supervision.Options{StartupTimeout: 15 * time.Second, ShutdownTimeout: 5 * time.Second, RecoveryTimeout: 30 * time.Second}, nil, resources.supervised...)
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- service.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case serviceErr := <-served:
			result = errors.Join(result, serviceErr)
		case <-time.After(10 * time.Second):
			result = errors.Join(result, supervision.ErrShutdownIncomplete)
		}
	}()

	var admin *admingate.Broker
	if len(launch.Admin) != 0 {
		ids := make([]uint16, len(launch.Admin))
		for index := range launch.Admin {
			ids[index] = launch.Admin[index].ID
		}
		admin, err = admingate.NewPreparedBroker(ctx, streams[roleentry.LaneAdmin], session, ids)
		if err != nil {
			return err
		}
		defer func() {
			_ = admin.Close()
			admin.Wait()
		}()
		go func() {
			admin.Wait()
			if ctx.Err() == nil {
				close(adminLost)
				cancel()
			}
		}()
	}
	select {
	case <-service.Ready():
		if err := ctx.Err(); err != nil {
			return err
		}
		if !service.Status().Ready {
			return supervision.ErrStartupFailed
		}
		if admin != nil {
			endpoints, listenErr := admingate.ListenAll(launch.Admin)
			if listenErr != nil {
				return listenErr
			}
			if startErr := admin.Start(endpoints); startErr != nil {
				for _, endpoint := range endpoints {
					_ = endpoint.Listener.Close()
				}
				return startErr
			}
		}
		if ready != nil {
			ready()
		}
	case serviceErr := <-served:
		served <- serviceErr
		return supervision.ErrStartupFailed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case serviceErr := <-served:
		served <- serviceErr
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l WindowsLaunch) validate() error {
	if l.Runtime.Plan.Role != windowsprofile.Runtime || l.Runtime.Plan.Validate() != nil || l.Runtime.Identity == nil || l.Runtime.Executable == "" || l.Runtime.Storage == "" ||
		l.RuntimeDir == nil || l.RuntimeStorage == nil || l.Journal == nil || l.Policy.Validate() != nil || l.Policy.Launch.Backend != "windows" ||
		l.RuntimeInput.Validate() != nil || len(l.Policy.Commands.NodeCommands) != 0 || len(l.Policy.Commands.Callbacks) != 0 {
		return supervision.ErrInvalidComposition
	}
	runtimePath, err := filepath.Abs(l.Runtime.Storage)
	heldPath, heldErr := filepath.Abs(l.RuntimeDir.Name())
	if err != nil || heldErr != nil || !strings.EqualFold(filepath.Clean(runtimePath), filepath.Clean(heldPath)) {
		return supervision.ErrInvalidComposition
	}
	if l.Writer != nil {
		if l.Writer.Plan.Role != windowsprofile.Writer || l.Writer.Plan.Validate() != nil || l.Writer.Identity == nil || l.Writer.Identity == l.Runtime.Identity ||
			l.Writer.Executable == "" || l.Writer.Storage == "" || l.Writer.Target == "" {
			return supervision.ErrInvalidComposition
		}
	}
	return nil
}

type windowsResources struct {
	files      []*os.File
	streams    []transport.Stream
	processes  []*windowsprofile.Process
	supervised []supervision.Process
	bridges    sync.WaitGroup
}

func (r *windowsResources) start(ctx context.Context, role WindowsRole, lanes []roleentry.Lane) (*windowsprofile.Process, map[roleentry.Lane]transport.Stream, error) {
	parents := make(map[roleentry.Lane]*os.File, len(lanes))
	children := make([]*os.File, 0, len(lanes))
	handles := make([]windows.Handle, 0, len(lanes))
	for _, lane := range lanes {
		parent, child, err := transport.PipePair()
		if err != nil {
			return nil, nil, err
		}
		r.files = append(r.files, parent, child)
		parents[lane] = parent
		children = append(children, child)
		handles = append(handles, windows.Handle(child.Fd()))
	}
	process, err := windowsprofile.Launch(ctx, windowsprofile.Request{
		Plan: role.Plan, Identity: role.Identity, Executable: role.Executable, Storage: role.Storage, Pipes: handles, Lanes: lanes,
	})
	if err != nil {
		return nil, nil, err
	}
	r.processes = append(r.processes, process)
	for _, child := range children {
		_ = child.Close()
	}
	streams := make(map[roleentry.Lane]transport.Stream, len(parents))
	for lane, parent := range parents {
		stream, bindErr := transport.BindPipe(parent)
		if bindErr != nil {
			return nil, nil, bindErr
		}
		r.streams = append(r.streams, stream)
		streams[lane] = stream
	}
	return process, streams, nil
}

func (r *windowsResources) supervise(process *windowsprofile.Process, proof roleentry.Ready, stream transport.Stream) error {
	ready := make(chan supervision.ReadyResult, 1)
	ready <- supervision.ReadyResult{Role: proof.Role, Version: proof.Version, PolicyDigest: proof.PolicyDigest, RestrictionsApplied: proof.RestrictionsApplied}
	supervised, err := process.Supervise(ready, func(context.Context) error {
		_ = stream.Close()
		return nil
	})
	if err == nil {
		r.supervised = append(r.supervised, supervised)
	}
	return err
}

func (r *windowsResources) bridge(left, right transport.Stream) {
	bridgeStreams(&r.bridges, left, right)
}

func (r *windowsResources) close() error {
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
		if _, err := process.Wait(ctx); err != nil {
			result = errors.Join(result, supervision.ErrShutdownIncomplete, err)
		}
		result = errors.Join(result, process.Close())
	}
	r.bridges.Wait()
	return result
}
