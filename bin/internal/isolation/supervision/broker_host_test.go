package supervision

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeRevoker struct {
	err   error
	calls int
}

func (f *fakeRevoker) Shutdown(context.Context) error { f.calls++; return f.err }

func TestBrokerHostRecoversBeforeUseAndAfterRevocation(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	effect := testEffect("owned")
	effect.State, effect.Identity = EffectOwned, "identity"
	if err := journal.Put(effect); err != nil {
		t.Fatal(err)
	}
	reconciler := &fakeReconciler{states: map[string]IdentityState{"owned": IdentityAbsent}, inspect: make(map[string]int)}
	revoker := &fakeRevoker{}
	host := BrokerHost{Journal: journal, Reconciler: reconciler, Broker: revoker, PerEffect: time.Second}
	if err := host.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(journal.Snapshot()) != 0 {
		t.Fatal("startup recovery retained an absent effect")
	}
	if err := host.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if revoker.calls != 1 {
		t.Fatalf("shutdown calls = %d", revoker.calls)
	}
}

func TestBrokerHostKeepsRecoveryVisible(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	effect := testEffect("uncertain")
	effect.State, effect.Identity = EffectUncertain, "identity"
	if err := journal.Put(effect); err != nil {
		t.Fatal(err)
	}
	reconciler := &fakeReconciler{states: map[string]IdentityState{"uncertain": IdentityUnknown}, inspect: make(map[string]int)}
	revoker := &fakeRevoker{err: errors.New("broker cleanup failed")}
	host := BrokerHost{Journal: journal, Reconciler: reconciler, Broker: revoker, PerEffect: time.Second}
	err = host.Revoke(context.Background())
	if !errors.Is(err, ErrRecoveryUncertain) || len(journal.Snapshot()) != 1 {
		t.Fatalf("Revoke() = %v, records = %#v", err, journal.Snapshot())
	}
}

func TestBrokerHostRejectsIncompleteComposition(t *testing.T) {
	for _, host := range []BrokerHost{{}, {Journal: &Journal{}}, {Reconciler: &fakeReconciler{}}, {Broker: &fakeRevoker{}}} {
		if err := host.Recover(context.Background()); !errors.Is(err, ErrInvalidComposition) {
			t.Fatalf("Recover() = %v", err)
		}
		if err := host.Revoke(context.Background()); !errors.Is(err, ErrInvalidComposition) {
			t.Fatalf("Revoke() = %v", err)
		}
	}
}
