//go:build linux && amd64

package linuxprofile_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/networkrole"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/nativebackend"
	"github.com/asciimoth/sysnet-linux/routing"
	"github.com/coder/websocket"
)

func TestNativeNetworkRoleAssembly(t *testing.T) {
	nativeCompleteDaemonEnvironment(t)
	if os.Getenv("ALMAGEST_PROFILE_TRUSTED_SYSTEM") != "1" {
		t.Skip("requires trusted backend container")
	}
	mode := os.Getenv("ALMAGEST_NETWORK_ASSEMBLY_CHILD")
	if mode == "" {
		for _, mode := range []string{"ready", "file-output", "backend-failure", "helper-failure", "admin-failure"} {
			t.Run(mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeNetworkRoleAssembly$", "-test.v")
				cmd.Env = append(os.Environ(), "ALMAGEST_NETWORK_ASSEMBLY_CHILD="+mode)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("assembly: %v\n%s", err, output)
				}
				t.Logf("%s", output)
			})
		}
		return
	}
	open := func(path string) *os.File {
		t.Helper()
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	private := func(uid int, files map[string][]byte) *os.File {
		t.Helper()
		path := t.TempDir()
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		file := open(path)
		t.Cleanup(func() {
			if err := filepath.WalkDir(path, func(path string, info os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				return os.Lchown(path, 0, 0)
			}); err != nil {
				t.Errorf("restore fixture ownership: %v", err)
			}
		})
		for name, data := range files {
			target := filepath.Join(path, name)
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(target, uid, uid); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chown(path, uid, uid); err != nil {
			t.Fatal(err)
		}
		return file
	}
	launcher, cgroup := open("/qualification/launcher"), open("/sys/fs/cgroup")
	role := func(kind linuxprofile.Role, uid uint32, executable string, state *os.File) networkrole.LinuxRole {
		return networkrole.LinuxRole{Plan: linuxprofile.Plan{Role: kind, UID: uid, GID: uid, Limits: linuxprofile.DefaultLimits()}, Inputs: linuxprofile.Inputs{Launcher: launcher, Executable: open(executable), Directory: state, Cgroup: cgroup}}
	}
	runtime := role(linuxprofile.Runtime, 65534, "/qualification/almagestd", private(65534, nil))
	writer := role(linuxprofile.Writer, 65533, "/qualification/config-writer-role", private(65533, map[string][]byte{"config.hujson": []byte(`{}`)}))
	helper := role(linuxprofile.Command, 65532, "/qualification/command-role", private(65532, nil))
	helper.Inputs.Command = open("/qualification/command")
	state := private(0, nil)
	journal, err := supervision.OpenJournal(filepath.Join(state.Name(), "recovery.json"), 64)
	if err != nil {
		t.Fatal(err)
	}
	settings := nativebackend.NewLinuxFileConfig().Native(t.Logf)
	settings.RoutingMonitorNamespace = routing.CurrentNamespace
	launch := networkrole.LinuxLaunch{
		Runtime: runtime, Writer: &networkrole.LinuxWriter{LinuxRole: writer, Target: "config.hujson"},
		Helper:       &networkrole.LinuxHelper{LinuxRole: helper, Config: commandhelper.Config{Profiles: map[string]commandhelper.Profile{"fixed": {Sandbox: "fixed", Executable: "/command", WorkingDirectory: "/state", MaxConcurrent: 1}}, Commands: map[string]commandhelper.Command{"fixed": {Profile: "fixed", AllowNodes: true}}}},
		RuntimeInput: roleentry.RuntimeInput{Version: protocol.Version}, RuntimeStorage: &bootstrap.RuntimeStorageInput{Regular: []byte(`{}`)},
		Policy: policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: "container-root"}, policy.CommandAuthority{NodeCommands: []string{"fixed"}}),
		Native: settings, State: state, Cgroup: cgroup, Limits: linuxprofile.DefaultLimits(), Journal: journal,
		Admin: []admingate.EndpointConfig{{ID: 1, Kind: admingate.EndpointWeb, Address: "127.0.0.1:48137"}}, Output: os.Stderr,
	}
	if mode == "backend-failure" {
		launch.Cgroup = state
	}
	if mode == "helper-failure" {
		launch.Helper.Config.Commands["fixed"] = commandhelper.Command{Profile: "missing", AllowNodes: true}
	}
	if mode == "admin-failure" {
		launch.Admin[0].Address = "invalid-address"
	}
	if mode == "file-output" {
		output, err := os.OpenFile(filepath.Join(state.Name(), "roles.log"), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = output.Close() })
		launch.Output = output
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ready := false
	err = networkrole.RunLinux(ctx, launch, func(roleentry.RuntimeIsolationStatus) error {
		defer cancel()
		ready = true
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:48137/")
		if err != nil {
			t.Error(err)
		} else {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Errorf("admin status %d", response.StatusCode)
			}
		}
		assemblyConfigUpdate(t, ctx)
		return nil
	})
	if errors.Is(err, supervision.ErrShutdownIncomplete) {
		t.Fatalf("assembly left unconfirmed processes: %v", err)
	}
	switch mode {
	case "backend-failure":
		if ready || !errors.Is(err, linuxprofile.ErrUnavailable) {
			t.Fatalf("backend failure: ready=%t err=%v", ready, err)
		}
		t.Log("post-child backend preparation failure rolled back R/C/H")
	case "helper-failure", "admin-failure":
		if ready || err == nil {
			t.Fatalf("failed startup published readiness: mode=%s ready=%t err=%v", mode, ready, err)
		}
		t.Logf("%s rolled back R/C/H without incomplete exit observations", mode)
	default:
		if !ready || !errors.Is(err, context.Canceled) {
			t.Fatalf("assembly readiness/cancellation: ready=%t err=%v", ready, err)
		}
		for _, path := range []string{writer.Inputs.Directory.Name(), filepath.Join(writer.Inputs.Directory.Name(), "config.hujson")} {
			if err := os.Chown(path, 0, 0); err != nil {
				t.Fatal(err)
			}
		}
		persisted, err := os.ReadFile(filepath.Join(writer.Inputs.Directory.Name(), "config.hujson"))
		var actual, expected any
		decodeErr := json.Unmarshal(persisted, &actual)
		expectedErr := json.Unmarshal([]byte(assemblyUpdatedConfig), &expected)
		if err != nil || decodeErr != nil || expectedErr != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("fixed writer persistence: %q %v", persisted, err)
		}
		t.Log("trusted assembly prepared N, started restricted R/C/H, served admin, and joined all exits")
	}
}

const assemblyUpdatedConfig = `{"Config":{"Save":{"IdleMilliseconds":10}},"nodes":{}}`

func assemblyConfigUpdate(t *testing.T, parent context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:48137/api/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	call := func(id string, surface api.Surface, method string, payload any) api.Message {
		t.Helper()
		request, err := api.NewRequest(id, surface, method, payload)
		if err != nil {
			t.Fatal(err)
		}
		data, err := api.MarshalMessage(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := api.UnmarshalMessage(data)
			if err != nil {
				t.Fatal(err)
			}
			if reply.Kind == api.MessageResponse && reply.ID == id {
				if reply.Error != nil {
					t.Fatalf("%s: %+v", method, reply.Error)
				}
				return reply
			}
		}
	}
	call("hello", api.SurfaceAdmin, string(api.MethodAdminHello), api.HelloRequest{ClientName: "assembly-test"})
	reply := call("config", api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{})
	snapshot, err := api.DecodePayload[api.ConfigSnapshot](reply.Result)
	if err != nil || snapshot.ReadOnly || snapshot.Revision == "" {
		t.Fatalf("writer snapshot: %+v %v", snapshot, err)
	}
	call("update", api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{Revision: snapshot.Revision, Text: assemblyUpdatedConfig})
}
