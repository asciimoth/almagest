//go:build linux && amd64

package linuxprofile_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/network"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/almagest/bin/internal/nativebackend"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"github.com/asciimoth/sysnet-linux/routing"
	"github.com/coder/websocket"
)

// TestNativeCompleteDaemon starts the real daemon engine in R and gives it
// only authenticated broker lanes. The test observes readiness through N's
// public listener, executes a command through H, then removes N and requires R
// to die.
func TestNativeCompleteDaemon(t *testing.T) {
	nativeCompleteDaemonEnvironment(t)
	if os.Getenv("ALMAGEST_PROFILE_TRUSTED_SYSTEM") != "1" {
		t.Skip("requires the dedicated trusted-backend container")
	}
	if os.Getenv("ALMAGEST_COMPLETE_DAEMON_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeCompleteDaemon$", "-test.v")
		cmd.Env = append(os.Environ(), "ALMAGEST_COMPLETE_DAEMON_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("complete daemon: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	open := func(path string) *os.File {
		t.Helper()
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := open(state)
	regularConfig := []byte("{\n  \"nodes\": {\n    \"Qualification Command\": {\n      \"Class\": \"almagest/Command\",\n      \"command\": \"fixed\",\n    },\n  },\n}\n")
	protectedConfig := []byte("{\n  \"CommandRunner\": {\n    \"Commands\": {\n      \"fixed\": {\"Command\": [\"/command\"], \"AllowNodes\": true},\n    },\n  },\n}\n")
	for name, data := range map[string][]byte{
		roleentry.RuntimeRegularConfigFile:   regularConfig,
		roleentry.RuntimeProtectedConfigFile: protectedConfig,
	} {
		if err := os.WriteFile(filepath.Join(state, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(filepath.Join(state, name), 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(state, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chown(state, 0, 0)
		_ = os.Chmod(state, 0o700)
		// These private subdirectories are mode 0700. Change their owners before
		// Walk tries to read them because trusted N has CHOWN but no DAC override.
		for _, name := range []string{"tls-builtin-ca", "snapshots"} {
			directory := filepath.Join(state, name)
			_ = os.Chown(directory, 0, 0)
			_ = os.Chmod(directory, 0o700)
		}
		walkErr := filepath.Walk(state, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := os.Lchown(path, 0, 0); err != nil {
				return err
			}
			if info.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return nil
		})
		if walkErr != nil {
			t.Errorf("restore test storage ownership: %v", walkErr)
		}
	})

	parents := make([]*os.File, 6)
	children := make([]*os.File, 6)
	for index := range parents {
		var err error
		parents[index], children[index], err = transport.SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = parents[index].Close(); _ = children[index].Close() })
	}
	var output bytes.Buffer
	runtimeProcess, err := linuxprofile.Start(ctx, linuxprofile.Plan{Role: linuxprofile.Runtime, UID: 65534, GID: 65534, Limits: linuxprofile.DefaultLimits()}, linuxprofile.Inputs{
		Launcher: open("/qualification/launcher"), Executable: open("/qualification/almagestd"),
		Directory: stateFile, Cgroup: open("/sys/fs/cgroup"), Control: children[0],
		Data: children[1], Diagnostics: children[2], Admin: children[3], Config: children[4], Commands: children[5],
	}, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		_ = child.Close()
	}
	finished := make(chan struct{})
	var waitErr error
	go func() { waitErr = runtimeProcess.Wait(context.Background()); close(finished) }()
	t.Cleanup(func() {
		cancel()
		_ = runtimeProcess.Kill()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Errorf("daemon did not exit; output=%s", output.Bytes())
		}
	})

	runtimePeer, err := runtimeProcess.Peer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	streams := make([]transport.Stream, len(parents))
	for index, parent := range parents {
		stream, bindErr := transport.BindSocket(parent, runtimePeer)
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		streams[index] = stream
	}

	p := policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: "container-root"}, policy.CommandAuthority{NodeCommands: []string{"fixed"}})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}
	input := roleentry.RuntimeInput{Version: protocol.Version, ConfigWritable: true, AdminEndpoints: []roleentry.RuntimeAdminEndpoint{{ID: 1, Kind: "web"}}}
	runtimeProof, err := roleentry.SendRuntimeBootstrap(ctx, streams[0], roleentry.Bootstrap{
		Version: protocol.Version, Role: protocol.RoleRuntime, Session: session,
	}, input)
	if err != nil {
		t.Fatalf("runtime bootstrap: %v; output=%s", err, output.Bytes())
	}

	helperParent, helperChild, err := transport.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helperParent.Close(); _ = helperChild.Close() })
	helperState := t.TempDir()
	if err := os.Chmod(helperState, 0o700); err != nil {
		t.Fatal(err)
	}
	helperStateFile := open(helperState)
	if err := os.Chown(helperState, 65532, 65532); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chown(helperState, 0, 0)
		_ = os.Chmod(helperState, 0o700)
	})
	var helperOutput bytes.Buffer
	helperProcess, err := linuxprofile.Start(ctx, linuxprofile.Plan{Role: linuxprofile.Command, UID: 65532, GID: 65532, Limits: linuxprofile.DefaultLimits()}, linuxprofile.Inputs{
		Launcher: open("/qualification/launcher"), Control: helperChild,
		Executable: open("/qualification/command-role"), Directory: helperStateFile, Cgroup: open("/sys/fs/cgroup"),
		Command: open("/qualification/command"),
	}, &helperOutput, &helperOutput)
	if err != nil {
		t.Fatal(err)
	}
	_ = helperChild.Close()
	helperFinished := make(chan struct{})
	var helperWaitErr error
	go func() { helperWaitErr = helperProcess.Wait(context.Background()); close(helperFinished) }()
	t.Cleanup(func() {
		_ = helperProcess.Kill()
		select {
		case <-helperFinished:
		case <-time.After(5 * time.Second):
			t.Errorf("command helper did not exit; output=%s", helperOutput.Bytes())
		}
	})
	helperPeer, err := helperProcess.Peer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	helperStream, err := transport.BindSocket(helperParent, helperPeer)
	if err != nil {
		t.Fatal(err)
	}
	helperConfig := commandhelper.Config{
		Profiles: map[string]commandhelper.Profile{"fixed": {
			Sandbox: "fixed", Executable: "/command", WorkingDirectory: "/state", MaxConcurrent: 1,
		}},
		Commands: map[string]commandhelper.Command{"fixed": {Profile: "fixed", AllowNodes: true}},
	}
	ready, err := commandhelper.SendBootstrap(ctx, helperStream, roleentry.Bootstrap{
		Version: protocol.Version, Role: protocol.RoleCommandHelper, Session: session,
	}, helperConfig)
	if err != nil || !ready.RestrictionsApplied {
		t.Fatalf("command helper bootstrap=%+v err=%v output=%s", ready, err, helperOutput.Bytes())
	}
	// The launcher requires a private 0700 role directory. After the final H
	// restrictions are installed, let H write and let the trusted harness search
	// for the fixed marker. Neither side receives read access to the other's data.
	if err := os.Chown(helperState, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(helperState, 0o733); err != nil {
		t.Fatal(err)
	}
	var closeBridge sync.Once
	stopBridge := func() {
		closeBridge.Do(func() {
			_ = streams[5].Close()
			_ = helperStream.Close()
		})
	}
	t.Cleanup(stopBridge)
	for _, direction := range [][2]transport.Stream{{streams[5], helperStream}, {helperStream, streams[5]}} {
		destination, source := direction[0], direction[1]
		go func() {
			_, _ = io.Copy(destination, source)
			stopBridge()
		}()
	}

	writerState := t.TempDir()
	if err := os.Chmod(writerState, 0o700); err != nil {
		t.Fatal(err)
	}
	configTarget := filepath.Join(writerState, "config.hujson")
	if err := os.WriteFile(configTarget, regularConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	writerStateFile := open(writerState)
	t.Cleanup(func() { _ = os.Chown(writerState, 0, 0); _ = os.Chmod(writerState, 0o700) })
	// Transfer the file first: N has CHOWN, not a DAC bypass for C's directory.
	for _, path := range []string{configTarget, writerState} {
		if err := os.Chown(path, 65533, 65533); err != nil {
			t.Fatal(err)
		}
	}
	writerParent, writerChild, err := transport.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerParent.Close(); _ = writerChild.Close() })
	var writerOutput bytes.Buffer
	writerProcess, err := linuxprofile.Start(ctx, linuxprofile.Plan{Role: linuxprofile.Writer, UID: 65533, GID: 65533, Limits: linuxprofile.DefaultLimits()}, linuxprofile.Inputs{
		Launcher: open("/qualification/launcher"), Control: writerChild,
		Executable: open("/qualification/config-writer-role"), Directory: writerStateFile, Cgroup: open("/sys/fs/cgroup"),
	}, &writerOutput, &writerOutput)
	if err != nil {
		t.Fatal(err)
	}
	_ = writerChild.Close()
	writerDone := make(chan struct{})
	var writerWaitErr error
	go func() { writerWaitErr = writerProcess.Wait(context.Background()); close(writerDone) }()
	t.Cleanup(func() {
		_ = writerProcess.Kill()
		select {
		case <-writerDone:
		case <-time.After(5 * time.Second):
			t.Errorf("config writer did not exit; output=%s", writerOutput.Bytes())
		}
	})
	writerPeer, err := writerProcess.Peer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writerStream, err := transport.BindSocket(writerParent, writerPeer)
	if err != nil {
		t.Fatal(err)
	}
	writerProof, err := roleentry.SendBootstrap(ctx, writerStream, roleentry.Bootstrap{
		Version: protocol.Version, Role: protocol.RoleConfigWriter, Session: session, Target: "config.hujson",
	})
	if err != nil {
		t.Fatalf("config writer bootstrap: %v; output=%s", err, writerOutput.Bytes())
	}
	var closeWriterBridge sync.Once
	stopWriterBridge := func() {
		closeWriterBridge.Do(func() { _ = streams[4].Close(); _ = writerStream.Close() })
	}
	t.Cleanup(stopWriterBridge)
	for _, direction := range [][2]transport.Stream{{streams[4], writerStream}, {writerStream, streams[4]}} {
		destination, source := direction[0], direction[1]
		go func() { _, _ = io.Copy(destination, source); stopWriterBridge() }()
	}

	settings, err := nativebackend.ParseLinuxFileConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	nativeConfig := settings.Native(t.Logf)
	nativeConfig.RoutingMonitorNamespace = routing.CurrentNamespace
	system, err := linuxsysnet.New(nativeConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Close() })
	backend, err := network.NewSystemBackend(system, network.CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := supervision.OpenJournal(filepath.Join(t.TempDir(), "complete-daemon-recovery.json"), 64)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := network.NewWithJournal(session, p, backend, journal)
	if err != nil {
		t.Fatal(err)
	}
	adapt := func(proof roleentry.Ready, stream transport.Stream, launched *linuxprofile.Process) *supervision.StartedProcess {
		ack := make(chan supervision.ReadyResult, 1)
		ack <- supervision.ReadyResult{Role: proof.Role, Version: proof.Version, PolicyDigest: proof.PolicyDigest, RestrictionsApplied: proof.RestrictionsApplied}
		process, err := launched.Supervise(ack, func(context.Context) error {
			_ = stream.Close()
			return nil // The adapter still requires the native exit observation.
		})
		if err != nil {
			t.Fatal(err)
		}
		return process
	}
	writerAck := make(chan supervision.ReadyResult, 1)
	writerAck <- supervision.ReadyResult{Role: writerProof.Role, Version: writerProof.Version, PolicyDigest: writerProof.PolicyDigest, RestrictionsApplied: writerProof.RestrictionsApplied}
	writerSupervised, err := writerProcess.Supervise(writerAck, func(context.Context) error { return writerStream.Close() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writerProcess.Supervise(writerAck, func(context.Context) error { return nil }); err == nil {
		t.Fatal("duplicate writer supervision accepted")
	}
	service, err := network.NewService(broker, transport.Lanes{Control: streams[0], Data: streams[1], Diagnostic: streams[2]},
		supervision.NativeReconciler{}, time.Second,
		supervision.Options{StartupTimeout: 10 * time.Second, ShutdownTimeout: 5 * time.Second, RecoveryTimeout: 5 * time.Second}, nil,
		adapt(runtimeProof, streams[0], runtimeProcess), adapt(ready, helperStream, helperProcess),
		writerSupervised)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		served <- service.Run(ctx)
	}()

	public, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := admingate.NewBroker(ctx, streams[3], session, []admingate.Endpoint{{ID: 1, Listener: public}})
	if err != nil {
		t.Fatalf("admin broker: %v; output=%s", err, output.Bytes())
	}
	t.Cleanup(func() { _ = admin.Close(); admin.Wait() })
	select {
	case <-service.Ready():
	case <-finished:
		t.Fatalf("daemon exited before application readiness: %v; output=%s", waitErr, output.Bytes())
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not report application readiness; output=%s", output.Bytes())
	}

	url := "http://" + public.Addr().String() + "/"
	client := &http.Client{Timeout: time.Second}
	var response *http.Response
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		response, err = client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil || response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("daemon admin readiness: status=%v err=%v; output=%s", response, err, output.Bytes())
	}
	websocketURL := "ws://" + public.Addr().String() + "/api/ws"
	websocketConn, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatalf("daemon admin WebSocket: %v; output=%s", err, output.Bytes())
	}
	defer websocketConn.CloseNow()                        //nolint:errcheck
	if _, _, err := websocketConn.Read(ctx); err != nil { // initial version event
		t.Fatal(err)
	}
	hello, err := api.NewRequest("qualification-hello", api.SurfaceAdmin, string(api.MethodAdminHello), api.HelloRequest{ClientName: "isolation-qualification"})
	if err != nil {
		t.Fatal(err)
	}
	helloData, err := api.MarshalMessage(hello)
	if err != nil {
		t.Fatal(err)
	}
	if err := websocketConn.Write(ctx, websocket.MessageText, helloData); err != nil {
		t.Fatal(err)
	}
	_, helloReplyData, err := websocketConn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	helloReply, err := api.UnmarshalMessage(helloReplyData)
	if err != nil || helloReply.ID != hello.ID || helloReply.Error != nil {
		t.Fatalf("admin hello reply=%+v err=%v", helloReply, err)
	}
	helloResult, err := api.DecodePayload[api.HelloResponse](helloReply.Result)
	if err != nil || helloResult.ExecutionMode != "isolation-development" {
		t.Fatalf("admin hello=%+v err=%v", helloResult, err)
	}
	call := func(id string, surface api.Surface, method string, payload any) api.Message {
		t.Helper()
		request, callErr := api.NewRequest(id, surface, method, payload)
		if callErr != nil {
			t.Fatal(callErr)
		}
		data, callErr := api.MarshalMessage(request)
		if callErr != nil {
			t.Fatal(callErr)
		}
		if callErr = websocketConn.Write(ctx, websocket.MessageText, data); callErr != nil {
			t.Fatal(callErr)
		}
		for {
			_, data, callErr = websocketConn.Read(ctx)
			if callErr != nil {
				t.Fatal(callErr)
			}
			reply, decodeErr := api.UnmarshalMessage(data)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if reply.Kind == api.MessageResponse && reply.ID == id {
				if reply.Error != nil {
					t.Fatalf("%s/%s: %+v", surface, method, reply.Error)
				}
				return reply
			}
		}
	}
	workspaceReply := call("qualification-workspace", api.SurfaceEngine, string(api.MethodEngineWorkspaceSnapshot), api.WorkspaceRequest{Workspace: "main"})
	workspace, err := api.DecodePayload[api.WorkspaceSnapshotResponse](workspaceReply.Result)
	if err != nil {
		t.Fatal(err)
	}
	var commandNode uint64
	for id, node := range workspace.Snapshot.Nodes {
		if node.Class == "almagest/Command" {
			if node.Placeholder {
				t.Fatalf("command node is a placeholder: %+v; runtime_output=%s", node, output.Bytes())
			}
			commandNode = id
			break
		}
	}
	if commandNode == 0 {
		t.Fatalf("command node missing from workspace: %+v", workspace.Snapshot.Nodes)
	}
	call("qualification-command", api.SurfaceEngine, string(api.MethodEngineTriggerNode), api.IDRequest{Workspace: "main", ID: commandNode})
	marker := filepath.Join(helperState, "command-ran")
	for deadline := time.Now().Add(5 * time.Second); ; {
		_, statErr := os.Stat(marker)
		if statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not execute through H: marker_err=%v helper_output=%s", statErr, helperOutput.Bytes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Chmod(helperState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(helperState, 65532, 65532); err != nil {
		t.Fatal(err)
	}
	configReply := call("qualification-config", api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{})
	configSnapshot, err := api.DecodePayload[api.ConfigSnapshot](configReply.Result)
	if err != nil || configSnapshot.ReadOnly || configSnapshot.Revision == "" {
		t.Fatalf("config snapshot=%+v err=%v", configSnapshot, err)
	}
	updatedText := "{\n  \"Config\": {\"Save\": {\"IdleMilliseconds\": 10}},\n  \"nodes\": {},\n}\n"
	call("qualification-config-update", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
		Text: updatedText, Revision: configSnapshot.Revision,
	})
	createReply := call("qualification-snapshot-create", api.SurfaceSnapshots, string(api.MethodSnapshotsCreate), api.SnapshotsCreateRequest{Name: "qualification"})
	created, err := api.DecodePayload[api.SnapshotInfo](createReply.Result)
	if err != nil || created.UUID == "" {
		t.Fatalf("snapshot create=%+v err=%v", created, err)
	}
	restoreReply := call("qualification-snapshot-restore", api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotUUIDRequest{UUID: created.UUID})
	restored, err := api.DecodePayload[api.SnapshotsRestoreResponse](restoreReply.Result)
	if err != nil || restored.Snapshot.UUID != created.UUID || !restored.ConfigWritten {
		t.Fatalf("snapshot restore=%+v err=%v", restored, err)
	}

	if err := broker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
		if ctx.Err() != nil {
			t.Fatalf("runtime exit=%v context=%v output=%s", waitErr, ctx.Err(), output.Bytes())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("runtime survived trusted-network loss; output=%s", output.Bytes())
	}
	select {
	case <-helperFinished:
		if helperWaitErr == nil || ctx.Err() != nil {
			t.Fatalf("command helper exit=%v context=%v output=%s", helperWaitErr, ctx.Err(), helperOutput.Bytes())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("command helper survived runtime loss; output=%s", helperOutput.Bytes())
	}
	select {
	case err := <-served:
		if !errors.Is(err, supervision.ErrRuntimeLost) {
			t.Fatalf("supervised broker loss: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("network broker did not stop")
	}
	select {
	case <-writerDone:
		if writerWaitErr == nil || ctx.Err() != nil {
			t.Fatalf("writer exit=%v context=%v output=%s", writerWaitErr, ctx.Err(), writerOutput.Bytes())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("config writer did not stop")
	}
	// Inspect C's private output only after it has exited. Do not weaken its
	// directory ownership while it is serving runtime transactions.
	for _, path := range []string{writerState, configTarget} {
		if err := os.Chown(path, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	written, err := os.ReadFile(configTarget)
	if err != nil || !bytes.Contains(written, []byte("IdleMilliseconds")) {
		t.Fatalf("isolated config target=%q err=%v", written, err)
	}
	t.Log("real daemon engine, relayed admin, restricted C/H, config transaction, and live snapshot restore passed; supervised trusted-network loss stopped R, C, and H")
}

func nativeCompleteDaemonEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv("ALMAGEST_PROFILE_TEST_NATIVE") != "1" {
		t.Skip("run just linux-isolation-profiles in Docker")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("Docker required")
	}
}
