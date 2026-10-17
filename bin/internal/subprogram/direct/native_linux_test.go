//go:build linux

package direct

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func TestLinuxLandlockProfileLaunchAndDenial(t *testing.T) {
	launcher := os.Getenv("ALMAGEST_SUBPROGRAM_LAUNCHER")
	if launcher == "" {
		t.Skip("native subprogram launcher was not supplied")
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 65534, 65534
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "almagest-subprogram-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err = os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	deniedExecutable := filepath.Join(root, "denied-program")
	executableData, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(deniedExecutable, executableData, 0o755); err != nil {
		t.Fatal(err)
	}
	approvedExecutable := filepath.Join(root, "approved-program")
	if err = os.WriteFile(approvedExecutable, executableData, 0o555); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(approvedExecutable, filepath.Join(root, "approved-program-alias")); err != nil {
		t.Fatal(err)
	}
	dynamicDependency := filepath.Join(root, "dynamic-dependency")
	if err = os.WriteFile(dynamicDependency, []byte("mapped executable data"), 0o644); err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(root, "read-only")
	if err = os.Mkdir(readOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	readOnlyFile := filepath.Join(readOnly, "input")
	if err = os.WriteFile(readOnlyFile, []byte("approved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err = os.Chown(readOnly, uid, gid); err != nil {
			t.Fatal(err)
		}
		if err = os.Chown(readOnlyFile, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	mode := sandbox.Auto
	readOnlyTarget := readOnly
	executableTarget := approvedExecutable
	dynamicDependencyTarget := dynamicDependency
	fullExpected := os.Geteuid() == 0 && os.Getenv("E2E_CGROUP_PARENT") != ""
	if fullExpected {
		mode = sandbox.Strict
		readOnlyTarget = "/approved"
		executableTarget = "/approved-executable"
		dynamicDependencyTarget = "/dynamic-dependency"
	}
	config := policy.Config{
		Mode: mode, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"), LinuxLauncher: launcher,
		LinuxCgroupRoot: os.Getenv("E2E_CGROUP_PARENT"),
		Programs: map[sandbox.ProgramID]policy.Program{"helper": {
			Executable: executable, Builder: "fixed-v1",
			Arguments: []policy.Argument{{Literal: "-test.run=TestDirectHelperProcess"}, {Literal: "--"}, {Input: "action"}, {Input: "value"}},
			Inputs:    map[string]policy.Input{"action": {Kind: sandbox.TextValue, Required: true, Maximum: 32}, "value": {Kind: sandbox.TextValue, Required: true, Maximum: 128}},
		}, "denied": {Executable: deniedExecutable, Builder: "fixed-v1"}},
		Templates: map[sandbox.TemplateID]policy.Template{"native": {
			AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper", "denied"}, RuntimeProfile: "linux-landlock-v1",
			Linux: policy.Linux{UID: uint32(uid), GID: uint32(gid)}, PersistentRoot: filepath.Join(root, "persistent"), ReadOnly: []policy.ReadOnly{{Source: readOnly, Target: readOnlyTarget}, {Source: approvedExecutable, Target: executableTarget, Executable: true}, {Source: dynamicDependency, Target: dynamicDependencyTarget, Executable: true}}, Limits: policy.TemplateLimits{Commands: 2, Tasks: 4096, Files: 64, Sockets: 64, MemoryBytes: 8 << 30, CPUPercent: 100, TransferBytes: 1 << 20, TemporaryBytes: 32 << 20, OutputBytes: 1 << 20},
		}},
	}
	probe := policy.Probe{Platform: "linux", EffectiveIdentity: strconv.Itoa(os.Geteuid()), Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true}}
	backend, err := NewBackend(config, probe)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := backend.Prepare(context.Background(), sandbox.Owner{Workspace: "main", Key: "native-key", Epoch: 1, Class: "test/Node", Name: "Native", NameRevision: 1}, "main", "native", 1)
	if err != nil {
		t.Fatal(err)
	}
	files, err := instance.FS(sandbox.Temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err = files.WriteFile(context.Background(), "private-input", []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := instance.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	status := instance.Status()
	wantIdentity := strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
	if status.EffectiveIdentity != wantIdentity {
		t.Fatalf("effective identity = %q, want %q", status.EffectiveIdentity, wantIdentity)
	}
	if fullExpected {
		if !status.FullBoundary || !status.StrictSatisfied || status.Profile != "linux-full-v1" {
			t.Fatalf("full status = %+v", status)
		}
		if context := instance.ProcessContext(); context.ControllerPID != 1 {
			t.Fatalf("full process context = %+v", context)
		}
		for _, feature := range status.Features {
			if feature.State != sandbox.Applied {
				t.Fatalf("full feature = %+v", feature)
			}
		}
	}
	for _, feature := range []sandbox.Feature{sandbox.WritableStorage, sandbox.FilesystemAccess} {
		if !featureApplied(status, feature) {
			t.Fatalf("feature %q was not applied: %+v", feature, status.Features)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	sandboxCommand, err := instance.NewCommand("helper", launch("echo", "never"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sandboxCommand.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = sandboxCommand.Start(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled start = %v", err)
	}
	concrete := sandboxCommand.(*command)
	concrete.mu.Lock()
	retained := len(concrete.nativeFiles) != 0 || concrete.execStatus != nil || concrete.stdout != nil
	concrete.mu.Unlock()
	if retained {
		t.Fatal("pre-canceled start retained native descriptors")
	}
	if _, err = reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("pre-canceled stdout remained open")
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("echo", "native-ok"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "native-ok" {
		t.Fatalf("native output = %q, %v", output, err)
	}
	privatePath, err := instance.ChildPath(sandbox.Path{Root: sandbox.Temporary, Rel: "private-input"})
	if err != nil {
		t.Fatal(err)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("read", privatePath))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "private" {
		t.Fatalf("sandbox-created private file = %q, %v", output, err)
	}
	deniedPath, err := instance.ProgramPath("denied")
	if err != nil {
		t.Fatal(err)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("exec-path", deniedPath))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "denied" {
		t.Fatalf("undeclared descendant execution = %q, %v", output, err)
	}
	if fullExpected {
		sandboxCommand, err = instance.NewCommand("helper", launch("exec-path", executableTarget))
		if err != nil {
			t.Fatal(err)
		}
		output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
		if err != nil || string(output) != "escapedescaped" {
			t.Fatalf("approved read-only execution = %q, %v", output, err)
		}
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("identity", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != wantIdentity {
		t.Fatalf("native identity = %q, %v; want %q", output, err, wantIdentity)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("namespace", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "denied" {
		t.Fatalf("namespace creation = %q, %v", output, err)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("write-temp", "approved"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "written" {
		t.Fatalf("writable storage = %q, %v", output, err)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("write-state", "approved"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "written" {
		t.Fatalf("persistent storage = %q, %v", output, err)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("outside", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "denied" {
		t.Fatalf("outside read = %q, %v", output, err)
	}
	childReadOnlyFile := readOnlyFile
	if fullExpected {
		childReadOnlyFile = "/approved/input"
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("read", childReadOnlyFile))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "approved" {
		t.Fatalf("approved read = %q, %v", output, err)
	}
	sandboxCommand, err = instance.NewCommand("helper", launch("write", childReadOnlyFile))
	if err != nil {
		t.Fatal(err)
	}
	output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
	if err != nil || string(output) != "denied" {
		t.Fatalf("read-only write = %q, %v", output, err)
	}
	if fullExpected {
		for action, want := range map[string]string{"dev-null": "usable", "exec-temp": "denied", "linux-security": "denied", "controller-signal": "denied"} {
			sandboxCommand, err = instance.NewCommand("helper", launch(action, "unused"))
			if err != nil {
				t.Fatal(err)
			}
			output, err = sandbox.Output(context.Background(), sandboxCommand, 128)
			if err != nil || string(output) != want {
				t.Fatalf("%s = %q, %v", action, output, err)
			}
		}
		sandboxCommand, err = instance.NewCommand("helper", launch("spawn-setsid-background", "unused"))
		if err != nil {
			t.Fatal(err)
		}
		if output, outputErr := sandbox.Output(context.Background(), sandboxCommand, 256); outputErr != nil {
			t.Fatalf("descendant launch: %v: %s", outputErr, output)
		}
		time.Sleep(1200 * time.Millisecond)
		files, openErr := instance.FS(sandbox.Temporary)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, readErr := files.ReadFile(context.Background(), "escaped-descendant", 32); !errors.Is(readErr, os.ErrNotExist) {
			t.Fatalf("setsid descendant escaped command cgroup: %v", readErr)
		}
		testFullNativeSockets(t, instance)
	}
}

func testFullNativeSockets(t *testing.T, instance sandbox.Sandbox) {
	t.Helper()
	hostListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hostListener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if connection, dialErr := instance.DialContext(ctx, "tcp4", hostListener.Addr().String()); dialErr == nil {
		_ = connection.Close()
		t.Fatal("sandbox reached a host-loopback listener")
	}
	abstractName := "@almagest-host-" + strconv.Itoa(os.Getpid())
	abstractListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: abstractName, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = abstractListener.Close() })
	command, err := instance.NewCommand("helper", launch("dial-unix", abstractName))
	if err != nil {
		t.Fatal(err)
	}
	if output, outputErr := sandbox.Output(context.Background(), command, 128); outputErr != nil || string(output) != "denied" {
		t.Fatalf("host abstract Unix socket = %q, %v", output, outputErr)
	}
	listener, err := instance.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if outside, dialErr := net.DialTimeout("tcp4", listener.Addr().String(), 100*time.Millisecond); dialErr == nil {
		_ = outside.Close()
		t.Fatal("host reached private sandbox listener")
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err := instance.DialContext(context.Background(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server := <-accepted
	if server == nil {
		t.Fatal("sandbox listener did not accept")
	}
	t.Cleanup(func() { _ = server.Close() })
	if _, err = client.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 7)
	if _, err = io.ReadFull(server, data); err != nil || string(data) != "private" {
		t.Fatalf("private stream = %q, %v", data, err)
	}
	ipv6Listener, err := instance.Listen(context.Background(), "tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ipv6Listener.Close() })
	ipv6Accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := ipv6Listener.Accept()
		ipv6Accepted <- connection
	}()
	ipv6Client, err := instance.DialContext(context.Background(), "tcp6", ipv6Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = ipv6Client.Close()
	if connection := <-ipv6Accepted; connection == nil {
		t.Fatal("sandbox IPv6 listener did not accept")
	} else {
		_ = connection.Close()
	}
	packet, err := instance.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = packet.Close()
	unixListener, err := instance.ListenUnix(context.Background(), sandbox.Path{Root: sandbox.Temporary, Rel: "control.sock"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unixListener.Close() })
	unixAccepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := unixListener.Accept()
		unixAccepted <- conn
	}()
	unixClient, err := instance.DialUnix(context.Background(), sandbox.Path{Root: sandbox.Temporary, Rel: "control.sock"})
	if err != nil {
		t.Fatal(err)
	}
	_ = unixClient.Close()
	if conn := <-unixAccepted; conn == nil {
		t.Fatal("sandbox Unix listener did not accept")
	} else {
		_ = conn.Close()
	}
}

func featureApplied(status sandbox.Status, feature sandbox.Feature) bool {
	for _, result := range status.Features {
		if result.Feature == feature {
			return result.State == sandbox.Applied
		}
	}
	return false
}
