//go:build unix

package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerSyncTimeoutKillsDescendantsAndReturnsPromptly(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	logger := &recordingLogger{}
	command := helperCommand(t, "spawn-delayed-write", marker)
	command.Timeout = 100 * time.Millisecond
	runner := newTestRunner(t, logger, Config{Commands: map[string]AllowedCommand{
		"spawn-command": command,
	}})

	cmd, err := runner.registry.resolve(Request{Name: "spawn-command"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	start := time.Now()
	runner.runSync(context.Background(), cmd)
	elapsed := time.Since(start)

	if !strings.Contains(strings.Join(logger.messages(), "\n"), "status=timeout") {
		t.Fatal("command did not report timeout status")
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("timed-out command returned after %s, want less than 500ms", elapsed)
	}
	if _, err := os.Stat(marker + ".ready"); err != nil {
		t.Fatalf("descendant was not started before timeout: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("descendant remained alive after command timeout")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat descendant marker: %v", err)
	}
}
