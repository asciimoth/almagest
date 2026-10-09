package transport

import (
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// ConfigChunkPayload leaves room for base64 and envelope metadata in 64 KiB.
const ConfigChunkPayload = 32 * 1024

type ConfigChunk struct {
	Transfer uint64 `json:"transfer"`
	Offset   uint32 `json:"offset"`
	Total    uint32 `json:"total"`
	Final    bool   `json:"final"`
	Data     []byte `json:"data"`
}

// ConfigAssembler retains at most one 8 MiB document per session. The owner
// serializes Add/Reset and calls Reset on cancellation or session loss. A
// started transfer has a fixed 30-second lifetime, not a sliding timeout.
type ConfigAssembler struct {
	id, last uint64
	total    uint32
	until    time.Time
	data     []byte
}

func (a *ConfigAssembler) Reset() { a.id = 0; a.data = nil; a.total = 0; a.until = time.Time{} }

func (a *ConfigAssembler) Add(c ConfigChunk, now time.Time) ([]byte, error) {
	if a.id != 0 && !now.Before(a.until) {
		a.Reset()
		return nil, protocol.ErrInvalidContract
	}
	if c.Transfer == 0 || c.Total > protocol.MaxConfigBytes || len(c.Data) > ConfigChunkPayload || c.Offset > c.Total || uint64(c.Offset)+uint64(len(c.Data)) > uint64(c.Total) {
		a.Reset()
		return nil, protocol.ErrLimitExceeded
	}
	if a.id == 0 {
		if c.Offset != 0 || c.Transfer <= a.last {
			return nil, protocol.ErrInvalidContract
		}
		a.id, a.last, a.total, a.until = c.Transfer, c.Transfer, c.Total, now.Add(30*time.Second)
		a.data = make([]byte, 0, int(c.Total))
	}
	if c.Transfer != a.id || c.Total != a.total || int(c.Offset) != len(a.data) || c.Final != (uint64(c.Offset)+uint64(len(c.Data)) == uint64(c.Total)) || (!c.Final && len(c.Data) == 0) {
		a.Reset()
		return nil, protocol.ErrInvalidContract
	}
	a.data = append(a.data, c.Data...)
	if !c.Final {
		return nil, nil
	}
	out := a.data
	a.Reset()
	return out, nil
}
