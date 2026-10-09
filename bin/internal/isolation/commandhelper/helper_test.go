package commandhelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type fakeLauncher struct {
	validateErr error
	run         func(context.Context, Execution) ExecutionResult
	mu          sync.Mutex
	executions  []Execution
}

func (f *fakeLauncher) Validate(Profile) error { return f.validateErr }
func (f *fakeLauncher) Run(ctx context.Context, execution Execution) ExecutionResult {
	f.mu.Lock()
	f.executions = append(f.executions, execution)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, execution)
	}
	return ExecutionResult{Started: true}
}
func (f *fakeLauncher) calls() []Execution {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.executions)
}

func helperSession() protocol.Session {
	return protocol.Session{ID: protocol.SessionID{1}, Epoch: 7, Version: protocol.Version}
}

func helperMeta(operation string) protocol.RequestMeta {
	return protocol.RequestMeta{RequestID: 1, SessionID: helperSession().ID, SessionEpoch: helperSession().Epoch, GraphEpoch: 3, OperationID: operation}
}

func baseConfig(t *testing.T) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"FIXED": "yes"}
	if value := os.Getenv("SystemRoot"); value != "" {
		environment["SystemRoot"] = value
	}
	return Config{
		Profiles: map[string]Profile{"notify": {
			Sandbox: "test-restricted", Executable: executable,
			Arguments:   []string{"fixed", "--name=${name}", "${generation}"},
			Environment: environment, WorkingDirectory: filepath.Dir(executable),
			Timeout: time.Second, MaxOutputBytes: 1024, MaxConcurrent: 2,
			Slots: map[string]Slot{
				"name":       {MaxLength: 16, Pattern: `[A-Za-z0-9._-]+`, Required: true},
				"generation": {MaxLength: 20, Pattern: `[0-9]+`, Required: true},
			},
		}},
		Commands: map[string]Command{
			"node-ok":    {Profile: "notify", AllowNodes: true},
			"owner-only": {Profile: "notify"},
		},
		Callbacks: map[string]Callback{"tun-ready": {
			Profile: "notify", Event: "tun-ready", ResourceType: protocol.ResourceTun,
			Slots: map[string]CallbackField{"name": CallbackEvent, "generation": CallbackResourceGeneration},
		}},
	}
}

func newTestHelper(t *testing.T, launcher Launcher) *Helper {
	t.Helper()
	helper, err := New(helperSession(), baseConfig(t), launcher)
	if err != nil {
		t.Fatal(err)
	}
	return helper
}

func TestHelperRunsKnownNodeCommandWithFixedProcessInputs(t *testing.T) {
	launcher := &fakeLauncher{}
	helper := newTestHelper(t, launcher)
	request := protocol.NodeCommandRequest{Meta: helperMeta("node-1"), CommandID: "node-ok", Slots: map[string]string{"name": "alpha", "generation": "9"}}
	result, err := helper.RunNodeCommand(context.Background(), request)
	if err != nil || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("RunNodeCommand() = %+v, %v", result, err)
	}
	calls := launcher.calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Arguments, []string{"fixed", "--name=alpha", "9"}) || !slices.Contains(calls[0].Environment, "FIXED=yes") {
		t.Fatalf("executions = %#v", calls)
	}
	if slices.ContainsFunc(calls[0].Environment, func(value string) bool { return strings.HasPrefix(value, "PATH=") }) {
		t.Fatalf("inherited environment leaked: %#v", calls[0].Environment)
	}
	retry, err := helper.RunNodeCommand(context.Background(), request)
	if err != nil || retry != result || len(launcher.calls()) != 1 {
		t.Fatalf("retry = %+v, %v; calls = %d", retry, err, len(launcher.calls()))
	}
	request.Slots["name"] = "different"
	conflict, err := helper.RunNodeCommand(context.Background(), request)
	if !errors.Is(err, protocol.ErrOperationConflict) || conflict.Code != "conflict" || len(launcher.calls()) != 1 {
		t.Fatalf("conflict = %+v, %v", conflict, err)
	}
}

