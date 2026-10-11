//go:build linux

package launchconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/bin/internal/nativebackend"
)

type linuxSystemDocument struct {
	Type    string          `json:"Type"`
	Linux   json.RawMessage `json:"Linux"`
	Windows json.RawMessage `json:"Windows"`
	Debug   json.RawMessage `json:"Debug"`
}

// LinuxSystem selects only the native Linux backend. Debug integration runs in
// process and is therefore available only with explicit --no-isolation.
func LinuxSystem(raw json.RawMessage) (nativebackend.LinuxFileConfig, error) {
	document := linuxSystemDocument{}
	if len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return nativebackend.LinuxFileConfig{}, fmt.Errorf("protected System: %w", err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return nativebackend.LinuxFileConfig{}, errors.New("protected System has trailing data")
		}
	}
	if document.Type != "" && document.Type != "native" {
		return nativebackend.LinuxFileConfig{}, fmt.Errorf("protected System.Type %q is unavailable in isolation; use explicit --no-isolation", document.Type)
	}
	if len(document.Linux) == 0 || bytes.Equal(bytes.TrimSpace(document.Linux), []byte("null")) {
		document.Linux = json.RawMessage(`{}`)
	}
	return nativebackend.ParseLinuxFileConfig(document.Linux)
}
