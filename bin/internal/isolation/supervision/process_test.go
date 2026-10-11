package supervision

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestStartedProcessWaitsForExit(t *testing.T) {
	exit := make(chan struct{})
	var stops atomic.Int32
	p, err := NewStartedProcess(protocol.RoleRuntime, make(chan ReadyResult), func() error {
		<-exit
		return nil
	}, func(context.Context) error { stops.Add(1); return nil }, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer close(exit)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := p.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("stop without exit: %v", err)
			}
		})
	}
	wg.Wait()
	if stops.Load() != 1 {
		t.Fatalf("graceful requests=%d", stops.Load())
	}
}

func TestStartedProcessHungGracefulRequest(t *testing.T) {
	unblock := make(chan struct{})
	defer close(unblock)
	p, err := NewStartedProcess(protocol.RoleRuntime, make(chan ReadyResult), func() error {
		<-unblock
		return nil
	}, func(context.Context) error { <-unblock; return nil }, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung stop: %v", err)
	}
	if err := p.Stop(nil); !errors.Is(err, ErrInvalidComposition) { //nolint:staticcheck // Deliberately verify invalid input handling.
		t.Fatal(err)
	}
}

func TestStartedProcessChild(t *testing.T) {
	if os.Getenv("ALMAGEST_SUPERVISION_TEST_CHILD") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestStartedProcessNativeLifecycle(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "forced"}[force], func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStartedProcessChild$") //nolint:gosec // Fixed test subprocess.
			cmd.Env = append(os.Environ(), "ALMAGEST_SUPERVISION_TEST_CHILD=1")
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close() //nolint:errcheck
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			proof := make(chan ReadyResult, 1)
			proof <- ready(protocol.RoleRuntime)
			p, err := NewStartedProcess(protocol.RoleRuntime, proof, cmd.Wait, func(context.Context) error {
				if force {
					return errors.New("graceful channel lost")
				}
				return input.Close()
			}, cmd.Process.Kill)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if force {
				if err := p.Stop(ctx); err == nil {
					t.Fatal("failed graceful request reported success")
				}
				if err := p.Kill(); err != nil {
					t.Fatal(err)
				}
			} else if err := p.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-p.Done():
				if (err != nil) != force {
					t.Fatalf("exit=%v force=%v", err, force)
				}
			case <-ctx.Done():
				t.Fatal("child was not reaped")
			}
			<-p.exited
			if err := p.Stop(ctx); err != nil {
				t.Fatalf("stop after confirmed exit: %v", err)
			}
			if err := p.Kill(); err != nil {
				t.Fatalf("kill after confirmed exit: %v", err)
			}
		})
	}
}

func TestStartedProcessRejectsMissingOwnership(t *testing.T) {
	proof := make(chan ReadyResult)
	wait := func() error { return nil }
	stop := func(context.Context) error { return nil }
	for _, role := range []protocol.Role{protocol.RoleNetworkBroker, "unknown"} {
		if _, err := NewStartedProcess(role, proof, wait, stop, wait); !errors.Is(err, ErrInvalidComposition) {
			t.Fatal(err)
		}
	}
	for _, missing := range []string{"ready", "wait", "stop", "kill"} {
		r, w, s, k := (<-chan ReadyResult)(proof), wait, stop, wait
		switch missing {
		case "ready":
			r = nil
		case "wait":
			w = nil
		case "stop":
			s = nil
		case "kill":
			k = nil
		}
		if _, err := NewStartedProcess(protocol.RoleRuntime, r, w, s, k); !errors.Is(err, ErrInvalidComposition) {
			t.Fatalf("missing %s: %v", missing, err)
		}
	}
}

func TestObservedProcessFailureDoesNotProveExit(t *testing.T) {
	for _, waitErr := range []error{nil, errors.New("native wait failed")} {
		var kills atomic.Int32
		p, err := NewObservedProcess(protocol.RoleRuntime, make(chan ReadyResult), func() (bool, error) {
			return false, waitErr
		}, func(context.Context) error { return nil }, func() error { kills.Add(1); return nil })
		if err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-p.Done():
			if err == nil {
				t.Fatal("observation loss reported no failure")
			}
		case <-time.After(time.Second):
			t.Fatal("observation loss not reported")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if err := p.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unproved exit reported as stopped: %v", err)
		}
		cancel()
		if err := p.Kill(); err != nil || kills.Load() != 1 {
			t.Fatalf("unproved exit suppressed kill: calls=%d err=%v", kills.Load(), err)
		}
	}
}
