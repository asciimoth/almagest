package httpnodes

import (
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

func TestHTTPServerRestartWaitsForPreviousWorker(t *testing.T) {
	spawner := &httpServerTestSpawner{}
	stopSeen := make(chan struct{})
	release := make(chan struct{})
	old, err := nodekit.NewRestartableWorker(spawner, func(stop <-chan struct{}) {
		<-stop
		close(stopSeen)
		<-release
	}, "old HTTP server")
	if err != nil {
		t.Fatalf("NewRestartableWorker: %v", err)
	}

	logFactory := logging.NewDebugLogFactory(32)
	w := pasta.NewWorkspace(logFactory)
	t.Cleanup(w.Close)
	n := &httpServerNode{
		NodeBase: NodeBase{
			Logger:        logFactory.NodeLogger(1, NodeTypeHTTPServer),
			W:             w,
			ID:            1,
			WorkerSpawner: spawner,
		},
		host:    "127.0.0.1",
		port:    18080,
		network: gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil),
		worker:  old,
	}
	t.Cleanup(func() { _ = n.network.Close() })

	n.restartWorker()
	select {
	case <-stopSeen:
	case <-time.After(time.Second):
		t.Fatal("previous worker did not receive stop")
	}
	n.response = "new response"
	n.restartWorker()

	if got := spawner.Count("http server"); got != 0 {
		t.Fatalf("replacement workers started before previous worker stopped = %d, want 0", got)
	}
	if n.worker != old || !n.workerStopping {
		t.Fatal("previous worker was not retained while stopping")
	}

	close(release)
	select {
	case <-old.Done():
	case <-time.After(time.Second):
		t.Fatal("previous worker did not stop")
	}
	if err := n.OnInbox(pasta.InboxMessage{Payload: httpServerStart{previous: old}}); err != nil {
		t.Fatalf("OnInbox: %v", err)
	}
	if got := spawner.Count("http server"); got != 1 {
		t.Fatalf("replacement HTTP server workers = %d, want 1", got)
	}
	if n.worker == nil || n.worker == old || n.workerStopping {
		t.Fatal("current worker did not start after the previous worker stopped")
	}
	n.stopWorker()
}

type httpServerTestSpawner struct {
	mu      sync.Mutex
	workers int
	names   map[string]int
}

func (s *httpServerTestSpawner) Spawn(worker func(), name string) (uint64, error) {
	s.mu.Lock()
	s.workers++
	if s.names == nil {
		s.names = make(map[string]int)
	}
	s.names[name]++
	id := uint64(s.workers)
	s.mu.Unlock()
	go worker()
	return id, nil
}

func (s *httpServerTestSpawner) SpawnWg(worker func(), waitgroup *sync.WaitGroup, name string) (uint64, error) {
	s.mu.Lock()
	s.workers++
	if s.names == nil {
		s.names = make(map[string]int)
	}
	s.names[name]++
	id := uint64(s.workers)
	s.mu.Unlock()
	waitgroup.Add(1)
	go func() {
		defer waitgroup.Done()
		worker()
	}()
	return id, nil
}

func (s *httpServerTestSpawner) Count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.names[name]
}
