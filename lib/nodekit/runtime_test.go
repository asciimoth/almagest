package nodekit

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
)

type runtimeTestSpawner struct {
	err   error
	names []string
}

func (s *runtimeTestSpawner) Spawn(worker func(), name string) (uint64, error) {
	s.names = append(s.names, name)
	if s.err != nil {
		return 0, s.err
	}
	go worker()
	return uint64(len(s.names)), nil
}

func (s *runtimeTestSpawner) SpawnWg(worker func(), waitgroup *sync.WaitGroup, name string) (uint64, error) {
	if waitgroup == nil {
		return 0, ErrWorkerWaitGroupNil
	}
	waitgroup.Add(1)
	return s.Spawn(func() {
		defer waitgroup.Done()
		worker()
	}, name)
}

func TestRestartableWorkerLifecycle(t *testing.T) {
	spawner := &runtimeTestSpawner{}
	started := make(chan struct{})
	worker, err := NewRestartableWorker(spawner, func(stop <-chan struct{}) {
		close(started)
		<-stop
	}, "watch")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("restartable worker did not start")
	}
	worker.Stop()
	select {
	case <-worker.Done():
	case <-time.After(time.Second):
		t.Fatal("restartable worker did not stop")
	}
	if len(spawner.names) != 1 || spawner.names[0] != "watch" {
		t.Fatalf("spawned names = %v, want [watch]", spawner.names)
	}
}

func TestRestartableWorkerRejectsUnavailableSpawnerAndSpawnFailure(t *testing.T) {
	worker, err := NewRestartableWorker(nil, func(<-chan struct{}) {}, "missing")
	if worker != nil || !errors.Is(err, ErrWorkerSpawnerUnavailable) {
		t.Fatalf("NewRestartableWorker(nil) = %#v, %v", worker, err)
	}

	wantErr := errors.New("spawn failed")
	worker, err = NewRestartableWorker(&runtimeTestSpawner{err: wantErr}, func(<-chan struct{}) {}, "failed")
	if worker != nil || !errors.Is(err, wantErr) {
		t.Fatalf("NewRestartableWorker(failing) = %#v, %v", worker, err)
	}
}

func TestRestartableWorkerNilMethods(t *testing.T) {
	var worker *RestartableWorker
	worker.Stop()
	select {
	case <-worker.Done():
	default:
		t.Fatal("nil worker Done channel is not closed")
	}
}

func TestBaseWorkerHelpers(t *testing.T) {
	var base *Base
	if err := base.SpawnWorker("missing", func() {}); !errors.Is(err, ErrWorkerSpawnerUnavailable) {
		t.Fatalf("nil Base.SpawnWorker() error = %v", err)
	}
	if worker, err := base.NewRestartableWorker("missing", func(<-chan struct{}) {}); worker != nil || !errors.Is(err, ErrWorkerSpawnerUnavailable) {
		t.Fatalf("nil Base.NewRestartableWorker() = %#v, %v", worker, err)
	}

	wantErr := errors.New("spawn failed")
	logf := logging.NewDebugLogFactory(2)
	base = &Base{WorkerSpawner: &runtimeTestSpawner{err: wantErr}, Logger: logf.EngineLogger()}
	if err := base.SpawnWorker("one-shot", func() {}); !errors.Is(err, wantErr) {
		t.Fatalf("Base.SpawnWorker() error = %v", err)
	}
	if worker, err := base.NewRestartableWorker("restartable", func(<-chan struct{}) {}); worker != nil || !errors.Is(err, wantErr) {
		t.Fatalf("Base.NewRestartableWorker() = %#v, %v", worker, err)
	}
	if entries := logf.Drain(); len(entries) != 2 {
		t.Fatalf("error log count = %d, want 2", len(entries))
	}
}
