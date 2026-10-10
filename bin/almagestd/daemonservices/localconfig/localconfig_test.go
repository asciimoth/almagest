package localconfig

import (
	"bytes"
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/lib/logging"
)

func TestPersistenceCompareReplaceAndReceipts(t *testing.T) {
	current := []byte("before")
	writes := 0
	persistence := New(
		func() ([]byte, error) { return bytes.Clone(current), nil },
		func(data []byte) error {
			writes++
			current = bytes.Clone(data)
			return nil
		},
	)

	initial, err := persistence.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	result := persistence.Replace(initial.Revision, "operation-1", []byte("after"))
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != daemonservices.ConfigOutcomeCommitted || result.Revision != daemonservices.ConfigRevision([]byte("after")) || writes != 1 {
		t.Fatalf("result = %#v, writes = %d", result, writes)
	}

	retried := persistence.Replace(initial.Revision, "operation-1", []byte("after"))
	if retried != result || writes != 1 {
		t.Fatalf("retried result = %#v, writes = %d; want retained result without another write", retried, writes)
	}
	stored, err := persistence.GetResult("operation-1")
	if err != nil || stored != result {
		t.Fatalf("GetResult = %#v, %v", stored, err)
	}
	if _, err := persistence.GetResult("missing"); !errors.Is(err, daemonservices.ErrConfigOperationUnknown) {
		t.Fatalf("missing GetResult error = %v", err)
	}
}

func TestWithLoggerPreservesPersistenceResults(t *testing.T) {
	current := []byte("before")
	logger := logging.NewDebugLogFactory(8)
	persistence := WithLogger(New(
		func() ([]byte, error) { return bytes.Clone(current), nil },
		func(data []byte) error { current = bytes.Clone(data); return nil },
	), logger.EngineLogger())

	result := persistence.Replace(daemonservices.ConfigRevision(current), "operation-1", []byte("after"))
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	stored, err := persistence.GetResult("operation-1")
	if err != nil || stored != result {
		t.Fatalf("GetResult = %#v, %v", stored, err)
	}
}

func TestPersistenceRejectsStaleRevisionAndOperationReuse(t *testing.T) {
	current := []byte("external edit")
	writes := 0
	persistence := New(
		func() ([]byte, error) { return bytes.Clone(current), nil },
		func(data []byte) error { writes++; current = bytes.Clone(data); return nil },
	)

	result := persistence.Replace(daemonservices.ConfigRevision([]byte("old")), "operation-1", []byte("candidate"))
	if result.Outcome != daemonservices.ConfigOutcomeRejected || result.Code != "conflict" || !errors.Is(result.Err(), daemonservices.ErrConfigRevisionConflict) || writes != 0 {
		t.Fatalf("stale replace = %#v, writes = %d", result, writes)
	}
	reused := persistence.Replace(result.Revision, "operation-1", []byte("different"))
	if reused.Err() == nil || writes != 0 {
		t.Fatalf("reused operation = %#v, writes = %d", reused, writes)
	}
}

func TestPersistenceRetainsWriteFailure(t *testing.T) {
	current := []byte("before")
	attempts := 0
	persistence := New(
		func() ([]byte, error) { return bytes.Clone(current), nil },
		func([]byte) error { attempts++; return errors.New("disk full") },
	)
	revision := daemonservices.ConfigRevision(current)
	result := persistence.Replace(revision, "operation-1", []byte("after"))
	if result.Outcome != daemonservices.ConfigOutcomeRejected || result.Err() == nil || result.Error != "disk full" || attempts != 1 {
		t.Fatalf("failed replace = %#v, attempts = %d", result, attempts)
	}
	retried := persistence.Replace(revision, "operation-1", []byte("after"))
	if retried != result || attempts != 1 {
		t.Fatalf("retried failure = %#v, attempts = %d", retried, attempts)
	}
}

func TestPersistenceRecognizesConfirmedCommitAfterReplaceError(t *testing.T) {
	current := []byte("before")
	persistence := New(
		func() ([]byte, error) { return bytes.Clone(current), nil },
		func(data []byte) error {
			current = bytes.Clone(data)
			return errors.New("post-commit durability report failed")
		},
	)
	result := persistence.Replace(daemonservices.ConfigRevision(current), "operation-1", []byte("after"))
	if err := result.Err(); err != nil {
		t.Fatalf("confirmed commit reported failure: %v", err)
	}
	if result.Outcome != daemonservices.ConfigOutcomeCommitted || result.Revision != daemonservices.ConfigRevision([]byte("after")) {
		t.Fatalf("confirmed revision = %q", result.Revision)
	}
}
