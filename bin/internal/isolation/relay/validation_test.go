package relay

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestDataFrameValidationMatrix(t *testing.T) {
	validPacket := fixture()
	source := netip.MustParseAddrPort("192.0.2.1:1234")
	destination := netip.MustParseAddrPort("198.51.100.2:4321")
	validPacket.Metadata.Records[0].Packet = &protocol.PacketMetadata{
		Source:         &source,
		Destination:    &destination,
		OriginalLength: 3,
	}
	if err := validPacket.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		change func(*Frame)
		want   error
	}{
		{"version", func(f *Frame) { f.Metadata.Version++ }, protocol.ErrUnsupportedVersion},
		{"uppercase_session", func(f *Frame) { f.Metadata.Session = "0A000000000000000000000000000000" }, protocol.ErrInvalidContract},
		{"invalid_resource", func(f *Frame) { f.Metadata.Resource = "not-an-id" }, protocol.ErrInvalidContract},
		{"session_epoch", func(f *Frame) { f.Metadata.SessionEpoch = 0 }, protocol.ErrInvalidContract},
		{"generation", func(f *Frame) { f.Metadata.Generation = 0 }, protocol.ErrInvalidContract},
		{"graph_epoch", func(f *Frame) { f.Metadata.GraphEpoch = 0 }, protocol.ErrInvalidContract},
		{"sequence", func(f *Frame) { f.Metadata.Sequence = 0 }, protocol.ErrInvalidContract},
		{"no_records", func(f *Frame) { f.Metadata.Records = nil; f.Payload = nil }, protocol.ErrLimitExceeded},
		{"too_many_records", func(f *Frame) {
			f.Metadata.Records = make([]protocol.DataRecord, protocol.MaxDataRecords+1)
			f.Payload = nil
		}, protocol.ErrLimitExceeded},
		{"payload_limit", func(f *Frame) { f.Payload = make([]byte, protocol.MaxDataPayload+1) }, protocol.ErrLimitExceeded},
		{"zero_request", func(f *Frame) { f.Metadata.Records[0].Request = 0 }, protocol.ErrInvalidContract},
		{"unordered_requests", func(f *Frame) { f.Metadata.Records = append(f.Metadata.Records, protocol.DataRecord{Request: 1}) }, protocol.ErrInvalidContract},
		{"stream_record_limit", func(f *Frame) {
			f.Metadata.Records[0].Length = protocol.MaxStreamRecord + 1
			f.Payload = make([]byte, protocol.MaxStreamRecord+1)
		}, protocol.ErrLimitExceeded},
		{"payload_mismatch", func(f *Frame) { f.Payload = f.Payload[:2] }, protocol.ErrInvalidContract},
		{"packet_short_original", func(f *Frame) { f.Metadata.Records[0].Packet = &protocol.PacketMetadata{OriginalLength: 2} }, protocol.ErrInvalidContract},
		{"packet_missing_truncated", func(f *Frame) { f.Metadata.Records[0].Packet = &protocol.PacketMetadata{OriginalLength: 4} }, protocol.ErrInvalidContract},
		{"packet_false_truncated", func(f *Frame) {
			f.Metadata.Records[0].Packet = &protocol.PacketMetadata{OriginalLength: 3, Truncated: true}
		}, protocol.ErrInvalidContract},
		{"invalid_source", func(f *Frame) {
			a := netip.AddrPort{}
			f.Metadata.Records[0].Packet = &protocol.PacketMetadata{Source: &a, OriginalLength: 3}
		}, protocol.ErrInvalidContract},
		{"invalid_destination", func(f *Frame) {
			a := netip.AddrPort{}
			f.Metadata.Records[0].Packet = &protocol.PacketMetadata{Destination: &a, OriginalLength: 3}
		}, protocol.ErrInvalidContract},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frame := fixture()
			tc.change(&frame)
			if err := frame.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDataMuxValidationAndAccounting(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	a, b := net.Pipe()
	mux, err := New(context.Background(), a, session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mux.Close(); _ = b.Close() })
	ref := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: session.ID, Type: protocol.ResourceSocket, Generation: 1, GraphEpoch: 1}

	for _, tc := range []struct {
		name    string
		ref     protocol.ResourceRef
		request uint64
		size    uint32
		packet  bool
		want    error
	}{
		{"zero_request", ref, 0, 1, false, protocol.ErrInvalidContract},
		{"stream_limit", ref, 1, protocol.MaxStreamRecord + 1, false, protocol.ErrLimitExceeded},
		{"packet_limit", ref, 1, protocol.MaxPacketRecord + 1, true, protocol.ErrLimitExceeded},
		{"foreign", func() protocol.ResourceRef { r := ref; r.SessionID[0]++; return r }(), 1, 1, false, protocol.ErrForeignResource},
		{"zero_generation", func() protocol.ResourceRef { r := ref; r.Generation = 0; return r }(), 1, 1, false, protocol.ErrInvalidContract},
		{"zero_epoch", func() protocol.ResourceRef { r := ref; r.GraphEpoch = 0; return r }(), 1, 1, false, protocol.ErrInvalidContract},
		{"wrong_type", func() protocol.ResourceRef { r := ref; r.Type = protocol.ResourceMatcher; return r }(), 1, 1, false, protocol.ErrResourceType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := mux.Reserve(tc.ref, tc.request, tc.size, tc.packet); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	if err := mux.Reserve(ref, 1, 3, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mux.Receive(ctx, ref, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("receive cancellation = %v", err)
	}
	mux.Release(ref, 1)
	if _, err := mux.Receive(context.Background(), ref, 1); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("released receive = %v", err)
	}

	if release, err := mux.HoldOutbound(-1); release != nil || !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("negative hold returned release=%t, error=%v", release != nil, err)
	}
	if release, err := mux.HoldOutbound(256*1024 + 1); release != nil || !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("large hold returned release=%t, error=%v", release != nil, err)
	}
	releases := make([]func(), 0, protocol.MaxSessionData/(256*1024))
	for len(releases) < cap(releases) {
		release, err := mux.HoldOutbound(256 * 1024)
		if err != nil {
			t.Fatalf("hold %d: %v", len(releases), err)
		}
		releases = append(releases, release)
	}
	if _, err := mux.HoldOutbound(1); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("session limit = %v", err)
	}
	for _, release := range releases {
		release()
		release()
	}
	if mux.held != 0 {
		t.Fatalf("released holds retained %d bytes", mux.held)
	}
}

