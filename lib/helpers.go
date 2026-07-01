package almagest

import (
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
)

func enginePool(e *engine) bufpool.Pool {
	if e == nil {
		return nil
	}
	return e.Pool()
}

// Gonnect spawner note: pass nil for gonnect-internal pumps that are stopped
// and waited from Pasta node lifecycle callbacks. Engine-tracked workers are
// fine for Almagest-owned routines because completion bookkeeping does not take
// the workspace lock. Some detached Gonnect wrappers still use nil to preserve
// their own direct-spawn behavior.

func parsePort(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}

	port, err := strconv.Atoi(value)
	if err != nil {
		port, err = gonnect.LookupPortOffline("ip", value)
		if err != nil {
			return fallback
		}
	}

	if port <= 0 || port > 65535 {
		return 0
	}

	return port
}

func sleepOrStopped(stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

type restartableWorker struct {
	stop chan struct{}
	done chan struct{}
}

func newRestartableWorker(spawner EngineWorkerSpawner, run func(stop <-chan struct{}), name string) (*restartableWorker, error) {
	worker := &restartableWorker{stop: make(chan struct{}), done: make(chan struct{})}
	if _, err := spawner.Spawn(func() {
		defer close(worker.done)
		run(worker.stop)
	}, name); err != nil {
		close(worker.done)
		return nil, err
	}
	return worker, nil
}

func (w *restartableWorker) Stop() {
	if w == nil {
		return
	}
	close(w.stop)
}

func appendCappedLog(logs *[]formular.LogLine, max int, level, text string) {
	if logs == nil || max <= 0 {
		return
	}
	*logs = append(*logs, formular.LogLine{Level: level, Text: text})
	if len(*logs) > max {
		*logs = append([]formular.LogLine(nil), (*logs)[len(*logs)-max:]...)
	}
}

func logsBlock(id, label string, order int, logs []formular.LogLine) formular.Block {
	return formular.Block{ID: id, Order: order, Generation: 1, Items: []formular.Item{
		{Type: formular.ItemLogs, ID: "worker", Label: label, Logs: append([]formular.LogLine(nil), logs...)},
	}}
}
