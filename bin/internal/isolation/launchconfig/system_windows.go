//go:build windows

package launchconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/bin/internal/nativebackend"
)

type windowsSystemDocument struct {
	Type    string          `json:"Type"`
	Linux   json.RawMessage `json:"Linux"`
	Windows json.RawMessage `json:"Windows"`
	Debug   json.RawMessage `json:"Debug"`
}

// WindowsSystem selects only the native Windows backend. Debug integration is
// in-process and requires explicit --no-isolation.
func WindowsSystem(raw json.RawMessage) (nativebackend.WindowsFileConfig, error) {
	document := windowsSystemDocument{}
	if len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return nativebackend.WindowsFileConfig{}, fmt.Errorf("protected System: %w", err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return nativebackend.WindowsFileConfig{}, errors.New("protected System has trailing data")
		}
	}
	if document.Type != "" && document.Type != "native" {
		return nativebackend.WindowsFileConfig{}, fmt.Errorf("protected System.Type %q is unavailable in isolation; use explicit --no-isolation", document.Type)
	}
	if len(document.Windows) == 0 || bytes.Equal(bytes.TrimSpace(document.Windows), []byte("null")) {
		document.Windows = json.RawMessage(`{}`)
	}
	return nativebackend.ParseWindowsFileConfig(document.Windows)
}