func TestHelperRejectsNodeAuthorityAndArgumentInjection(t *testing.T) {
	launcher := &fakeLauncher{}
	helper := newTestHelper(t, launcher)
	for name, request := range map[string]protocol.NodeCommandRequest{
		"owner command":     {Meta: helperMeta("deny-owner"), CommandID: "owner-only", Slots: map[string]string{"name": "x", "generation": "1"}},
		"unknown command":   {Meta: helperMeta("deny-unknown"), CommandID: "not-configured"},
		"unknown slot":      {Meta: helperMeta("deny-slot"), CommandID: "node-ok", Slots: map[string]string{"name": "x", "generation": "1", "argv": "--help"}},
		"pattern injection": {Meta: helperMeta("deny-pattern"), CommandID: "node-ok", Slots: map[string]string{"name": "x;touch", "generation": "1"}},
		"option injection":  {Meta: helperMeta("deny-option"), CommandID: "node-ok", Slots: map[string]string{"name": "x", "generation": "--help"}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := helper.RunNodeCommand(context.Background(), request)
			if err == nil || result.Outcome != protocol.OutcomeRejected {
				t.Fatalf("result = %+v, %v", result, err)
			}
		})
	}
	if len(launcher.calls()) != 0 {
		t.Fatalf("rejected inputs started %d commands", len(launcher.calls()))
	}
}

func TestHelperSeparatesCallbackAuthorityAndDerivesSlots(t *testing.T) {
	launcher := &fakeLauncher{}
	helper := newTestHelper(t, launcher)
	resourceID := protocol.ResourceID{9}
	request := protocol.CallbackRequest{Meta: helperMeta("shared-id"), CallbackID: "tun-ready", Event: "tun-ready", Resource: protocol.ResourceRef{
		ID: resourceID, SessionID: helperSession().ID, Type: protocol.ResourceTun, Generation: 14, GraphEpoch: 3,
	}}
	result, err := helper.RunCallback(context.Background(), request)
	if err != nil || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("RunCallback() = %+v, %v", result, err)
	}
	calls := launcher.calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Arguments, []string{"fixed", "--name=tun-ready", "14"}) {
		t.Fatalf("callback execution = %#v", calls)
	}
	query := protocol.OperationResultRequest{Meta: helperMeta(""), OperationID: "shared-id"}
	if _, err := helper.GetResult(context.Background(), query); !errors.Is(err, protocol.ErrOperationUnknown) {
		t.Fatalf("runtime read callback receipt: %v", err)
	}
	if got, err := helper.GetCallbackResult(context.Background(), query); err != nil || got != result {
		t.Fatalf("callback receipt = %+v, %v", got, err)
	}

	nodeBody, _ := json.Marshal(protocol.NodeCommandRequest{Meta: helperMeta("forge-node"), CommandID: "node-ok"})
	callbackBody, _ := json.Marshal(request)
	if _, err := helper.CallbackHandler(context.Background(), protocol.OperationRunNodeCommand, nodeBody); !errors.Is(err, protocol.ErrUnsupportedOperation) {
		t.Fatalf("node command on callback channel = %v", err)
	}
	if _, err := helper.RuntimeHandler(context.Background(), protocol.OperationRunCallback, callbackBody); !errors.Is(err, protocol.ErrUnsupportedOperation) {
		t.Fatalf("callback on runtime channel = %v", err)
	}
}

