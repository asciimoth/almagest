//go:build windows && winintegration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
)

const windowsSandboxMatrixChild = "ALMAGEST_WINDOWS_SANDBOX_MATRIX_CHILD"

const windowsTorHelperEnvironment = "ALMAGEST_WINVM_TOR_HELPER"

func TestWindowsSandboxModeMatrix(t *testing.T) {
	assertWindowsE2EDefaultSandboxMode(t)
	executable := os.Getenv("ALMAGEST_WINVM_SANDBOX_HELPER")
	if executable == "" {
		t.Fatal("ALMAGEST_WINVM_SANDBOX_HELPER is not set")
	}
	for _, mode := range []string{"off", "auto", "strict"} {
		t.Run(mode, func(t *testing.T) {
			root := newPrivateWindowsTestDir(t)
			pipe := fmt.Sprintf("almagest-sandbox-%s-%d", mode, os.Getpid())
			config := `{"nodes":{"Sandbox probe":{"Class":"almagest/SandboxProbe"}}}`
			protected := windowsSandboxMatrixProtected(t, root, pipe, executable, mode)
			daemon := newWindowsDaemon(t, root, pipe, config, protected)
			// Off mode inherits the daemon environment. Native modes construct the
			// same value from protected template policy.
			daemon.environment = append(daemon.environment, windowsSandboxMatrixChild+"=1")
			daemon.start()
			defer daemon.stop()

			waitForWindowsSandboxProbe(t, daemon)
			var hello api.HelloResponse
			if err := json.Unmarshal(daemon.cliJSON("admin", "hello", "--client-name", "windows-sandbox-matrix"), &hello); err != nil {
				t.Fatal(err)
			}
			assertWindowsSandboxMatrixStatus(t, hello, mode)
		})
	}
}

