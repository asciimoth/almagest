package api

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
)

// Codec reads and writes newline-delimited JSON messages over a byte stream.
// It is suitable for Unix sockets and TCP sockets. WebSocket transports can use
// MarshalMessage and UnmarshalMessage directly for each frame.
type Codec struct {
	dec *json.Decoder
	enc *json.Encoder
	mu  sync.Mutex
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

func (c *Codec) Read() (Message, error) {
	var msg Message
	if err := c.dec.Decode(&msg); err != nil {
		return Message{}, err
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
