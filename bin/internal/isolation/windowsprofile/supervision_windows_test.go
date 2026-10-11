//go:build windows

package windowsprofile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"golang.org/x/sys/windows"
)

func TestNativeSupervisedLifecycle(t *testing.T) {
	requireWinVM(t)
	for _, role := range []Role{Runtime, Writer, Command} {
		for _, action := range []string{"kill", "close"} {
			t.Run(string(role)+"/"+action, func(t *testing.T) {
				root, exe := fixture(t)
				identity, storage := identity(t, role, root, exe)
				a, b, err := transport.PipePair()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = a.Close(); _ = b.Close() }()
				p, err := Launch(context.Background(), Request{Plan: DefaultPlan(role), Identity: identity, Executable: exe, Storage: storage, Pipes: []windows.Handle{windows.Handle(b.Fd())}})
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close() //nolint:errcheck
				_ = b.Close()
				if err := a.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := json.NewEncoder(a).Encode(probeInput{Hang: true}); err != nil {
					t.Fatal(err)
				}
				ready := make(chan supervision.ReadyResult)
				stop := func(context.Context) error { return nil }
				observed, err := p.Supervise(ready, stop)
				if err != nil {
					t.Fatal(err)
				}
				wantRole := map[Role]protocol.Role{Runtime: protocol.RoleRuntime, Writer: protocol.RoleConfigWriter, Command: protocol.RoleCommandHelper}[role]
				if observed.Role() != wantRole || observed.Ready() != ready {
					t.Fatal("adapter changed the launched role or readiness channel")
				}
				if _, err := p.Supervise(ready, stop); !errors.Is(err, supervision.ErrInvalidComposition) {
					t.Fatalf("second supervisor: %v", err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				if err := observed.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("hung child reported clean stop: %v", err)
				}
				cancel()
				if action == "close" {
					if err := p.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := observed.Kill(); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					code, err := p.Wait(ctx)
					cancel()
					if err != nil || code != 1 {
						t.Fatalf("kill discarded native exit proof: %d, %v", code, err)
					}
				}
				select {
				case err := <-observed.Done():
					if err == nil || !strings.Contains(err.Error(), "exited with status 1") {
						t.Fatalf("native exit was not observed: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("native exit waiter did not finish")
				}
				if err := observed.Stop(context.Background()); err != nil {
					t.Fatalf("confirmed exit did not complete stop: %v", err)
				}
			})
		}
	}
}

func TestSuperviseRejectsUnlaunchedRole(t *testing.T) {
	for _, role := range []Role{Runtime, Writer, Command, Network, "unknown"} {
		p := &Process{role: role}
		if _, err := p.Supervise(make(chan supervision.ReadyResult), func(context.Context) error { return nil }); !errors.Is(err, supervision.ErrInvalidComposition) {
			t.Fatalf("unlaunched %s: %v", role, err)
		}
		if err := p.Kill(); err == nil {
			t.Fatal("kill accepted an absent job")
		}
	}
}
