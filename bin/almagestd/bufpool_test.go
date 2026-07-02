package main

import "testing"

func TestConfigureGlobalBufPoolNoPoolDisablesPool(t *testing.T) {
	previous := globalBufPool
	t.Cleanup(func() { globalBufPool = previous })

	globalBufPool = leveledBufPool{}
	configureGlobalBufPool(true)

	if globalBufPool != nil {
		t.Fatal("globalBufPool is not nil after configureGlobalBufPool(true)")
	}
}

func TestLeveledBufPoolGetReturnsRequestedLength(t *testing.T) {
	pool := leveledBufPool{}

	for _, length := range []int{0, 1, 255, 256, 257, 1024, 64 * 1024} {
		buf := pool.Get(length)
		if len(buf) != length {
			t.Fatalf("Get(%d) returned len %d", length, len(buf))
		}
		if cap(buf) < length {
			t.Fatalf("Get(%d) returned cap %d", length, cap(buf))
		}
	}
}

func TestLeveledBufPoolReusesSuitableBuffer(t *testing.T) {
	pool := leveledBufPool{}

	buf := pool.Get(257)
	if cap(buf) < 257 {
		t.Fatalf("Get(257) returned cap %d", cap(buf))
	}
	pool.Put(buf)

	reused := pool.Get(257)
	if len(reused) != 257 {
		t.Fatalf("reused buffer len = %d", len(reused))
	}
	if cap(reused) < 257 {
		t.Fatalf("reused buffer cap = %d", cap(reused))
	}
}

func TestLeveledBufPoolSkipsTooSmallPooledBuffer(t *testing.T) {
	pool := leveledBufPool{}

	pool.Put(make([]byte, 0, 257))

	buf := pool.Get(512)
	if len(buf) != 512 {
		t.Fatalf("Get(512) returned len %d", len(buf))
	}
	if cap(buf) < 512 {
		t.Fatalf("Get(512) returned cap %d", cap(buf))
	}
}

func TestLeveledBufPoolSkipsOversizedBuffer(t *testing.T) {
	pool := leveledBufPool{}
	oversized := make([]byte, 0, 1<<20)

	pool.Put(oversized)

	buf := pool.Get(1 << 20)
	if len(buf) != 1<<20 {
		t.Fatalf("Get(1 << 20) returned len %d", len(buf))
	}
	if cap(buf) != 1<<20 {
		t.Fatalf("Get(1 << 20) returned cap %d, want exact allocation", cap(buf))
	}
}
