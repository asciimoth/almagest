package diagnostics

import (
	"sync"
	"sync/atomic"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type priorityQueue struct {
	mu          sync.Mutex
	ready       *sync.Cond
	items       []Payload
	normalCap   int
	priorityCap int
	lowCount    int
	closed      bool
	dropped     map[protocol.Severity]*atomic.Uint64
	pending     atomic.Uint64
}

func newPriorityQueue(normal, priority int) *priorityQueue {
	q := &priorityQueue{normalCap: normal, priorityCap: priority, dropped: make(map[protocol.Severity]*atomic.Uint64)}
	q.ready = sync.NewCond(&q.mu)
	for _, severity := range []protocol.Severity{protocol.SeverityDebug, protocol.SeverityInfo, protocol.SeverityWarn, protocol.SeverityError, protocol.SeverityFatal} {
		q.dropped[severity] = &atomic.Uint64{}
	}
	return q
}

func (q *priorityQueue) push(p Payload) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		q.drop(p.Severity, 1)
		return false
	}
	high := isPriority(p)
	limit := q.normalCap + q.priorityCap
	if !high && (q.lowCount >= q.normalCap || len(q.items) >= limit) {
		q.drop(p.Severity, 1)
		return false
	}
	if high && len(q.items) >= limit {
		victim := -1
		for i := range q.items {
			if !isPriority(q.items[i]) {
				victim = i
				break
			}
		}
		if victim < 0 {
			victim = 0
		}
		old := q.items[victim]
		q.items = append(q.items[:victim], q.items[victim+1:]...)
		if !isPriority(old) {
			q.lowCount--
		}
		q.drop(old.Severity, 1)
	}
	q.items = append(q.items, p)
	if !high {
		q.lowCount++
	}
	q.ready.Signal()
	return true
}

func (q *priorityQueue) pop() (Payload, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.ready.Wait()
	}
	if len(q.items) == 0 {
		return Payload{}, false
	}
	best := 0
	for i := 1; i < len(q.items); i++ {
		if severityRank(q.items[i].Severity) > severityRank(q.items[best].Severity) {
			best = i
		}
	}
	p := q.items[best]
	q.items = append(q.items[:best], q.items[best+1:]...)
	if !isPriority(p) {
		q.lowCount--
	}
	return p, true
}

func isPriority(p Payload) bool {
	return severityRank(p.Severity) >= severityRank(protocol.SeverityWarn) || p.Operation != "" || p.Decision != "" || p.Outcome != ""
}

func (q *priorityQueue) drop(severity protocol.Severity, count uint64) {
	q.dropped[severity].Add(count)
	q.pending.Add(count)
}

func (q *priorityQueue) addPendingDrops(count uint64) { q.pending.Add(count) }
func (q *priorityQueue) takePendingDrops() uint64     { return q.pending.Swap(0) }

func (q *priorityQueue) markWriteFailure(severity protocol.Severity, carried uint64) {
	q.dropped[severity].Add(1)
	q.addPendingDrops(carried + 1)
}

func (q *priorityQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.ready.Broadcast()
	q.mu.Unlock()
}

func (q *priorityQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *priorityQueue) dropSnapshot() map[protocol.Severity]uint64 {
	out := make(map[protocol.Severity]uint64, len(q.dropped))
	for severity, count := range q.dropped {
		out[severity] = count.Load()
	}
	return out
}
