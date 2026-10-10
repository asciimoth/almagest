// Package relay implements the versioned byte and packet channel. It has no
// native network constructor and does not participate in direct mode.
package relay

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type Frame struct {
	Metadata protocol.DataMetadata
	Payload  []byte
}

func (f Frame) Validate() error {
	m := f.Metadata
	if m.Version != protocol.DataVersion {
		return protocol.ErrUnsupportedVersion
	}
	sid, err := protocol.ParseSessionID(m.Session)
	if err != nil || sid.String() != m.Session {
		return protocol.ErrInvalidContract
	}
	rid, err := protocol.ParseResourceID(m.Resource)
	if err != nil || rid.String() != m.Resource || m.SessionEpoch == 0 || m.Generation == 0 || m.GraphEpoch == 0 || m.Sequence == 0 {
		return protocol.ErrInvalidContract
	}
	if len(m.Records) == 0 || len(m.Records) > protocol.MaxDataRecords || len(f.Payload) > protocol.MaxDataPayload {
		return protocol.ErrLimitExceeded
	}
	total := uint64(0)
	var previous uint64
	for _, r := range m.Records {
		if r.Request == 0 || r.Request <= previous {
			return protocol.ErrInvalidContract
		}
		previous = r.Request
		total += uint64(r.Length)
		if r.Packet == nil {
			if r.Length > protocol.MaxStreamRecord {
				return protocol.ErrLimitExceeded
			}
		} else {
			if r.Length > protocol.MaxPacketRecord || r.Packet.OriginalLength < r.Length || r.Packet.Truncated != (r.Packet.OriginalLength > r.Length) {
				return protocol.ErrInvalidContract
			}
			if r.Packet.DNS != nil && r.Packet.DNS.Correlation == 0 {
				return protocol.ErrInvalidContract
			}
			if r.Packet.Source != nil && !r.Packet.Source.IsValid() || r.Packet.Destination != nil && !r.Packet.Destination.IsValid() {
				return protocol.ErrInvalidContract
			}
		}
	}
	if total != uint64(len(f.Payload)) {
		return protocol.ErrInvalidContract
	}
	return nil
}

func Encode(f Frame) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	meta, err := json.Marshal(f.Metadata)
	if err != nil {
		return nil, err
	}
	if len(meta) > protocol.MaxDataMetadata || len(meta)+len(f.Payload)+8 > protocol.MaxDataFrame {
		return nil, protocol.ErrLimitExceeded
	}
	out := make([]byte, 8+len(meta)+len(f.Payload))
	binary.BigEndian.PutUint32(out, uint32(len(out)-4))
	binary.BigEndian.PutUint32(out[4:], uint32(len(meta)))
	copy(out[8:], meta)
	copy(out[8+len(meta):], f.Payload)
	return out, nil
}

func ReadFrame(s transport.Stream) (Frame, error) {
	var f Frame
	if err := s.SetReadDeadline(time.Time{}); err != nil {
		return f, err
	}
	var prefix [8]byte
	if _, err := io.ReadFull(s, prefix[:1]); err != nil {
		return f, err
	}
	if err := s.SetReadDeadline(time.Now().Add(transport.FrameTimeout)); err != nil {
		return f, err
	}
	if _, err := io.ReadFull(s, prefix[1:]); err != nil {
		return f, err
	}
	body, meta := binary.BigEndian.Uint32(prefix[:4]), binary.BigEndian.Uint32(prefix[4:])
	if body < 4 || body > protocol.MaxDataFrame-4 || meta == 0 || meta > protocol.MaxDataMetadata || meta > body-4 || body-4-meta > protocol.MaxDataPayload {
		return f, protocol.ErrLimitExceeded
	}
	data := make([]byte, body-4)
	if _, err := io.ReadFull(s, data); err != nil {
		return f, err
	}
	if err := transport.Decode(data[:meta], &f.Metadata); err != nil {
		return f, err
	}
	f.Payload = data[meta:]
	return f, f.Validate()
}
