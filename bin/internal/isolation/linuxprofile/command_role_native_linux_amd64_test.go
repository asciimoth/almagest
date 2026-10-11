//go:build linux && amd64

package linuxprofile_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func TestNativeTrustedCommandHelper(t *testing.T) {
	if os.Getenv("ALMAGEST_PROFILE_TRUSTED_SYSTEM") != "1" {
		t.Skip("requires the dedicated trusted-backend container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile, err := os.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateFile.Close() })
	if err := os.Chown(state, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chown(state, 0, 0) })
	open := func(path string) *os.File {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	controlParent, controlChild, err := transport.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	callbackParent, callbackChild, err := transport.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	command, err := linuxprofile.CommandContext(ctx, linuxprofile.Plan{Role: linuxprofile.Command, UID: 65534, GID: 65534, Limits: linuxprofile.DefaultLimits()}, linuxprofile.Inputs{
		Launcher: open("/qualification/launcher"), Control: controlChild,
		Executable: open("/qualification/command-role"), Directory: stateFile, Cgroup: open("/sys/fs/cgroup"),
		Command: open("/qualification/command"), Callbacks: callbackChild,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = controlChild.Close()
	_ = callbackChild.Close()
	t.Cleanup(func() {
		_ = controlParent.Close()
		_ = callbackParent.Close()
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	childPID := launcherChildPID(t, command.Process.Pid, &output)
	bind := func(file *os.File) transport.Stream {
		stream, err := transport.BindSocket(file, transport.LinuxPeer{PID: int32(childPID), UID: 65534, GID: 65534})
		if err != nil {
			t.Fatal(err)
		}
		return stream
	}
	control, callback := bind(controlParent), bind(callbackParent)
	launch := policy.LaunchAuthority{Backend: "linux", CredentialClass: "container-root"}
	p := policy.Default(launch, policy.CommandAuthority{NodeCommands: []string{"node"}, Callbacks: []string{"callback"}})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}
	config := commandhelper.Config{
		Profiles:  map[string]commandhelper.Profile{"fixed": {Sandbox: "fixed", Executable: "/command", WorkingDirectory: "/state", MaxConcurrent: 1}},
		Commands:  map[string]commandhelper.Command{"node": {Profile: "fixed", AllowNodes: true}},
		Callbacks: map[string]commandhelper.Callback{"callback": {Profile: "fixed", Event: "created", ResourceType: protocol.ResourceTun}},
	}
	ready, err := commandhelper.SendBootstrap(ctx, control, roleentry.Bootstrap{Version: protocol.Version, Role: protocol.RoleCommandHelper, Session: session}, config)
	if err != nil || !ready.RestrictionsApplied {
		t.Fatalf("bootstrap=%+v %v output=%s", ready, err, output.Bytes())
	}
	peerReady := make(chan *transport.Peer, 1)
	peerErr := make(chan error, 1)
	go func() {
		peer, err := transport.NewControl(ctx, callback, session, nil)
		peerReady <- peer
		peerErr <- err
	}()
	runtimePeer, err := transport.NewControl(ctx, control, session, nil)
	if err != nil {
		t.Fatal(err)
	}
	callbackPeer := <-peerReady
	if err := <-peerErr; err != nil {
		t.Fatal(err)
	}
	var nodeResult protocol.CommandResult
	if err := runtimePeer.Call(ctx, protocol.OperationRunNodeCommand, protocol.NodeCommandRequest{
		Meta: protocol.RequestMeta{GraphEpoch: 1, OperationID: "node"}, CommandID: "node",
	}, &nodeResult); err != nil || nodeResult.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("node result=%+v %v", nodeResult, err)
	}
	resourceID, err := protocol.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	resource := protocol.ResourceRef{ID: resourceID, SessionID: session.ID, Type: protocol.ResourceTun, Generation: 1, GraphEpoch: 1}
	var callbackResult protocol.CommandResult
	if err := callbackPeer.Call(ctx, protocol.OperationRunCallback, protocol.CallbackRequest{
		Meta: protocol.RequestMeta{GraphEpoch: 1, OperationID: "callback"}, CallbackID: "callback", Resource: resource, Event: "created",
	}, &callbackResult); err != nil || callbackResult.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("callback result=%+v %v", callbackResult, err)
	}
	_ = runtimePeer.Close()
	_ = callbackPeer.Close()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err == nil || ctx.Err() != nil {
			t.Fatalf("helper exit=%v context=%v output=%s", err, ctx.Err(), output.Bytes())
		}
	case <-ctx.Done():
		t.Fatalf("helper did not exit after peer loss: %v output=%s", ctx.Err(), output.Bytes())
	}
}

func launcherChildPID(t *testing.T, launcherPID int, output *bytes.Buffer) int64 {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(launcherPID), "task", strconv.Itoa(launcherPID), "children"))
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 1 {
				pid, parseErr := strconv.ParseInt(fields[0], 10, 32)
				if parseErr == nil && pid > 0 {
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("launcher child identity unavailable for %d; output=%s", launcherPID, output.Bytes())
	return 0
}