func TestWindowsTorDriverSandboxModeMatrix(t *testing.T) {
	executable := os.Getenv(windowsTorHelperEnvironment)
	if executable == "" {
		t.Fatalf("%s is not set", windowsTorHelperEnvironment)
	}
	for _, mode := range []string{"off", "auto", "strict"} {
		t.Run(mode, func(t *testing.T) {
			root := newPrivateWindowsTestDir(t)
			bin := filepath.Join(root, "discovered-bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			image, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			writePrivateWindowsFile(t, filepath.Join(bin, "tor.exe"), image)
			pipe := fmt.Sprintf("almagest-tor-%s-%d", mode, os.Getpid())
			httpPort := reserveTCPPort(t)
			socksPort := reserveTCPPort(t)
			config := fmt.Sprintf(`{
	"nodes":{
		"Tor outbound":{"Class":"almagest/MockNetwork","allowAnyHost":true},
		"Target":{"Class":"almagest/HttpServer","Links":["Network -> [Tor outbound] Network"],"host":"127.0.0.1","port":%d,"response":"windows-tor-%s"},
		"Tor":{"Class":"almagest/TorDriver","Links":["out -> [Tor outbound] Network"]},
		"Tor proxy":{"Class":"almagest/LocalProxy","Links":["proxy -> [Tor] Network default"],"socksHost":"127.0.0.1","socksPort":%d}
	}}
`, httpPort, mode, socksPort)
			protected := windowsTorMatrixProtected(t, root, pipe, mode)
			daemon := newWindowsDaemon(t, root, pipe, config, protected)
			daemon.environment = append(daemon.environment, "PATH="+bin+";"+os.Getenv("PATH"))
			daemon.start()
			defer daemon.stop()

			waitForWindowsTorLabel(t, daemon, "up")
			wantSOCKSHTTP(t, socksPort, httpPort, "windows-tor-"+mode)
			var hello api.HelloResponse
			if err := json.Unmarshal(daemon.cliJSON("admin", "hello", "--client-name", "windows-tor-matrix"), &hello); err != nil {
				t.Fatal(err)
			}
			assertWindowsTorSandboxStatus(t, hello, mode)
		})
	}
}

func windowsTorMatrixProtected(t *testing.T, root, pipe, mode string) string {
	t.Helper()
	value := map[string]any{
		"Admin":  map[string]any{"Pipe": `\\.\pipe\` + pipe},
		"System": map[string]any{"Type": "debug"},
		"Sandbox": map[string]any{
			"Mode":                   mode,
			"DiscoverSystemPrograms": true,
			"TemporaryRoot":          filepath.Join(root, "sandbox-temporary"),
			"RegistryRoot":           filepath.Join(root, "sandbox-registry"),
		},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func waitForWindowsTorLabel(t *testing.T, daemon *windowsDaemon, label string) {
	t.Helper()
	waitFor(t, "TorDriver label "+label, func() bool {
		var response api.WorkspaceSnapshotResponse
		if err := json.Unmarshal(daemon.cliJSON("engine", "workspace-snapshot"), &response); err != nil {
			t.Fatal(err)
		}
		for _, node := range response.Snapshot.Nodes {
			if node.Name == "Tor" {
				if node.Label == "error" {
					t.Fatalf("TorDriver failed; daemon log:\n%s", daemon.readLog())
				}
				return node.Label == label
			}
		}
		return false
	})
}

func assertWindowsTorSandboxStatus(t *testing.T, hello api.HelloResponse, mode string) {
	t.Helper()
	if hello.SubprogramSandbox == nil {
		t.Fatal("daemon did not report subprogram sandbox status")
	}
	status, ok := hello.SubprogramSandbox.Templates["tor"]
	if !ok {
		t.Fatalf("Tor sandbox template is absent: %+v", hello.SubprogramSandbox)
	}
	if mode == "off" {
		if status.Profile != "direct-v1" || status.StrictSatisfied || status.FullBoundary {
			t.Fatalf("off Tor template status = %+v", status)
		}
		return
	}
	if status.Profile != "windows-native-v1" || !status.StrictSatisfied || status.FullBoundary {
		t.Fatalf("native Tor template status = %+v", status)
	}
}

func waitForWindowsSandboxProbe(t *testing.T, daemon *windowsDaemon) {
	t.Helper()
	deadline := time.Now().Add(integrationTimeout)
	for time.Now().Before(deadline) {
		logs := daemon.cliJSON("log", "subscribe")
		if bytes.Contains(logs, []byte("sandbox probe passed: windows sandbox child passed")) {
			return
		}
		if bytes.Contains(logs, []byte("sandbox probe failed:")) {
			t.Fatalf("sandbox probe failed:\n%s\ndaemon log:\n%s", logs, daemon.readLog())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for sandbox probe child; daemon log:\n%s", daemon.readLog())
}

func assertWindowsE2EDefaultSandboxMode(t *testing.T) {
	t.Helper()
	want := os.Getenv("ALMAGEST_E2E_SANDBOX_MODE")
	if want == "" {
		return
	}
	var root struct {
		Sandbox struct {
			Mode string
		}
	}
	if err := json.Unmarshal(windowsE2EProtectedConfig(t, []byte(`{}`)), &root); err != nil {
		t.Fatal(err)
	}
	if root.Sandbox.Mode != want {
		t.Fatalf("default E2E sandbox mode = %q, want %q", root.Sandbox.Mode, want)
	}
}

func windowsSandboxMatrixProtected(t *testing.T, root, pipe, executable, mode string) string {
	t.Helper()
	value := map[string]any{
		"Admin":  map[string]any{"Pipe": `\\.\pipe\` + pipe},
		"System": map[string]any{"Type": "debug"},
		"Sandbox": map[string]any{
			"Mode":          mode,
			"TemporaryRoot": filepath.Join(root, "sandbox-temporary"),
			"RegistryRoot":  filepath.Join(root, "sandbox-registry"),
			"Programs": map[string]any{"qualification-helper": map[string]any{
				"Executable": executable,
				"Builder":    "fixed-v1",
			}},
			"Templates": map[string]any{"qualification": map[string]any{
				"AllowedClasses": []string{"almagest/SandboxProbe"},
				"Programs":       []string{"qualification-helper"},
				"RuntimeProfile": "windows-native-v1",
				"Environment": map[string]any{"Set": map[string]string{
					windowsSandboxMatrixChild: "1",
				}},
			}},
		},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertWindowsSandboxMatrixStatus(t *testing.T, hello api.HelloResponse, mode string) {
	t.Helper()
	status := hello.SubprogramSandbox
	if hello.ExecutionMode != "direct" || hello.Isolation != nil || status == nil || status.Mode != mode || status.Platform != "windows" {
		t.Fatalf("daemon sandbox status = %+v, execution mode = %q, isolation = %+v", status, hello.ExecutionMode, hello.Isolation)
	}
	template, ok := status.Templates["qualification"]
	if !ok {
		t.Fatalf("qualification template is absent: %+v", status)
	}
	if mode == "off" {
		if template.Profile != "direct-v1" || template.StrictSatisfied || template.FullBoundary {
			t.Fatalf("off template status = %+v", template)
		}
		if len(template.Features) == 0 {
			t.Fatal("off template reported no feature decisions")
		}
		for _, feature := range template.Features {
			if feature.State != "off" {
				t.Fatalf("off feature status = %+v", feature)
			}
		}
		return
	}
	if template.Profile != "windows-native-v1" || !template.StrictSatisfied || template.FullBoundary {
		t.Fatalf("native template status = %+v", template)
	}
	if len(template.Features) == 0 {
		t.Fatal("native template reported no feature decisions")
	}
	for _, feature := range template.Features {
		if feature.State != "applied" && feature.State != "excluded-os" {
			t.Fatalf("native feature status = %+v", feature)
		}
	}
}
