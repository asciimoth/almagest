package socksnodes

import (
	"bytes"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/socksgo/protocol"
)

// TestSocksgoGOSTUDPConcurrentWritesKeepFramesAtomic guards the framing fix in
// socksgo v0.5.1. One GOST UDP frame can use separate stream writes for its
// header and payload. Concurrent packet writes must serialize both operations.
func TestSocksgoGOSTUDPConcurrentWritesKeepFramesAtomic(t *testing.T) {
	conn := &socksgoInterleavingConn{headersWritten: make(chan struct{})}
	client := protocol.NewSocks5UDPClientTUN(
		conn,
		protocol.AddrFromHostPort("127.0.0.1:1", "udp4"),
		nil,
		nil,
	)
	target := netip.MustParseAddrPort("127.0.0.1:51820")
	payloads := [][]byte{[]byte("first"), []byte("second")}

	var writers sync.WaitGroup
	for _, payload := range payloads {
		writers.Add(1)
		go func() {
			defer writers.Done()
			if _, err := client.WriteToUDPAddrPort(payload, target); err != nil {
				t.Errorf("write frame: %v", err)
			}
		}()
	}
	writers.Wait()

	got := make([][]byte, 0, len(payloads))
	for range payloads {
		buf := make([]byte, 64)
		n, _, err := protocol.ReadSocks5TunUDPPacket(nil, conn, buf, false)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		got = append(got, append([]byte(nil), buf[:n]...))
	}
	framesMatch := bytes.Equal(got[0], payloads[0]) &&
		bytes.Equal(got[1], payloads[1]) ||
		bytes.Equal(got[0], payloads[1]) &&
			bytes.Equal(got[1], payloads[0])
	if !framesMatch {
		t.Fatalf("concurrent frames were corrupted: got %q", got)
	}
}

// socksgoInterleavingConn forces the first two writes to complete before either
// caller continues. In socksgo v0.5.0, those writes are two frame headers. In
// v0.5.1, the frame lock makes them one header and its matching payload.
type socksgoInterleavingConn struct {
	mu             sync.Mutex
	stream         bytes.Buffer
	writeCalls     int
	headersWritten chan struct{}
}

func (c *socksgoInterleavingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	n, err := c.stream.Write(p)
	if c.writeCalls < 2 {
		c.writeCalls++
		if c.writeCalls == 2 {
			close(c.headersWritten)
		}
		c.mu.Unlock()
		select {
		case <-c.headersWritten:
		case <-time.After(20 * time.Millisecond):
		}
		return n, err
	}
	c.mu.Unlock()
	return n, err
}

func (c *socksgoInterleavingConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stream.Read(p)
}

func (*socksgoInterleavingConn) Close() error                     { return nil }
func (*socksgoInterleavingConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*socksgoInterleavingConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*socksgoInterleavingConn) SetDeadline(time.Time) error      { return nil }
func (*socksgoInterleavingConn) SetReadDeadline(time.Time) error  { return nil }
func (*socksgoInterleavingConn) SetWriteDeadline(time.Time) error { return nil }
