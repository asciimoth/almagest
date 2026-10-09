package transport

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// ConfigTransfer is opaque application data, not permission to replace a file.
// The config owner binds Transfer to a subsequent typed revision-checked
// operation. It must consume or discard the document before another transfer.
type ConfigTransfer struct {
	Transfer uint64
	Data     []byte
}

func (p *Peer) Configs() <-chan ConfigTransfer { return p.configs }

// SendConfig uses acknowledged, ordered control chunks. IDs must increase for
// each transfer in one direction. Only one transfer can run per direction;
// callers receive overload instead of entering an unbounded waiting queue.
// A chunk acknowledgement only confirms receipt, never persistence or commit.
func (p *Peer) SendConfig(ctx context.Context, id uint64, data []byte) error {
	if id == 0 || len(data) > protocol.MaxConfigBytes {
		return protocol.ErrLimitExceeded
	}
	select {
	case p.configSend <- struct{}{}:
		defer func() { <-p.configSend }()
	default:
		return protocol.ErrLimitExceeded
	}
	for offset := 0; ; {
		end := min(offset+ConfigChunkPayload, len(data))
		chunk := ConfigChunk{id, uint32(offset), uint32(len(data)), end == len(data), data[offset:end]}
		if err := p.call(ctx, "config", "", chunk, new(struct{})); err != nil {
			return err
		}
		if chunk.Final {
			return nil
		}
		offset = end
	}
}

func (p *Peer) receiveConfig(m message) error {
	if m.Operation != "" || m.Error != nil {
		return protocol.ErrInvalidContract
	}
	var chunk ConfigChunk
	if err := Decode(m.Body, &chunk); err != nil {
		return err
	}
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return io.ErrClosedPipe
	}
	if m.ID != p.received+1 || p.received == ^uint64(0) {
		p.mu.Unlock()
		return protocol.ErrInvalidContract
	}
	if len(p.configs) != 0 {
		p.mu.Unlock()
		return protocol.ErrLimitExceeded
	}
	p.received = m.ID
	if p.assembly.id == 0 {
		p.configTimer = time.AfterFunc(30*time.Second, func() { p.fail(context.DeadlineExceeded) })
	}
	data, err := p.assembly.Add(chunk, time.Now())
	if err != nil {
		p.mu.Unlock()
		return err
	}
	if chunk.Final {
		p.configTimer.Stop()
		p.configs <- ConfigTransfer{chunk.Transfer, data}
	}
	p.mu.Unlock()
	return p.queue(message{Kind: "reply", ID: m.ID, Body: json.RawMessage(`{}`)}, true)
}
