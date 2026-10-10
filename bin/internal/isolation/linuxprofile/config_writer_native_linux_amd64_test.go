//go:build linux && amd64

package linuxprofile_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/configpersistence"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func TestNativeTrustedConfigWriter(t *testing.T) {
	if os.Getenv("ALMAGEST_PROFILE_TRUSTED_SYSTEM") != "1" {
		t.Skip("requires the dedicated trusted-backend container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(state, "config.hujson")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateFile, err := os.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateFile.Close() })
	if err := os.Chown(target, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(state, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chown(state, 0, 0); _ = os.Chmod(state, 0o700) })
	open := func(path string) *os.File {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	parentFile, childFile, err := transport.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	plan := linuxprofile.Plan{Role: linuxprofile.Writer, UID: 65534, GID: 65534, Limits: linuxprofile.DefaultLimits()}
	command, err := linuxprofile.CommandContext(ctx, plan, linuxprofile.Inputs{
		Launcher: open("/qualification/launcher"), Control: childFile,
		Executable: open("/qualification/config-writer-role"), Directory: stateFile, Cgroup: open("/sys/fs/cgroup"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childFile.Close()
	t.Cleanup(func() {
		_ = parentFile.Close()
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	var childPID int64
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		data, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", command.Process.Pid, command.Process.Pid))
		if readErr == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 1 {
				childPID, readErr = strconv.ParseInt(fields[0], 10, 32)
				if readErr == nil && childPID > 0 {
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatalf("launcher child identity unavailable; output=%s", output.Bytes())
	}
	stream, err := transport.BindSocket(parentFile, transport.LinuxPeer{PID: int32(childPID), UID: 65534, GID: 65534})
	if err != nil {
		t.Fatal(err)
	}
	launch := policy.LaunchAuthority{Backend: "linux", CredentialClass: "container-root"}
	p := policy.Default(launch, policy.CommandAuthority{})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}
	ready, err := roleentry.SendBootstrap(ctx, stream, roleentry.Bootstrap{Version: protocol.Version,
		Role: protocol.RoleConfigWriter, Session: session, Target: "config.hujson"})
	if err != nil || !ready.RestrictionsApplied {
		waitErr := command.Wait()
		t.Fatalf("bootstrap readiness = %#v, %v; exit=%v args=%q output=%s", ready, err, waitErr, command.Args, output.Bytes())
	}
	peer, err := transport.NewControl(ctx, stream, session, nil)
	if err != nil {
		t.Fatalf("control peer: %v; output=%s", err, output.Bytes())
	}
	persistence, err := configpersistence.New(peer, session, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	document, err := persistence.ReadCurrent()
	if err != nil || string(document.Data) != "before" {
		t.Fatalf("read = %q, %v", document.Data, err)
	}
	result := persistence.Replace(document.Revision, "native-writer", []byte("after"))
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(state, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, 0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "after" {
		t.Fatalf("target = %q, %v", data, err)
	}
	_ = peer.Close()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		// Peer loss is a fatal role exit. The writer must not hang or report a
		// successful clean shutdown without a supervisor request.
		if err == nil || ctx.Err() != nil {
			t.Fatalf("writer exit = %v, context = %v; output=%s", err, ctx.Err(), output.Bytes())
		}
	case <-ctx.Done():
		t.Fatalf("writer did not exit after peer loss: %v; output=%s", ctx.Err(), output.Bytes())
	}
}
