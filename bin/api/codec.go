package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

var ErrMessageTooLarge = errors.New("API message is too large")

// Codec reads and writes newline-delimited JSON messages over a byte stream.
// It is suitable for Unix sockets and TCP sockets. WebSocket transports can use
// MarshalMessage and UnmarshalMessage directly for each frame.
type Codec struct {
	dec             *json.Decoder
	enc             *json.Encoder
	limitedReader   *limitedMessageReader
	readLimit       int64
	lastInputOffset int64
	mu              sync.Mutex
}

// limitedMessageReader restricts each underlying read as well as the total.
// The small read cap prevents json.Decoder from buffering a large part of the
// next message against the current message's allowance.
type limitedMessageReader struct {
	reader    io.Reader
	remaining int64
}

func (r *limitedMessageReader) Read(data []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	const maxRead = 32 << 10
	if int64(len(data)) > r.remaining {
		data = data[:r.remaining]
	}
	if len(data) > maxRead {
		data = data[:maxRead]
	}
	n, err := r.reader.Read(data)
	r.remaining -= int64(n)
	return n, err
}

func NewCodec(r io.Reader, w io.Writer) *Codec {
	return &Codec{
		dec: json.NewDecoder(bufio.NewReader(r)),
		enc: json.NewEncoder(w),
	}
}

func NewReadWriteCodec(rw io.ReadWriter) *Codec {
	return NewCodec(rw, rw)
}

// NewLimitedReadWriteCodec creates a stream codec with a per-message read
// limit. A limit protects the daemon before JSON decoding allocates a complete
// request payload. The connection must be closed after ErrMessageTooLarge.
func NewLimitedReadWriteCodec(rw io.ReadWriter, readLimit int64) *Codec {
	if readLimit <= 0 {
		return NewReadWriteCodec(rw)
	}
	limited := &limitedMessageReader{reader: rw}
	return &Codec{
		dec:           json.NewDecoder(limited),
		enc:           json.NewEncoder(rw),
		limitedReader: limited,
		readLimit:     readLimit,
	}
}

func (c *Codec) Read() (Message, error) {
	if c.limitedReader != nil {
		// Read one byte beyond the limit so an oversized JSON value cannot look
		// like a clean end of stream at the boundary.
		c.limitedReader.remaining = c.readLimit + 1
	}
	var msg Message
	if err := c.dec.Decode(&msg); err != nil {
		if c.limitedReader != nil && c.limitedReader.remaining == 0 {
			return Message{}, fmt.Errorf("%w: limit is %d bytes", ErrMessageTooLarge, c.readLimit)
		}
		return Message{}, err
	}
	if c.limitedReader != nil {
		inputOffset := c.dec.InputOffset()
		if inputOffset-c.lastInputOffset > c.readLimit {
			return Message{}, fmt.Errorf("%w: limit is %d bytes", ErrMessageTooLarge, c.readLimit)
		}
		c.lastInputOffset = inputOffset
	}
	return msg, msg.Validate()
}

func (c *Codec) Write(msg Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(msg)
}
