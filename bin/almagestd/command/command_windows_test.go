//go:build windows

package command

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWindowsRunnerTimeoutKillsChildAndGrandchild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	logger := &recordingLogger{}
	command := helperCommand(t, "spawn-delayed-write", marker)
	command.Timeout = 250 * time.Millisecond
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{"spawn": command}})

	resolved, err := runner.registry.resolve(Request{Name: "spawn"})
	if err != nil {
		t.Fatal(err)
	}
	runner.runSync(context.Background(), resolved)
	if !strings.Contains(strings.Join(logger.messages(), "\n"), "status=timeout") {
		t.Fatal("command did not report timeout status")
	}
	waitForWindowsMarker(t, marker+".ready")
	time.Sleep(1100 * time.Millisecond)
	if _, err = os.Stat(marker); err == nil {
		t.Fatal("grandchild remained alive after command timeout")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat descendant marker: %v", err)
	}
}

func TestWindowsRunnerShutdownKillsChildAndGrandchild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	command := helperCommand(t, "spawn-delayed-write", marker)
	command.Sync = false
	runner := newTestRunner(t, &recordingLogger{}, Config{Commands: map[string]AllowedCommand{"spawn": command}})
	runner.Start(context.Background())
	if !runner.Submit(Request{Name: "spawn"}) {
		t.Fatal("Submit returned false")
	}
	waitForWindowsMarker(t, marker+".ready")
	runner.Close()
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild remained alive after runner shutdown")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat descendant marker: %v", err)
	}
}

func TestWindowsRunnerImmediateCancellationDoesNotLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	runner := newTestRunner(t, &recordingLogger{}, Config{Commands: map[string]AllowedCommand{
		"marker": helperCommand(t, "write-marker", marker),
	}})
	resolved, err := runner.registry.resolve(Request{Name: "marker"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.runSync(ctx, resolved)
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command marker stat = %v, want not exist", err)
	}
}

func TestWindowsRunnerPreservesArgumentsPathAndEnvironment(t *testing.T) {
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "path with spaces")
	if err = os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "command helper.exe")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(executable, data, 0o700); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "invocation.json")
	wantArgs := []string{"argument with spaces", `embedded"quote`, `trailing\\`}
	argv := []string{executable, "-test.run=TestCommandHelperProcess", "--", "record-invocation", marker}
	argv = append(argv, wantArgs...)
	runner := newTestRunner(t, &recordingLogger{}, Config{Commands: map[string]AllowedCommand{
		"record": {
			Command: argv,
			Env: map[string]string{
				"ALMAGEST_COMMAND_HELPER": "1",
				"ALMAGEST_TEST_VALUE":     "environment value with spaces",
			},
			Sync: true,
		},
	}})
	resolved, err := runner.registry.resolve(Request{Name: "record"})
	if err != nil {
		t.Fatal(err)
	}
	runner.runSync(context.Background(), resolved)

	result := struct {
		Args []string `json:"args"`
		Env  string   `json:"env"`
	}{}
	data, err = os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read invocation record: %v", err)
	}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Args, wantArgs) || result.Env != "environment value with spaces" {
		t.Fatalf("invocation = %#v, want args %#v and configured environment", result, wantArgs)
	}
}

func TestWindowsRunnerRejectsUnixCredentialsBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	uid := uint32(1000)
	command := helperCommand(t, "write-marker", marker)
	command.UID = &uid
	logger := &recordingLogger{}
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{"marker": command}})
	resolved, err := runner.registry.resolve(Request{Name: "marker"})
	if err != nil {
		t.Fatal(err)
	}
	runner.runSync(context.Background(), resolved)
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential-rejected command marker stat = %v, want not exist", err)
	}
	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "unsupported on Windows") || strings.Contains(logs, "command \"marker\" started") {
		t.Fatalf("credential rejection logs = %q", logs)
	}
}

func waitForWindowsMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("marker %q was not created", path)
}
