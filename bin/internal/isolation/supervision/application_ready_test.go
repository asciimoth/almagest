package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestSupervisorApplicationReadinessBarrier(t *testing.T) {
	for _, applicationFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "restrictions_first", true: "application_first"}[applicationFirst], func(t *testing.T) {
			p := newFakeProcess(protocol.RoleRuntime)
			application := make(chan struct{})
			opts := testOptions()
			opts.ApplicationReady = application
			opts.StartupTimeout = 2 * time.Second
			s, err := New(&fakeHost{}, nil, opts, p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := runSupervisor(t, s, ctx)
			if applicationFirst {
				close(application)
			} else {
				p.ready <- ready(protocol.RoleRuntime)
			}
			select {
			case <-s.Ready():
				t.Fatal("published readiness before both startup barriers")
			case <-time.After(20 * time.Millisecond):
			}
			if status := s.Status(); status.Ready || status.Roles[0].Ready {
				t.Fatalf("premature ready status: %+v", status)
			}
			if applicationFirst {
				p.ready <- ready(protocol.RoleRuntime)
			} else {
				close(application)
			}
			waitReady(t, s)
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestSupervisorApplicationReadinessFailure(t *testing.T) {
	for _, failure := range []string{"runtime_exit", "timeout", "canceled", "not_closed"} {
		t.Run(failure, func(t *testing.T) {
			p := newFakeProcess(protocol.RoleRuntime)
			p.ready <- ready(protocol.RoleRuntime)
			application := make(chan struct{}, 1)
			opts := testOptions()
			opts.ApplicationReady = application
			s, err := New(&fakeHost{}, nil, opts, p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := runSupervisor(t, s, ctx)
			switch failure {
			case "runtime_exit":
				p.once.Do(func() { p.done <- errors.New("startup crash") })
			case "canceled":
				cancel()
			case "not_closed":
				application <- struct{}{}
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrStartupFailed) {
					t.Fatalf("startup error: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("startup did not stop")
			}
			select {
			case <-s.Ready():
				t.Fatal("failed startup published readiness")
			default:
			}
		})
	}
}

func TestSupervisorRequiresApplicationReadinessSignal(t *testing.T) {
	opts := testOptions()
	opts.ApplicationReady = nil
	if _, err := New(&fakeHost{}, nil, opts, newFakeProcess(protocol.RoleRuntime)); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("missing application barrier: %v", err)
	}
}
