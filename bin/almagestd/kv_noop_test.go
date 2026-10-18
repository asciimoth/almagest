package main

import (
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestBboltMaintenanceDoesNotCommitUnchangedState(t *testing.T) {
	m, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	transactionID := func() int {
		t.Helper()
		var id int
		if err := m.db.View(func(tx *bbolt.Tx) error { id = tx.ID(); return nil }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for name, run := range map[string]func() error{
		"DNS expiration":    func() error { return m.cleanupExpiredDNS(time.Now()) },
		"secret references": func() error { return m.reconcileSecretNodeReferences(nil) },
		"KV ownership":      func() error { return m.reconcileKVOwnerEntries(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			before := transactionID()
			if err := run(); err != nil {
				t.Fatal(err)
			}
			if after := transactionID(); after != before {
				t.Fatalf("unchanged maintenance committed a transaction: %d -> %d", before, after)
			}
		})
	}
}
