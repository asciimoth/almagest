package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenderSlotTemplates(t *testing.T) {
	got, err := RenderSlotTemplates(
		map[string]string{
			"literal": "static",
			"name":    "{{ .Name }}",
		},
		map[string]any{"Name": "tun0"},
	)
	if err != nil {
		t.Fatalf("RenderSlotTemplates: %v", err)
	}
	if got["literal"] != "static" {
		t.Fatalf("literal slot = %q, want static", got["literal"])
	}
	if got["name"] != "tun0" {
		t.Fatalf("name slot = %q, want tun0", got["name"])
	}
}

func TestRenderSlotTemplatesMissingKeyFails(t *testing.T) {
	_, err := RenderSlotTemplates(
		map[string]string{"missing": "{{ .Missing }}"},
		map[string]any{},
	)
	if err == nil {
		t.Fatal("RenderSlotTemplates succeeded with a missing template key")
	}
}

func TestRegistryRendersSlotsAndDropsRenderedEmptyArgs(t *testing.T) {
	registry := newTestRegistry(t, Config{Commands: map[string]AllowedCommand{
		"notify-dns": {
			Command: []string{
				"/usr/local/bin/notify-dns",
				"--server",
				"${server}",
				"${optional}",
				"",
			},
			Slots: map[string]Slot{
				"server": {
					MaxLength: 16,
					Pattern:   `[0-9.]+`,
					Required:  true,
				},
				"optional": {
					MaxLength:  8,
					AllowEmpty: true,
				},
			},
		},
	}})

	got, err := registry.resolve(Request{
		Name:  "notify-dns",
		Slots: map[string]string{"server": "192.0.2.53"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	want := []string{"/usr/local/bin/notify-dns", "--server", "192.0.2.53", ""}
	if len(got.Command) != len(want) {
		t.Fatalf("resolved argv = %#v, want %#v", got.Command, want)
	}
	for i := range want {
		if got.Command[i] != want[i] {
			t.Fatalf("resolved argv[%d] = %q, want %q", i, got.Command[i], want[i])
		}
	}
}

func TestRegistryRejectsUnsafeDefinitions(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "slot in executable",
			cfg: Config{Commands: map[string]AllowedCommand{
				"bad": {
					Command: []string{"${program}"},
					Slots:   map[string]Slot{"program": {MaxLength: 8, Required: true}},
				},
			}},
			want: "executable must be fixed",
		},
		{
			name: "unbounded slot",
			cfg: Config{Commands: map[string]AllowedCommand{
				"bad": {
					Command: []string{"/bin/echo", "${value}"},
					Slots:   map[string]Slot{"value": {}},
				},
			}},
			want: "MaxLength must be greater than zero",
		},
		{
			name: "undeclared slot",
			cfg: Config{Commands: map[string]AllowedCommand{
				"bad": {
					Command: []string{"/bin/echo", "${value}"},
				},
			}},
			want: "undeclared slot",
		},
		{
			name: "unused slot",
			cfg: Config{Commands: map[string]AllowedCommand{
				"bad": {
					Command: []string{"/bin/echo"},
					Slots:   map[string]Slot{"value": {MaxLength: 8}},
				},
			}},
			want: "declared but not used",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRunner(nil, tt.cfg)
			if err == nil {
				t.Fatal("NewRunner succeeded, want validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewRunner error = %v, want to contain %q", err, tt.want)
			}
		})
	}
}