func TestHelperRejectsForgedAndStaleCallbacks(t *testing.T) {
	helper := newTestHelper(t, &fakeLauncher{})
	valid := protocol.CallbackRequest{Meta: helperMeta("callback"), CallbackID: "tun-ready", Event: "tun-ready", Resource: protocol.ResourceRef{
		ID: protocol.ResourceID{1}, SessionID: helperSession().ID, Type: protocol.ResourceTun, Generation: 1, GraphEpoch: 3,
	}}
	for name, mutate := range map[string]func(*protocol.CallbackRequest){
		"runtime label": func(r *protocol.CallbackRequest) { r.CallbackID = "node-ok" },
		"event":         func(r *protocol.CallbackRequest) { r.Event = "claimed-event" },
		"foreign":       func(r *protocol.CallbackRequest) { r.Resource.SessionID = protocol.SessionID{2} },
		"type":          func(r *protocol.CallbackRequest) { r.Resource.Type = protocol.ResourceSocket },
		"generation":    func(r *protocol.CallbackRequest) { r.Resource.Generation = 0 },
		"graph":         func(r *protocol.CallbackRequest) { r.Resource.GraphEpoch++ },
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			request.Meta.OperationID += "-" + name
			mutate(&request)
			result, err := helper.RunCallback(context.Background(), request)
			if err == nil || result.Outcome != protocol.OutcomeRejected {
				t.Fatalf("result = %+v, %v", result, err)
			}
		})
	}
}

func TestHelperCancellationTimeoutOutputAndExitResults(t *testing.T) {
	if os.Getenv("ALMAGEST_COMMAND_HELPER_PROCESS") != "" {
		helperProcess()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		mode    string
		timeout time.Duration
		limit   int
		cancel  bool
		outcome protocol.Outcome
		code    string
	}{
		{"success", "success", 5 * time.Second, 1024, false, protocol.OutcomeCommitted, ""},
		{"exit", "exit", 5 * time.Second, 1024, false, protocol.OutcomeCommitted, "exit"},
		{"timeout", "sleep", 30 * time.Millisecond, 1024, false, protocol.OutcomeUncertain, "timeout"},
		{"cancel", "sleep", time.Second, 1024, true, protocol.OutcomeUncertain, "canceled"},
		{"output", "output", 5 * time.Second, 8, false, protocol.OutcomeUncertain, "output_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := map[string]string{"ALMAGEST_COMMAND_HELPER_PROCESS": test.mode}
			if value := os.Getenv("SystemRoot"); value != "" {
				environment["SystemRoot"] = value
			}
			cfg := Config{Profiles: map[string]Profile{"run": {Sandbox: "component-test", Executable: executable,
				Arguments:   []string{"-test.run=^TestHelperCancellationTimeoutOutputAndExitResults$"},
				Environment: environment, WorkingDirectory: filepath.Dir(executable),
				Timeout: test.timeout, MaxOutputBytes: test.limit, MaxConcurrent: 1}}, Commands: map[string]Command{"run": {Profile: "run", AllowNodes: true}}}
			helper, err := New(helperSession(), cfg, NewProcessLauncher("component-test"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			result, runErr := helper.RunNodeCommand(ctx, protocol.NodeCommandRequest{Meta: helperMeta(test.name), CommandID: "run"})
			if result.Outcome != test.outcome || result.Code != test.code {
				t.Fatalf("result = %+v, %v", result, runErr)
			}
			if test.name == "success" && runErr != nil {
				t.Fatal(runErr)
			}
			if test.name != "success" && runErr == nil {
				t.Fatal("expected command error")
			}
		})
	}
}

func helperProcess() {
	switch os.Getenv("ALMAGEST_COMMAND_HELPER_PROCESS") {
	case "success":
		os.Exit(0)
	case "exit":
		os.Exit(23)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "output":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 4096))
	}
	os.Exit(0)
}

