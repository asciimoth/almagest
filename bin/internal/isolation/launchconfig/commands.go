//go:build linux

package launchconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
)

const isolatedCommandPath = "/command"

type commandDocument struct {
	Commands map[string]allowedCommand `json:"Commands,omitempty"`
}

type allowedCommand struct {
	Command    []string          `json:"Command"`
	Env        map[string]string `json:"Env,omitempty"`
	UID        *uint32           `json:"UID,omitempty"`
	GID        *uint32           `json:"GID,omitempty"`
	Sync       bool              `json:"Sync,omitempty"`
	Timeout    time.Duration     `json:"Timeout,omitempty"`
	Slots      map[string]slot   `json:"Slots,omitempty"`
	AllowNodes bool              `json:"AllowNodes,omitempty"`
}

type slot struct {
	MaxLength  int    `json:"MaxLength"`
	Pattern    string `json:"Pattern,omitempty"`
	Required   bool   `json:"Required,omitempty"`
	AllowEmpty bool   `json:"AllowEmpty,omitempty"`
}

// Commands converts the legacy protected allowlist to H's fixed profiles.
// The current Linux H profile exposes one held static executable as /command,
// so all configured IDs must select the same host executable. Configurations
// outside that qualified profile fail with explicit --no-isolation guidance.
func Commands(raw json.RawMessage) (commandhelper.Config, string, error) {
	document := commandDocument{}
	if len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return commandhelper.Config{}, "", fmt.Errorf("protected CommandRunner: %w", err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return commandhelper.Config{}, "", errors.New("protected CommandRunner has trailing data")
		}
	}
	if len(document.Commands) == 0 {
		return commandhelper.Config{}, "", nil
	}
	config := commandhelper.Config{Profiles: make(map[string]commandhelper.Profile, len(document.Commands)), Commands: make(map[string]commandhelper.Command, len(document.Commands))}
	hostExecutable := ""
	for id, input := range document.Commands {
		if len(input.Command) == 0 {
			return commandhelper.Config{}, "", fmt.Errorf("command %q has empty argv", id)
		}
		if err := commandhelper.ValidateExecutablePath(input.Command[0]); err != nil {
			return commandhelper.Config{}, "", fmt.Errorf("command %q: %w", id, err)
		}
		if input.UID != nil || input.GID != nil {
			return commandhelper.Config{}, "", fmt.Errorf("command %q selects per-command credentials unsupported by the isolated helper; use explicit --no-isolation", id)
		}
		if hostExecutable == "" {
			hostExecutable = input.Command[0]
		} else if hostExecutable != input.Command[0] {
			return commandhelper.Config{}, "", errors.New("isolated command helper currently requires one shared executable; use explicit --no-isolation")
		}
		slots := make(map[string]commandhelper.Slot, len(input.Slots))
		for name, value := range input.Slots {
			slots[name] = commandhelper.Slot{MaxLength: value.MaxLength, Pattern: value.Pattern, Required: value.Required,
				AllowEmpty: value.AllowEmpty, AllowLeadingHyphen: true}
		}
		config.Profiles[id] = commandhelper.Profile{Sandbox: "fixed", Executable: isolatedCommandPath,
			Arguments: append([]string(nil), input.Command[1:]...), Environment: cloneStringMap(input.Env), WorkingDirectory: "/state",
			Timeout: input.Timeout, MaxOutputBytes: 64 * 1024, MaxConcurrent: 1, Slots: slots}
		config.Commands[id] = commandhelper.Command{Profile: id, AllowNodes: input.AllowNodes}
	}
	return config, hostExecutable, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