func TestAllowlistValidationEdges(t *testing.T) {
	if (AllowedCommand{}).Configured() || !(AllowedCommand{Command: []string{"echo"}}).Configured() {
		t.Fatal("Configured returned an incorrect value")
	}
	definitions := []struct {
		name string
		cfg  AllowedCommand
	}{
		{name: "", cfg: AllowedCommand{Command: []string{"echo"}}},
		{name: strings.Repeat("x", maxCommandNameLen+1), cfg: AllowedCommand{Command: []string{"echo"}}},
		{name: "bad/name", cfg: AllowedCommand{Command: []string{"echo"}}},
		{name: "empty-argv", cfg: AllowedCommand{}},
		{name: "empty-executable", cfg: AllowedCommand{Command: []string{" "}}},
		{name: "negative-timeout", cfg: AllowedCommand{Command: []string{"echo"}, Timeout: -time.Second}},
		{name: "bad-env-empty", cfg: AllowedCommand{Command: []string{"echo"}, Env: map[string]string{"": "x"}}},
		{name: "bad-env-equals", cfg: AllowedCommand{Command: []string{"echo"}, Env: map[string]string{"A=B": "x"}}},
		{name: "empty-slot", cfg: AllowedCommand{Command: []string{"echo", "${x}"}, Slots: map[string]Slot{"": {MaxLength: 1}}}},
		{name: "long-slot", cfg: AllowedCommand{Command: []string{"echo", "${x}"}, Slots: map[string]Slot{strings.Repeat("x", maxSlotNameLen+1): {MaxLength: 1}}}},
		{name: "bad-slot", cfg: AllowedCommand{Command: []string{"echo", "${x}"}, Slots: map[string]Slot{"1x": {MaxLength: 1}}}},
		{name: "large-slot", cfg: AllowedCommand{Command: []string{"echo", "${x}"}, Slots: map[string]Slot{"x": {MaxLength: MaxSlotValueLength + 1}}}},
		{name: "required-empty-slot", cfg: AllowedCommand{Command: []string{"echo", "${x}"}, Slots: map[string]Slot{"x": {MaxLength: 1, Required: true, AllowEmpty: true}}}},
		{name: "bad-pattern", cfg: AllowedCommand{Command: []string{"echo", "${x}"}, Slots: map[string]Slot{"x": {MaxLength: 1, Pattern: "["}}}},
		{name: "unterminated-placeholder", cfg: AllowedCommand{Command: []string{"echo", "${x"}}},
	}
	for _, test := range definitions {
		t.Run(test.name, func(t *testing.T) {
			if _, err := compileAllowedCommand(test.name, test.cfg); err == nil {
				t.Fatal("invalid allowlist definition was accepted")
			}
		})
	}
	var registry *registry
	if _, err := registry.resolve(Request{Name: "x"}); err == nil {
		t.Fatal("nil registry accepted request")
	}
	if _, err := RenderSlotTemplates(map[string]string{"bad/name": "value"}, nil); err == nil {
		t.Fatal("invalid template slot name was accepted")
	}
}

func TestRegistryRejectsUnsafeRequests(t *testing.T) {
	registry := newTestRegistry(t, Config{Commands: map[string]AllowedCommand{
		"notify-dns": {
			Command: []string{"/usr/local/bin/notify-dns", "${server}"},
			Slots: map[string]Slot{
				"server": {
					MaxLength: 16,
					Pattern:   `[0-9.]+`,
					Required:  true,
				},
			},
		},
	}})

	tests := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "unknown command",
			req:  Request{Name: "sh"},
			want: "not allowed",
		},
		{
			name: "unknown slot",
			req: Request{
				Name:  "notify-dns",
				Slots: map[string]string{"server": "192.0.2.53", "extra": "value"},
			},
			want: "unknown slot",
		},
		{
			name: "missing required slot",
			req:  Request{Name: "notify-dns"},
			want: "missing required slot",
		},
		{
			name: "slot too long",
			req: Request{
				Name:  "notify-dns",
				Slots: map[string]string{"server": "192.0.2.53-overlong"},
			},
			want: "exceeds 16 bytes",
		},
		{
			name: "pattern mismatch",
			req: Request{
				Name:  "notify-dns",
				Slots: map[string]string{"server": "192.0.2.53;rm"},
			},
			want: "does not match required pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := registry.resolve(tt.req)
			if err == nil {
				t.Fatal("resolve succeeded, want validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolve error = %v, want to contain %q", err, tt.want)
			}
		})
	}
}

func TestRunnerLogsNameAndStatusOnly(t *testing.T) {
	logger := &recordingLogger{}
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{
		"named-command": helperCommand(t, "success", "secret-output"),
	}})

	runner.handle(request{req: Request{
		Name:  "named-command",
		Label: "sysnet-linux.dns-configured",
	}})

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "sysnet-linux.dns-configured") {
		t.Fatalf("logs do not mention command label:\n%s", logs)
	}
	if !strings.Contains(logs, "named-command") {
		t.Fatalf("logs do not mention allowlist command name:\n%s", logs)
	}
	if strings.Contains(logs, "secret-output") {
		t.Fatalf("logs leaked argv:\n%s", logs)
	}
	if !strings.Contains(logs, "status=success") {
		t.Fatalf("logs do not contain success status:\n%s", logs)
	}
}

