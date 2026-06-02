package main

import (
	"math/bits"
	"sync"

	"github.com/asciimoth/bufpool"
)

var globalBufPool bufpool.Pool = leveledBufPool{}

type leveledBufPool struct{}

var bufPools [10]sync.Pool

func (leveledBufPool) Get(length int) []byte {
	id, capacityNeeded := bufPoolIDAndCapacity(length)
	for range 2 {
		if id < 0 || id >= len(bufPools) {
			break
		}
		if v := bufPools[id].Get(); v != nil {
			buf := *v.(*[]byte)
			if cap(buf) >= length {
				return buf[:length]
			}
		}
		id++
	}
	if capacityNeeded < length {
		capacityNeeded = length
	}
	return make([]byte, length, capacityNeeded)
}

func (leveledBufPool) Put(buf []byte) {
	capacity := cap(buf)
	if capacity == 0 {
		return
	}
	id, poolCapacity := bufPoolIDAndCapacity(capacity)
	if capacity <= poolCapacity {
		buf = buf[:0:capacity]
		bufPools[id].Put(&buf)
	}
}

func bufPoolIDAndCapacity(size int) (int, int) {
	size--
	if size < 0 {
		size = 0
	}
	size >>= 8
	id := bits.Len(uint(size))
	if id >= len(bufPools) {
		id = len(bufPools) - 1
	}
	return id, 1 << (id + 8)
}
