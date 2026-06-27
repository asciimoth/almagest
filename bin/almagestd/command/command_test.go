package command

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenderTemplatesAndDropsRenderedEmptyArgs(t *testing.T) {
	cmd := Command{
		Command: []string{
			"prog",
			"--name={{ .Name }}",
			"{{ .Optional }}",
			"",
		},
	}

	got, err := Render(cmd, map[string]any{
		"Name":     "tun0",
		"Optional": "",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := []string{"prog", "--name=tun0", ""}
	if len(got.Command) != len(want) {
		t.Fatalf("rendered argv = %#v, want %#v", got.Command, want)
	}
	for i := range want {
		if got.Command[i] != want[i] {
			t.Fatalf("rendered argv[%d] = %q, want %q", i, got.Command[i], want[i])
		}
	}
}

func TestRenderMissingTemplateKeyFails(t *testing.T) {
	_, err := Render(
		Command{Command: []string{"prog", "{{ .Missing }}"}},
		map[string]any{},
	)
	if err == nil {
		t.Fatal("Render succeeded with a missing template key")
	}
}

func TestRunnerLogsNameAndStatusOnly(t *testing.T) {
	logger := &recordingLogger{}
	runner := NewRunner(logger)
	runner.runSync(context.Background(), Command{
		Name:    "named-command",
		Command: []string{"sh", "-c", "printf secret-output"},
		Sync:    true,
	})

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "named-command") {
		t.Fatalf("logs do not mention command name:\n%s", logs)
	}
	if strings.Contains(logs, "secret-output") || strings.Contains(logs, "printf secret") {
		t.Fatalf("logs leaked command output or argv:\n%s", logs)
	}
	if !strings.Contains(logs, "status=success") {
		t.Fatalf("logs do not contain success status:\n%s", logs)
	}
}

func TestRunnerLogsExitStatus(t *testing.T) {
	logger := &recordingLogger{}
	runner := NewRunner(logger)
	runner.runSync(context.Background(), Command{
		Name:    "failing-command",
		Command: []string{"sh", "-c", "exit 7"},
		Sync:    true,
	})

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "status=exit-code-7") {
		t.Fatalf("logs do not contain exit status:\n%s", logs)
	}
}

func TestRunnerSyncTimeout(t *testing.T) {
	logger := &recordingLogger{}
	runner := NewRunner(logger)
	runner.runSync(context.Background(), Command{
		Name:    "sleep-command",
		Command: []string{"sh", "-c", "sleep 1"},
		Sync:    true,
		Timeout: 10 * time.Millisecond,
	})

	logs := strings.Join(logger.messages(), "\n")
	if !strings.Contains(logs, "status=timeout") {
		t.Fatalf("logs do not contain timeout status:\n%s", logs)
	}
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