func TestHelperConcurrentRetryRunsOnceAndWaitsForReceipt(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	launcher := &fakeLauncher{run: func(context.Context, Execution) ExecutionResult {
		close(started)
		<-release
		return ExecutionResult{Started: true}
	}}
	helper := newTestHelper(t, launcher)
	request := protocol.NodeCommandRequest{Meta: helperMeta("once"), CommandID: "node-ok", Slots: map[string]string{"name": "one", "generation": "1"}}
	first := make(chan protocol.CommandResult, 1)
	go func() { result, _ := helper.RunNodeCommand(context.Background(), request); first <- result }()
	<-started
	second := make(chan protocol.CommandResult, 1)
	go func() { result, _ := helper.RunNodeCommand(context.Background(), request); second <- result }()
	select {
	case <-second:
		t.Fatal("retry returned before the original command completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if (<-first).Outcome != protocol.OutcomeCommitted || (<-second).Outcome != protocol.OutcomeCommitted || len(launcher.calls()) != 1 {
		t.Fatalf("calls = %d", len(launcher.calls()))
	}
}

func TestHelperDetectsExecutableReplacement(t *testing.T) {
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "fixed-helper")
	copyFile(t, source, target)
	cfg := baseConfig(t)
	profile := cfg.Profiles["notify"]
	profile.Executable, profile.WorkingDirectory = target, directory
	cfg.Profiles["notify"] = profile
	launcher := &fakeLauncher{}
	helper, err := New(helperSession(), cfg, launcher)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("replacement")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("replace: %v, %v", writeErr, closeErr)
	}
	result, err := helper.RunNodeCommand(context.Background(), protocol.NodeCommandRequest{Meta: helperMeta("replace"), CommandID: "node-ok", Slots: map[string]string{"name": "x", "generation": "1"}})
	if err == nil || result.Code != "executable_changed" || len(launcher.calls()) != 0 {
		t.Fatalf("result = %+v, %v; calls = %d", result, err, len(launcher.calls()))
	}
}

func copyFile(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close() //nolint:errcheck
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy: %v, %v", copyErr, closeErr)
	}
}

func TestHelperRejectsUnsafeOrUnsupportedProfiles(t *testing.T) {
	base := baseConfig(t)
	for name, change := range map[string]func(*Config, *fakeLauncher){
		"relative executable": func(c *Config, _ *fakeLauncher) {
			p := c.Profiles["notify"]
			p.Executable = "helper"
			c.Profiles["notify"] = p
		},
		"interpreter": func(c *Config, _ *fakeLauncher) {
			p := c.Profiles["notify"]
			if _, err := os.Stat("/bin/sh"); err == nil {
				p.Executable = "/bin/sh"
			} else {
				p.Executable = `C:\\Windows\\System32\\cmd.exe`
			}
			c.Profiles["notify"] = p
		},
		"environment injection": func(c *Config, _ *fakeLauncher) {
			p := c.Profiles["notify"]
			p.Environment["BAD=KEY"] = "x"
			c.Profiles["notify"] = p
		},
		"unsupported sandbox": func(_ *Config, l *fakeLauncher) { l.validateErr = errors.New("not installed") },
		"unknown callback field": func(c *Config, _ *fakeLauncher) {
			callback := c.Callbacks["tun-ready"]
			callback.Slots["name"] = "runtime_claim"
			c.Callbacks["tun-ready"] = callback
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := cloneConfig(base)
			launcher := &fakeLauncher{}
			change(&cfg, launcher)
			if _, err := New(helperSession(), cfg, launcher); err == nil {
				t.Fatal("New() accepted unsafe profile")
			}
		})
	}
}

func cloneConfig(input Config) Config {
	out := Config{Profiles: make(map[string]Profile), Commands: make(map[string]Command), Callbacks: make(map[string]Callback)}
	for key, profile := range input.Profiles {
		profile.Arguments = slices.Clone(profile.Arguments)
		profile.Environment = cloneStrings(profile.Environment)
		profile.Slots = cloneSlots(profile.Slots)
		out.Profiles[key] = profile
	}
	for key, command := range input.Commands {
		out.Commands[key] = command
	}
	for key, callback := range input.Callbacks {
		callback.Slots = cloneCallbackFields(callback.Slots)
		out.Callbacks[key] = callback
	}
	return out
}

