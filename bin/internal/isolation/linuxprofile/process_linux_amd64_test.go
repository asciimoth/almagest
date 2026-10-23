//go:build linux && amd64

package linuxprofile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
)

func TestSingleChildPID(t *testing.T) {
	for _, input := range []string{"0", "-1", "+1", "01", "2147483648", "1 2", "1x", "1\x00"} {
		if _, err := singleChildPID([]byte(input)); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for input, want := range map[string]int32{"": 0, " \n": 0, "1 ": 1, "2147483647\n": 2147483647} {
		if got, err := singleChildPID([]byte(input)); err != nil || got != want {
			t.Errorf("%q: %d %v", input, got, err)
		}
	}
}

func FuzzSingleChildPID(f *testing.F) {
	for _, seed := range []string{"", "123 ", "1 2", "-1", "2147483648", "1\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		pid, err := singleChildPID([]byte(input))
		if err != nil {
			return
		}
		fields := strings.Fields(input)
		if len(fields) == 0 && pid == 0 {
			return
		}
		if len(fields) != 1 || pid <= 0 || strconv.FormatInt(int64(pid), 10) != fields[0] {
			t.Fatalf("invalid accepted identity: %q -> %d", input, pid)
		}
	})
}

func TestProcessRejectsUnstartedAndInvalidLaunch(t *testing.T) {
	for _, process := range []*Process{nil, {}} {
		if err := process.Wait(context.Background()); err == nil {
			t.Fatal("wait accepted unstarted process")
		}
		if err := process.Kill(); err == nil {
			t.Fatal("kill accepted unstarted process")
		}
		if _, err := process.Peer(context.Background()); err == nil {
			t.Fatal("peer accepted unstarted process")
		}
		if _, err := process.Supervise(make(chan supervision.ReadyResult), func(context.Context) error { return nil }); err == nil {
			t.Fatal("supervision accepted unstarted process")
		}
	}
	if _, err := Start(context.Background(), Plan{Role: Runtime, UID: 123, GID: 123, Limits: DefaultLimits()}, Inputs{}, nil, nil); err == nil {
		t.Fatal("started without held inputs")
	}
}

func TestProcessSupervisionRoleAndUnknownExit(t *testing.T) {
	for role, want := range map[Role]protocol.Role{Runtime: protocol.RoleRuntime, Writer: protocol.RoleConfigWriter, Command: protocol.RoleCommandHelper} {
		p := &Process{command: &exec.Cmd{}, plan: Plan{Role: role}, done: make(chan struct{}), childDone: make(chan struct{}), err: errors.New("wait failed")}
		close(p.done)
		close(p.childDone)
		ready := make(chan supervision.ReadyResult)
		stop := func(context.Context) error { return nil }
		if _, err := p.Supervise(nil, stop); err == nil {
			t.Fatal("accepted missing readiness")
		}
		supervised, err := p.Supervise(ready, stop)
		if err != nil {
			t.Fatal(err)
		}
		if supervised.Role() != want || supervised.Ready() != ready {
			t.Fatal("lost launch role or readiness")
		}
		if _, err := p.Supervise(ready, stop); err == nil {
			t.Fatal("accepted duplicate supervision")
		}
		if err := <-supervised.Done(); !errors.Is(err, p.err) {
			t.Fatalf("wait result %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		if err := supervised.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unknown exit marked stopped: %v", err)
		}
		cancel()
	}
}

func TestProcessWaitAndPeerCancellation(t *testing.T) {
	// No child is started: this fixture exercises cancellation and exit checks
	// before procfs lookup. Never signal the fixture's process.
	p := &Process{command: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}, done: make(chan struct{}), err: errors.New("exit result")}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
	if _, err := p.Peer(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("peer: %v", err)
	}
	if err := p.Wait(nil); !errors.Is(err, supervision.ErrInvalidComposition) { //nolint:staticcheck // Verify invalid-context rejection.
		t.Fatalf("nil wait: %v", err)
	}
	if _, err := p.Peer(nil); !errors.Is(err, supervision.ErrInvalidComposition) { //nolint:staticcheck // Verify invalid-context rejection.
		t.Fatalf("nil peer: %v", err)
	}
	close(p.done)
	if err := p.Wait(context.Background()); !errors.Is(err, p.err) {
		t.Fatalf("exit result: %v", err)
	}
	if _, err := p.Peer(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("bound exited launcher: %v", err)
	}
}

func TestProcessWaitsForNamespaceExit(t *testing.T) {
	p := &Process{command: &exec.Cmd{ProcessState: &os.ProcessState{}}, plan: Plan{Role: Runtime}, done: make(chan struct{}), childDone: make(chan struct{}), err: errors.New("wrapper exit")}
	close(p.done)
	supervised, err := p.Supervise(make(chan supervision.ReadyResult), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-supervised.Done():
		t.Fatalf("wrapper exit counted as namespace exit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	p.childErr = errors.New("pidfd observation failed")
	close(p.childDone)
	if err := <-supervised.Done(); !errors.Is(err, p.childErr) {
		t.Fatalf("lost child observation error: %v", err)
	}
	if err := waitChildExit(-1); !errors.Is(err, supervision.ErrInvalidComposition) {
		t.Fatalf("negative descriptor accepted: %v", err)
	}
}

func TestProcessObservationFailureReportsBeforeOtherExit(t *testing.T) {
	for _, childFailure := range []bool{false, true} {
		failure := errors.New("observation failed")
		p := &Process{command: &exec.Cmd{}, plan: Plan{Role: Runtime}, done: make(chan struct{}), childDone: make(chan struct{})}
		if childFailure {
			p.childErr = failure
			close(p.childDone)
		} else {
			p.err = failure
			close(p.done)
		}
		supervised, err := p.Supervise(make(chan supervision.ReadyResult), func(context.Context) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-supervised.Done():
			if !errors.Is(err, failure) {
				t.Fatalf("lost observation error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("observation failure hidden behind other process wait")
		}
	}
}

func TestProcessWaitStoppedRequiresBothExitProofs(t *testing.T) {
	p := &Process{command: &exec.Cmd{ProcessState: &os.ProcessState{}}, done: make(chan struct{}), childDone: make(chan struct{})}
	close(p.done)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := p.WaitStopped(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("confirmed only wrapper: %v", err)
	}
	close(p.childDone)
	if err := p.WaitStopped(context.Background()); err != nil {
		t.Fatalf("both exits: %v", err)
	}
	p.childErr = errors.New("failed observation")
	if err := p.WaitStopped(context.Background()); !errors.Is(err, p.childErr) {
		t.Fatalf("lost observation error: %v", err)
	}
	p.childDone = nil
	if err := p.WaitStopped(context.Background()); !errors.Is(err, supervision.ErrShutdownIncomplete) {
		t.Fatalf("unbound child confirmed stopped: %v", err)
	}
}
