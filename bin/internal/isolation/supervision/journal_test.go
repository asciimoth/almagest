package supervision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func testSessionID(value byte) protocol.SessionID {
	var id protocol.SessionID
	id[0] = value
	return id
}

func testEffect(id string) Effect {
	return Effect{ID: id, SessionID: testSessionID(1), ResourceType: protocol.ResourceTun, State: EffectPending}
}

func TestJournalPersistsTransitionsAndResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	journal, err := OpenJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	pending := testEffect("operation-1")
	if err := journal.Put(pending); err != nil {
		t.Fatal(err)
	}
	owned := pending
	owned.State, owned.Identity = EffectOwned, "linux-ifindex:42;handle:opaque"
	if err := journal.Put(owned); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); len(got) != 1 || got[0] != owned {
		t.Fatalf("reopened records = %#v", got)
	}
	if err := reopened.Resolve(owned.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); len(got) != 0 {
		t.Fatalf("resolved records = %#v", got)
	}
}

func TestJournalPreparesOwnedIdentityReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	journal, err := OpenJournal(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	pending := testEffect("replace")
	if err := journal.Put(pending); err != nil {
		t.Fatal(err)
	}
	owned := pending
	owned.State, owned.Identity = EffectOwned, "old-native"
	if err := journal.Put(owned); err != nil {
		t.Fatal(err)
	}
	if err := journal.PrepareReplacement(pending); err != nil {
		t.Fatal(err)
	}
	replacement := pending
	replacement.State, replacement.Identity = EffectOwned, "new-native"
	if err := journal.Put(replacement); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); len(got) != 1 || got[0] != replacement {
		t.Fatalf("replacement records = %#v", got)
	}
	if err := reopened.PrepareReplacement(pending); err != nil {
		t.Fatal(err)
	}
	if err := reopened.PrepareReplacement(pending); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("second replacement preparation error = %v", err)
	}
}

func TestJournalRejectsAdoptionLimitsAndTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	journal, err := OpenJournal(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := testEffect("first")
	if err := journal.Put(first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.SessionID = testSessionID(2)
	if err := journal.Put(changed); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("session adoption error = %v", err)
	}
	if err := journal.Put(testEffect("second")); !errors.Is(err, ErrJournalFull) {
		t.Fatalf("limit error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path, 1); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestJournalPersistenceFailureDoesNotPublishState(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "recovery.json")
	journal, err := OpenJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := journal.Put(testEffect("must-not-publish")); err == nil {
		t.Fatal("Put succeeded without its fixed parent")
	}
	if got := journal.Snapshot(); len(got) != 0 {
		t.Fatalf("in-memory state changed after failure: %#v", got)
	}
}

type fakeReconciler struct {
	states  map[string]IdentityState
	inspect map[string]int
	cleaned []string
	err     error
}

func (f *fakeReconciler) Inspect(_ context.Context, effect Effect) (IdentityState, error) {
	f.inspect[effect.ID]++
	return f.states[effect.ID], f.err
}
func (f *fakeReconciler) Cleanup(_ context.Context, effect Effect) error {
	f.cleaned = append(f.cleaned, effect.ID)
	if f.err != nil {
		return f.err
	}
	f.states[effect.ID] = IdentityAbsent
	return nil
}

func TestRecoveryUsesIdentityAndKeepsForeignUncertainAndStrictEffects(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]IdentityState{"absent": IdentityAbsent, "owned": IdentityOwned, "foreign": IdentityForeign, "unknown": IdentityUnknown} {
		effect := testEffect(id)
		effect.State, effect.Identity = EffectOwned, "native:"+id
		if id == "unknown" {
			effect.RetainOnFailure = true
		}
		if err := journal.Put(effect); err != nil {
			t.Fatal(err)
		}
		_ = state
	}
	reconciler := &fakeReconciler{states: map[string]IdentityState{"absent": IdentityAbsent, "owned": IdentityOwned, "foreign": IdentityForeign, "unknown": IdentityOwned}, inspect: make(map[string]int)}
	result, err := journal.Recover(context.Background(), reconciler, time.Second)
	if !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("Recover error = %v", err)
	}
	if len(result.Resolved) != 2 || len(result.Remaining) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(reconciler.cleaned) != 1 || reconciler.cleaned[0] != "owned" {
		t.Fatalf("cleaned = %v", reconciler.cleaned)
	}
	if reconciler.inspect["owned"] != 2 {
		t.Fatalf("owned inspections = %d", reconciler.inspect["owned"])
	}
}

func TestRecoveryTimeoutRetainsRecord(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 1)
	if err != nil {
		t.Fatal(err)
	}
	effect := testEffect("timeout")
	effect.State, effect.Identity = EffectOwned, "native:timeout"
	if err := journal.Put(effect); err != nil {
		t.Fatal(err)
	}
	reconciler := blockingReconciler{}
	started := time.Now()
	result, err := journal.Recover(context.Background(), reconciler, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Remaining) != 1 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("recovery wait was not bounded")
	}
}

type stubbornReconciler struct{}

func (stubbornReconciler) Inspect(context.Context, Effect) (IdentityState, error) {
	return IdentityOwned, nil
}
func (stubbornReconciler) Cleanup(context.Context, Effect) error { return nil }

func TestRecoveryRequiresAbsentProofAfterCleanup(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 1)
	if err != nil {
		t.Fatal(err)
	}
	effect := testEffect("still-present")
	effect.State, effect.Identity = EffectOwned, "native:still-present"
	if err := journal.Put(effect); err != nil {
		t.Fatal(err)
	}
	result, err := journal.Recover(context.Background(), stubbornReconciler{}, time.Second)
	if !errors.Is(err, ErrRecoveryUncertain) || len(result.Resolved) != 0 || len(result.Remaining) != 1 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if result.Remaining[0].State != EffectUncertain {
		t.Fatalf("remaining effect = %#v", result.Remaining[0])
	}
}

func TestJournalConcurrentWritesStayBoundedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	journal, err := OpenJournal(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 32)
	for index := range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- journal.Put(testEffect(fmt.Sprintf("effect-%02d", index)))
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenJournal(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Snapshot()); got != 32 {
		t.Fatalf("durable record count = %d", got)
	}
}

type blockingReconciler struct{}

func (blockingReconciler) Inspect(ctx context.Context, _ Effect) (IdentityState, error) {
	<-ctx.Done()
	return IdentityUnknown, ctx.Err()
}
func (blockingReconciler) Cleanup(context.Context, Effect) error { return nil }

func FuzzJournalDecoder(f *testing.F) {
	validPath := filepath.Join(f.TempDir(), "seed.json")
	journal, err := OpenJournal(validPath, 4)
	if err != nil {
		f.Fatal(err)
	}
	if err := journal.Put(testEffect("seed")); err != nil {
		f.Fatal(err)
	}
	valid, err := os.ReadFile(validPath)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"document":{},"sha256":"00"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		records, err := decodeJournal(data, 4)
		if err == nil {
			if len(records) > 4 {
				t.Fatal("decoder exceeded record limit")
			}
			for id, record := range records {
				if id != record.ID || record.validate() != nil {
					t.Fatalf("invalid accepted record: %#v", record)
				}
			}
		}
	})
}
