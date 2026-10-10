package transport

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const FrameTimeout = 5 * time.Second

// Stream must support deadlines and Close that wakes pending I/O. Each lane
// has a separate stream. No data or diagnostic lock is shared with control.
type Stream interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// ReadFrame allows an idle channel, but bounds a started frame to five seconds.
// A caller must close the stream after any error; framing cannot be recovered.
func ReadFrame(s Stream, dst any) error {
	return readFrame(s, dst, time.Time{})
}

// ReadFrameDeadline bounds both the idle wait and a started frame. It is used
// for startup exchanges where silence is a failed launch, not an idle session.
func ReadFrameDeadline(s Stream, dst any, deadline time.Time) error {
	if deadline.IsZero() {
		return protocol.ErrInvalidContract
	}
	return readFrame(s, dst, deadline)
}

func readFrame(s Stream, dst any, idleDeadline time.Time) error {
	if err := s.SetReadDeadline(idleDeadline); err != nil {
		return err
	}
	var prefix [4]byte
	if _, err := io.ReadFull(s, prefix[:1]); err != nil {
		return err
	}
	if err := s.SetReadDeadline(earlierDeadline(idleDeadline, time.Now().Add(FrameTimeout))); err != nil {
		return err
	}
	if _, err := io.ReadFull(s, prefix[1:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > protocol.MaxControlFrameBytes-4 {
		return protocol.ErrLimitExceeded
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(s, data); err != nil {
		return err
	}
	return Decode(data, dst)
}

func encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, protocol.ErrInvalidContract
	}
	if len(data) > protocol.MaxControlFrameBytes-4 {
		return nil, protocol.ErrLimitExceeded
	}
	// Validate locally generated JSON too, including RawMessage fields.
	if err := Decode(data, new(json.RawMessage)); err != nil {
		return nil, err
	}
	out := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	copy(out[4:], data)
	return out, nil
}

func writeFrame(s Stream, data []byte) error {
	return writeFrameDeadline(s, data, time.Now().Add(FrameTimeout))
}

func writeFrameDeadline(s Stream, data []byte, deadline time.Time) error {
	if err := s.SetWriteDeadline(earlierDeadline(deadline, time.Now().Add(FrameTimeout))); err != nil {
		return err
	}
	for len(data) != 0 {
		n, err := s.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
		data = data[n:]
	}
	return nil
}

// WriteFrame writes one strictly bounded JSON frame. It is exported for the
// trusted role-bootstrap exchange which runs before Peer negotiation.
func WriteFrame(s Stream, value any) error {
	data, err := encode(value)
	if err != nil {
		return err
	}
	return writeFrame(s, data)
}

// WriteFrameDeadline bounds a startup frame by the caller's deadline and the
// normal frame timeout, whichever occurs first.
func WriteFrameDeadline(s Stream, value any, deadline time.Time) error {
	if deadline.IsZero() {
		return protocol.ErrInvalidContract
	}
	data, err := encode(value)
	if err != nil {
		return err
	}
	return writeFrameDeadline(s, data, deadline)
}

func earlierDeadline(left, right time.Time) time.Time {
	if left.IsZero() || right.Before(left) {
		return right
	}
	return left
}

// Lanes are independent endpoints supplied by trusted bootstrap. Close owns
// all three endpoints; no handles are transferred by the wire protocol.
type Lanes struct{ Control, Data, Diagnostic Stream }

func (l Lanes) Close() {
	for _, s := range []Stream{l.Control, l.Data, l.Diagnostic} {
		if s != nil {
			_ = s.Close()
		}
	}
}
