package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testRotatingLogOpen(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func TestRotatingLogBoundsAndOrdersBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	log, err := newRotatingLogFile(path, 4, 2, testRotatingLogOpen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = log.Write([]byte("abcdefghijkl")); err != nil {
		t.Fatal(err)
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"daemon.log":   "ijkl",
		"daemon.log.1": "efgh",
		"daemon.log.2": "abcd",
	} {
		data, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if readErr != nil {
			t.Errorf("read %s: %v", name, readErr)
		} else if string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
	if _, err = os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected third backup: %v", err)
	}
}

func TestRotatingLogReportsRotationFailureAndRemainsClosable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	log, err := newRotatingLogFile(path, 4, 1, testRotatingLogOpen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = log.Write([]byte("full")); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected rename failure")
	log.rename = func(string, string) error { return wantErr }
	if _, err = log.Write([]byte("x")); !errors.Is(err, wantErr) {
		t.Fatalf("rotation error = %v, want %v", err, wantErr)
	}
	if err = log.Close(); err != nil {
		t.Fatalf("close after rotation failure: %v", err)
	}
}

func TestRotatingLogCloseFlushesAndRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	log, err := newRotatingLogFile(path, 64, 1, testRotatingLogOpen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = log.Write([]byte("shutdown record\n")); err != nil {
		t.Fatal(err)
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	if err = log.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, err = log.Write([]byte("late")); !errors.Is(err, errRotatingLogClosed) {
		t.Fatalf("write after close = %v, want %v", err, errRotatingLogClosed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shutdown record\n" {
		t.Fatalf("persisted log = %q", data)
	}
}

func TestRotatingLogRejectsOpenFailure(t *testing.T) {
	wantErr := errors.New("injected open failure")
	_, err := newRotatingLogFile("daemon.log", 64, 1, func(string) (*os.File, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("newRotatingLogFile() error = %v, want %v", err, wantErr)
	}
}
