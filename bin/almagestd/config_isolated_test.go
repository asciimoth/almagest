package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/configpersistence"
	"github.com/asciimoth/almagest/bin/internal/isolation/configwriter"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestConfigCoordinatorUsesIsolatedTransactionsForUpdateAndAutosave(t *testing.T) {
	conf, err := ParseConfig([]byte(`{"nodes": {}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := conf.MainToBytes()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	path := filepath.Join(t.TempDir(), "config.hujson")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	writer, err := configwriter.New(session, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	a, z := net.Pipe()
	b, y := net.Pipe()
	c, x := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = writer.Serve(ctx, transport.Lanes{Control: z, Data: y, Diagnostic: x}) }()
	remote, err := transport.New(ctx, transport.Lanes{Control: a, Data: b, Diagnostic: c}, session, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	persistence, err := configpersistence.New(remote, session, 1, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	document, err := persistence.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	saver := newConfigAutosaver(
		engine, conf, persistence, logging.NewDebugLogFactory(8).EngineLogger(), time.Millisecond, document.Revision,
	)
	snapshot, err := saver.Snapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := saver.Update(
		`{"nodes":{"admin":{"Class":"test/AdminAPIBatchNode"}}}`,
		snapshot.Revision,
	)
	if err != nil || !bytes.Contains([]byte(updated.Snapshot.Text), []byte(`"admin"`)) {
		t.Fatalf("Update() = %+v, %v", updated, err)
	}
	if _, err := engine.AddNode(almagest.MainWorkspace, "test/AdminAPIBatchNode", "autosaved"); err != nil {
		t.Fatal(err)
	}
	saver.Schedule()
	if err := saver.persistNodeConfig(); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range [][]byte{[]byte(`"admin"`), []byte(`"autosaved"`)} {
		if !bytes.Contains(written, name) {
			t.Fatalf("isolated target does not contain %s: %s", name, written)
		}
	}
}
