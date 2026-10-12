package networkrole

import (
	"io"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

// bridgeStreams transfers ownership of streams that can retain bounded
// bootstrap deadlines. A bridge is idle-capable, so clear those deadlines
// before starting either copy direction.
func bridgeStreams(group *sync.WaitGroup, left, right transport.Stream) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = left.Close(); _ = right.Close() }) }
	for _, stream := range []transport.Stream{left, right} {
		if err := stream.SetReadDeadline(time.Time{}); err != nil {
			closeBoth()
			return
		}
		if err := stream.SetWriteDeadline(time.Time{}); err != nil {
			closeBoth()
			return
		}
	}
	for _, direction := range [][2]transport.Stream{{left, right}, {right, left}} {
		group.Go(func() { _, _ = io.Copy(direction[0], direction[1]); closeBoth() })
	}
}