func TestHelperControlChannelsAndPeerFailure(t *testing.T) {
	launcher := &fakeLauncher{}
	helper := newTestHelper(t, launcher)
	server, client := pipeLanes()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- helper.ServeRuntime(ctx, server) }()
	peer, err := transport.New(ctx, client, helperSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.NodeCommandRequest{Meta: helperMeta("wire"), CommandID: "node-ok", Slots: map[string]string{"name": "wire", "generation": "2"}}
	var result protocol.CommandResult
	if err := peer.Call(context.Background(), protocol.OperationRunNodeCommand, request, &result); err != nil || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("wire call = %+v, %v", result, err)
	}
	_ = peer.Close()
	select {
	case err := <-served:
		if err == nil {
			t.Fatal("peer loss returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("helper did not detect peer loss")
	}
}

func TestHelperDeathFailsRuntimePeer(t *testing.T) {
	helper := newTestHelper(t, &fakeLauncher{})
	server, client := pipeLanes()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = helper.ServeRuntime(ctx, server) }()
	peer, err := transport.New(ctx, client, helperSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		helper.serveMu.Lock()
		ready := helper.runtimePeer != nil
		helper.serveMu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := helper.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.RunNodeCommand(context.Background(), protocol.NodeCommandRequest{Meta: helperMeta("after-close"), CommandID: "node-ok"}); !errors.Is(err, protocol.ErrCapabilityUnavailable) {
		t.Fatalf("command after helper close = %v", err)
	}
	select {
	case <-peer.Done():
		if peer.Err() == nil {
			t.Fatal("helper death produced no peer error")
		}
	case <-time.After(time.Second):
		t.Fatal("runtime peer did not detect helper death")
	}
}

func TestHelperBoundsAndExpiresCompletedReceipts(t *testing.T) {
	launcher := &fakeLauncher{}
	helper := newTestHelper(t, launcher)
	for i := range protocol.MaxOperationReceipts + 1 {
		request := protocol.NodeCommandRequest{Meta: helperMeta(fmt.Sprintf("receipt-%d", i)), CommandID: "owner-only"}
		result, err := helper.RunNodeCommand(context.Background(), request)
		if !errors.Is(err, protocol.ErrPolicyDenied) || result.Outcome != protocol.OutcomeRejected {
			t.Fatalf("request %d = %+v, %v", i, result, err)
		}
	}
	if len(helper.receipts) != protocol.MaxOperationReceipts || len(helper.order) != protocol.MaxOperationReceipts {
		t.Fatalf("retained %d receipts and %d order entries", len(helper.receipts), len(helper.order))
	}
	if _, err := helper.GetResult(context.Background(), protocol.OperationResultRequest{Meta: helperMeta(""), OperationID: "receipt-0"}); !errors.Is(err, protocol.ErrOperationUnknown) {
		t.Fatalf("expired receipt = %v", err)
	}
	last := fmt.Sprintf("receipt-%d", protocol.MaxOperationReceipts)
	if _, err := helper.GetResult(context.Background(), protocol.OperationResultRequest{Meta: helperMeta(""), OperationID: last}); !errors.Is(err, protocol.ErrPolicyDenied) {
		t.Fatalf("newest receipt = %v", err)
	}
}

func TestHelperStartFailureAndPrestartCancellationAreRejected(t *testing.T) {
	startError := errors.New("injected start failure")
	launcher := &fakeLauncher{run: func(context.Context, Execution) ExecutionResult {
		return ExecutionResult{Err: startError, ExitCode: -1}
	}}
	helper := newTestHelper(t, launcher)
	request := protocol.NodeCommandRequest{Meta: helperMeta("start"), CommandID: "node-ok", Slots: map[string]string{"name": "x", "generation": "1"}}
	result, err := helper.RunNodeCommand(context.Background(), request)
	if !errors.Is(err, startError) || result.Outcome != protocol.OutcomeRejected || result.Code != "start" {
		t.Fatalf("start failure = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request.Meta.OperationID = "pre-canceled"
	result, err = helper.RunNodeCommand(ctx, request)
	if !errors.Is(err, context.Canceled) || result.Outcome != protocol.OutcomeRejected || result.Code != "canceled" || len(launcher.calls()) != 1 {
		t.Fatalf("pre-canceled = %+v, %v; calls = %d", result, err, len(launcher.calls()))
	}
}

func pipeLanes() (transport.Lanes, transport.Lanes) {
	leftControl, rightControl := net.Pipe()
	leftData, rightData := net.Pipe()
	leftDiagnostic, rightDiagnostic := net.Pipe()
	return transport.Lanes{Control: leftControl, Data: leftData, Diagnostic: leftDiagnostic}, transport.Lanes{Control: rightControl, Data: rightData, Diagnostic: rightDiagnostic}
}

func TestHelperBoundsProfileConcurrency(t *testing.T) {
	testConcurrencyLimit(t, 1, 3, 1)
}

func TestHelperBoundsGlobalConcurrency(t *testing.T) {
	testConcurrencyLimit(t, maxConcurrentRuns, maxConcurrentRuns+8, maxConcurrentRuns)
}

func testConcurrencyLimit(t *testing.T, profileLimit, requests, want int) {
	t.Helper()
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	launcher := &fakeLauncher{run: func(context.Context, Execution) ExecutionResult {
		current := active.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		<-release
		active.Add(-1)
		return ExecutionResult{Started: true}
	}}
	cfg := baseConfig(t)
	profile := cfg.Profiles["notify"]
	profile.MaxConcurrent = profileLimit
	cfg.Profiles["notify"] = profile
	helper, err := New(helperSession(), cfg, launcher)
	if err != nil {
		t.Fatal(err)
	}
	// Concurrency is the subject of this test. Use the already captured
	// directory identity to avoid hashing the test binary in every goroutine.
	helper.config.profiles["notify"].executable = helper.config.profiles["notify"].directory
	var group sync.WaitGroup
	for i := range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = helper.RunNodeCommand(context.Background(), protocol.NodeCommandRequest{Meta: helperMeta(fmt.Sprintf("limited-%d", i)), CommandID: "node-ok", Slots: map[string]string{"name": "x", "generation": "1"}})
		}()
	}
	deadline := time.Now().Add(time.Second)
	for len(launcher.calls()) < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	group.Wait()
	if maximum.Load() != int32(want) {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
}

func FuzzProfileSlotResolution(f *testing.F) {
	f.Add("alpha", "9")
	f.Add("-option", "1")
	f.Add("a;touch /tmp/x", "18446744073709551615")
	executable, err := os.Executable()
	if err != nil {
		f.Fatal(err)
	}
	environment := map[string]string{"FIXED": "yes"}
	if value := os.Getenv("SystemRoot"); value != "" {
		environment["SystemRoot"] = value
	}
	profile, err := compileProfile(Profile{
		Sandbox: "fuzz", Executable: executable, Arguments: []string{"fixed", "--name=${name}", "${generation}"},
		Environment: environment, WorkingDirectory: filepath.Dir(executable),
		Timeout: time.Second, MaxOutputBytes: 1024, MaxConcurrent: 1,
		Slots: map[string]Slot{"name": {MaxLength: 16, Pattern: `[A-Za-z0-9._-]+`, Required: true}, "generation": {MaxLength: 20, Pattern: `[0-9]+`, Required: true}},
	}, &fakeLauncher{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, name, generation string) {
		execution, err := profile.resolve(map[string]string{"name": name, "generation": generation})
		if err != nil {
			return
		}
		if len(execution.Arguments) != 3 || execution.Executable != profile.profile.Executable || !slices.Contains(execution.Environment, "FIXED=yes") {
			t.Fatalf("resolved execution escaped profile: %#v", execution)
		}
		if execution.Arguments[1] != "--name="+name || execution.Arguments[2] != generation || strings.HasPrefix(execution.Arguments[2], "-") {
			t.Fatalf("slot substitution mismatch: %#v", execution.Arguments)
		}
	})
}