func TestDataMuxRejectsFutureGeneration(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	ref := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: session.ID, Type: protocol.ResourceSocket, Generation: 1, GraphEpoch: 1}
	a, b := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sender, err := New(ctx, a, session)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := New(ctx, b, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	if err = receiver.Reserve(ref, 1, 3, false); err != nil {
		t.Fatal(err)
	}
	future := ref
	future.Generation++
	// The receiver closes the stream as soon as it sees the future generation.
	// The sender can therefore observe the protocol close before Send returns.
	_ = sender.Send(ctx, future, 1, []byte("new"), nil)
	select {
	case <-receiver.Done():
		if !errors.Is(receiver.Err(), protocol.ErrInvalidContract) {
			t.Fatalf("future generation error = %v", receiver.Err())
		}
	case <-ctx.Done():
		t.Fatal("future generation did not stop receiver")
	}
}

func FuzzDataFrameMetadata(f *testing.F) {
	f.Add(uint32(1), uint64(1), uint64(1), uint32(3), uint32(3), false, false, []byte("abc"))
	f.Add(uint32(1), uint64(1), uint64(1), uint32(3), uint32(4), true, true, []byte("abc"))
	f.Fuzz(func(t *testing.T, version uint32, sequence, request uint64, declared, original uint32, packet, truncated bool, raw []byte) {
		if len(raw) > 256 {
			raw = raw[:256]
		}
		frame := fixture()
		frame.Metadata.Version = version
		frame.Metadata.Sequence = sequence
		frame.Metadata.Records[0].Request = request
		frame.Metadata.Records[0].Length = declared
		frame.Payload = append([]byte(nil), raw...)
		if packet {
			frame.Metadata.Records[0].Packet = &protocol.PacketMetadata{OriginalLength: original, Truncated: truncated}
		}
		encoded, err := Encode(frame)
		if err != nil {
			return
		}
		decoded, err := ReadFrame(&memoryStream{bytes.NewReader(encoded)})
		if err != nil || !bytes.Equal(decoded.Payload, frame.Payload) {
			t.Fatalf("valid frame did not round trip: %v", err)
		}
	})
}
