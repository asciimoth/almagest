package relay

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type memoryStream struct{ *bytes.Reader }

func (*memoryStream) Write(p []byte) (int, error)      { return len(p), nil }
func (*memoryStream) Close() error                     { return nil }
func (*memoryStream) SetReadDeadline(time.Time) error  { return nil }
func (*memoryStream) SetWriteDeadline(time.Time) error { return nil }
func fixture() Frame {
	return Frame{Metadata: protocol.DataMetadata{Version: protocol.DataVersion, Session: "01000000000000000000000000000000", SessionEpoch: 1, Resource: "02000000000000000000000000000000", Generation: 1, GraphEpoch: 1, Sequence: 1, Records: []protocol.DataRecord{{Request: 1, Length: 3}}}, Payload: []byte("abc")}
}
func TestDataFrame(t *testing.T) {
	f := fixture()
	encoded, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&memoryStream{bytes.NewReader(encoded)})
	if err != nil || !bytes.Equal(got.Payload, f.Payload) {
		t.Fatal(got, err)
	}
	for n := 0; n < len(encoded); n++ {
		if _, err = ReadFrame(&memoryStream{bytes.NewReader(encoded[:n])}); err == nil {
			t.Fatalf("accepted truncated frame %d", n)
		}
	}
	for _, mutate := range []func(*Frame){
		func(f *Frame) { f.Metadata.Version++ }, func(f *Frame) { f.Metadata.Sequence = 0 }, func(f *Frame) { f.Metadata.Records[0].Request = 0 }, func(f *Frame) { f.Metadata.Records[0].Length++ }, func(f *Frame) { f.Metadata.Session = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }, func(f *Frame) { f.Metadata.Records[0].Packet = &protocol.PacketMetadata{OriginalLength: 1} },
	} {
		f = fixture()
		mutate(&f)
		if _, err = Encode(f); err == nil {
			t.Fatal("accepted invalid metadata")
		}
	}
	for _, raw := range [][]byte{{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1}, {0, 0, 0, 5, 0, 0, 0, 9}} {
		if _, err = ReadFrame(&memoryStream{bytes.NewReader(raw)}); err == nil {
			t.Fatal("accepted oversized lengths")
		}
	}
	// Strict JSON applies inside data metadata too.
	prefix := []byte(`{"version":1,"version":1}`)
	raw := make([]byte, 8+len(prefix))
	binary.BigEndian.PutUint32(raw, uint32(len(raw)-4))
	binary.BigEndian.PutUint32(raw[4:], uint32(len(prefix)))
	copy(raw[8:], prefix)
	if _, err = ReadFrame(&memoryStream{bytes.NewReader(raw)}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal(err)
	}
}

func TestDataCredits(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	ref := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: session.ID, Type: protocol.ResourceSocket, GraphEpoch: 1, Generation: 1}
	a, b := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sender, err := New(ctx, a, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	receiver, err := New(ctx, b, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receiver.Close() }()
	if err = receiver.Reserve(ref, 1, 3, false); err != nil {
		t.Fatal(err)
	}
	if err = receiver.Reserve(ref, 2, 3, false); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal("duplicate reservation", err)
	}
	// Old-generation traffic must not consume replacement credit or stop it.
	newRef := ref
	newRef.Generation++
	receiver.Release(ref, 1)
	if err = receiver.Reserve(newRef, 1, 3, false); err != nil {
		t.Fatal(err)
	}
	if err = sender.Send(ctx, ref, 1, []byte("old"), nil); err != nil {
		t.Fatal(err)
	}
	ref = newRef
	if err = sender.Send(ctx, ref, 1, []byte("abc"), nil); err != nil {
		t.Fatal(err)
	}
	frame, err := receiver.Receive(ctx, ref, 1)
	if err != nil || string(frame.Payload) != "abc" {
		t.Fatal(err)
	}
	receiver.Release(ref, 1)
	if err = receiver.Reserve(ref, 1, 3, false); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal("request replay", err)
	}
	// A syntactically valid record without credit fails the session.
	_ = sender.Send(ctx, ref, 2, []byte("abc"), nil)
	select {
	case <-receiver.Done():
	case <-ctx.Done():
		t.Fatal("over-credit sender remained live")
	}
}

func TestDataReservationLimit(t *testing.T) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	mux, err := New(context.Background(), a, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mux.Close() }()
	count := 0
	for i := 1; i <= protocol.MaxConcurrentRequests+1; i++ {
		ref := protocol.ResourceRef{ID: protocol.ResourceID{byte(i)}, SessionID: session.ID, Type: protocol.ResourceSocket, GraphEpoch: 1, Generation: 1}
		err = mux.Reserve(ref, 1, 0, true)
		if err != nil {
			if !errors.Is(err, protocol.ErrLimitExceeded) {
				t.Fatal(err)
			}
			break
		}
		count++
	}
	if count != protocol.MaxSessionData/protocol.MaxDataFrame {
		t.Fatalf("reserved %d", count)
	}
}
func FuzzDataFrame(f *testing.F) {
	raw, _ := Encode(fixture())
	f.Add(raw)
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, raw []byte) {
		frame, err := ReadFrame(&memoryStream{bytes.NewReader(raw)})
		if err != nil {
			return
		}
		encoded, err := Encode(frame)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ReadFrame(&memoryStream{bytes.NewReader(encoded)})
		if err != nil || !bytes.Equal(again.Payload, frame.Payload) {
			t.Fatal(err)
		}
	})
}
func BenchmarkDataFrame(b *testing.B) {
	f := fixture()
	f.Payload = make([]byte, 1200)
	f.Metadata.Records[0].Length = 1200
	b.ReportAllocs()
	b.SetBytes(1200)
	for b.Loop() {
		raw, _ := Encode(f)
		_, _ = ReadFrame(&memoryStream{bytes.NewReader(raw)})
	}
}

var _ io.ReadWriteCloser = (*memoryStream)(nil)
