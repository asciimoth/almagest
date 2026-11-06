package socksnodes

import (
	"sync"
	"time"
)

const (
	shadowsocksReplayWindow = 10 * time.Minute
	shadowsocksReplayLimit  = 100000
)

type shadowsocksReplayEntry struct {
	salt string
	seen time.Time
}
type shadowsocksReplayGuard struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	entries []shadowsocksReplayEntry
	head    int
}

func newShadowsocksReplayGuard() *shadowsocksReplayGuard {
	return &shadowsocksReplayGuard{seen: make(map[string]time.Time)}
}

// accept records a valid AEAD salt. The time and size limits bound memory.
func (g *shadowsocksReplayGuard) accept(salt []byte) bool {
	if g == nil {
		return true
	}
	now := time.Now()
	key := string(salt)
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.head < len(g.entries) {
		oldest := g.entries[g.head]
		if now.Sub(oldest.seen) < shadowsocksReplayWindow {
			break
		}
		delete(g.seen, oldest.salt)
		g.head++
	}
	if _, exists := g.seen[key]; exists {
		return false
	}
	if len(g.seen) >= shadowsocksReplayLimit {
		delete(g.seen, g.entries[g.head].salt)
		g.head++
	}
	g.seen[key] = now
	g.entries = append(g.entries, shadowsocksReplayEntry{salt: key, seen: now})
	if g.head > 1024 && g.head > len(g.entries)/2 {
		g.entries = append([]shadowsocksReplayEntry(nil), g.entries[g.head:]...)
		g.head = 0
	}
	return true
}
