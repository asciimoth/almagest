package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestReadDaemonRandomSeeds(t *testing.T) {
	raw := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	}
	got, err := readDaemonRandomSeeds(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("readDaemonRandomSeeds error = %v, want nil", err)
	}
	if want := int64(0x0706050403020100); got.system != want {
		t.Fatalf("system seed = %#x, want %#x", got.system, want)
	}
	if want := int64(0x0f0e0d0c0b0a0908); got.workspace != want {
		t.Fatalf("workspace seed = %#x, want %#x", got.workspace, want)
	}
}

func TestReadDaemonRandomSeedsRequiresAllBytes(t *testing.T) {
	_, err := readDaemonRandomSeeds(bytes.NewReader(make([]byte, 15)))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readDaemonRandomSeeds error = %v, want %v", err, io.ErrUnexpectedEOF)
	}
}
