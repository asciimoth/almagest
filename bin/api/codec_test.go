package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLimitedCodecReadsBufferedMessagesIndependently(t *testing.T) {
	first, err := NewRequest("1", SurfaceSnapshots, string(MethodSnapshotsList), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRequest("2", SurfaceSnapshots, string(MethodSnapshotsList), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	encoder := json.NewEncoder(&stream)
	if err := encoder.Encode(first); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(second); err != nil {
		t.Fatal(err)
	}
	limit := int64(stream.Len()/2 + 8)
	codec := NewLimitedReadWriteCodec(&stream, limit)

	gotFirst, err := codec.Read()
	if err != nil || gotFirst.ID != first.ID {
		t.Fatalf("first message = %#v, err=%v", gotFirst, err)
	}
	gotSecond, err := codec.Read()
	if err != nil || gotSecond.ID != second.ID {
		t.Fatalf("second message = %#v, err=%v", gotSecond, err)
	}
}

func TestLimitedCodecRejectsOversizedMessage(t *testing.T) {
	message, err := NewRequest(
		"1", SurfaceSnapshots, string(MethodSnapshotsImport), map[string]string{"archive": strings.Repeat("A", 512)},
	)
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if err := json.NewEncoder(&stream).Encode(message); err != nil {
		t.Fatal(err)
	}
	codec := NewLimitedReadWriteCodec(&stream, 256)

	if _, err := codec.Read(); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("oversized message error = %v, want ErrMessageTooLarge", err)
	}
}
