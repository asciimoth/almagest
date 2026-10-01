package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeFileAcceptsPowerShellUTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"value":"native"}`)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Value string `json:"value"`
	}
	if err := decodeFile(path, &evidence); err != nil {
		t.Fatalf("decode PowerShell UTF-8 file: %v", err)
	}
	if evidence.Value != "native" {
		t.Fatalf("value = %q, want native", evidence.Value)
	}
}