func TestRunnerRejectsDisallowedCommandRequest(t *testing.T) {
	logger := &recordingLogger{}
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{
		"allowed": helperCommand(t, "success"),
	}})

	runner.handle(request{req: Request{Name: "sh"}})

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "not allowed") {
		t.Fatalf("logs do not contain allowlist rejection:\n%s", logs)
	}
	if strings.Contains(logs, "status=success") {
		t.Fatalf("disallowed command appears to have run:\n%s", logs)
	}
}

func TestRunnerLogsExitStatus(t *testing.T) {
	logger := &recordingLogger{}
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{
		"failing-command": helperCommand(t, "fail"),
	}})

	cmd, err := runner.registry.resolve(Request{Name: "failing-command"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	runner.runSync(context.Background(), cmd)

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "status=exit-code-7") {
		t.Fatalf("logs do not contain exit status:\n%s", logs)
	}
}

func TestRunnerSyncTimeout(t *testing.T) {
	logger := &recordingLogger{}
	command := helperCommand(t, "sleep")
	command.Timeout = 10 * time.Millisecond
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{
		"sleep-command": command,
	}})

	cmd, err := runner.registry.resolve(Request{Name: "sleep-command"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	runner.runSync(context.Background(), cmd)

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "status=timeout") {
		t.Fatalf("logs do not contain timeout status:\n%s", logs)
	}
}

func newTestRegistry(t *testing.T, cfg Config) *registry {
	t.Helper()
	runner := newTestRunner(t, nil, cfg)
	return runner.registry
}

func newTestRunner(t *testing.T, logger Logger, cfg Config) *Runner {
	t.Helper()
	runner, err := NewRunner(logger, cfg)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func helperCommand(t *testing.T, mode string, args ...string) AllowedCommand {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	argv := []string{exe, "-test.run=TestCommandHelperProcess", "--", mode}
	argv = append(argv, args...)
	return AllowedCommand{
		Command: argv,
		Env: map[string]string{
			"ALMAGEST_COMMAND_HELPER": "1",
		},
		Sync: true,
	}
}

func TestCommandHelperProcess(t *testing.T) {
	if os.Getenv("ALMAGEST_COMMAND_HELPER") != "1" {
		return
	}

	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	switch mode {
	case "success":
		os.Exit(0)
	case "fail":
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
		os.Exit(0)
	case "spawn-delayed-write":
		marker := helperCommandArg("spawn-delayed-write", 1)
		child := exec.Command(
			os.Args[0],
			"-test.run=TestCommandHelperProcess",
			"--",
			"delayed-write",
			marker,
		)
		child.Env = os.Environ()
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(marker+".ready", []byte("ready"), 0o600); err != nil {
			os.Exit(4)
		}
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "delayed-write":
		marker := helperCommandArg("delayed-write", 1)
		time.Sleep(time.Second)
		if err := os.WriteFile(marker, []byte("survived"), 0o600); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(2)
	}
}

func TestRunnerLifecycleSubmitsSyncAndBackgroundCommands(t *testing.T) {
	logger := &recordingLogger{}
	syncCommand := helperCommand(t, "success")
	syncCommand.Sync = true
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{
		"sync":       syncCommand,
		"background": helperCommand(t, "success"),
	}})
	runner.Start(nil) //nolint:staticcheck // Verify the documented nil-context fallback.
	runner.Start(context.Background())
	if !runner.Submit(Request{Name: "sync"}) || !runner.Submit(Request{Name: "background"}) {
		t.Fatal("Submit returned false for a running runner")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(logger.messages()) < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	runner.Close()
	runner.Close()
	if runner.Submit(Request{Name: "sync"}) {
		t.Fatal("Submit returned true after Close")
	}
	if len(logger.messages()) < 4 {
		t.Fatalf("logs = %#v, want start and finish for both commands", logger.messages())
	}
}

func helperCommandArg(mode string, offset int) string {
	for i, arg := range os.Args {
		if arg == "--" && i+1+offset < len(os.Args) && os.Args[i+1] == mode {
			return os.Args[i+1+offset]
		}
	}
	os.Exit(6)
	return ""
}

type recordingLogger struct {
	mu   sync.Mutex
	logs []string
}

func (l *recordingLogger) Debugf(format string, args ...any) {
	l.append(format, args...)
}

func (l *recordingLogger) Infof(format string, args ...any) {
	l.append(format, args...)
}

func (l *recordingLogger) Warnf(format string, args ...any) {
	l.append(format, args...)
}

func (l *recordingLogger) Errf(format string, args ...any) {
	l.append(format, args...)
}

func (l *recordingLogger) append(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.logs))
	copy(out, l.logs)
	return out
}
