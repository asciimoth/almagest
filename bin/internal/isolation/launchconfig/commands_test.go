//go:build linux

package launchconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandsConvertsFixedAllowlist(t *testing.T) {
	config, executable, err := Commands(json.RawMessage(`{"Commands":{
		"one":{"Command":["/opt/notify","--name=${name}"],"Env":{"LANG":"C"},"Timeout":1000000000,"Slots":{"name":{"MaxLength":20,"Required":true}},"AllowNodes":true},
		"two":{"Command":["/opt/notify","fixed"]}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if executable != "/opt/notify" || len(config.Profiles) != 2 || len(config.Commands) != 2 ||
		config.Profiles["one"].Executable != "/command" || config.Profiles["one"].WorkingDirectory != "/state" ||
		!config.Profiles["one"].Slots["name"].AllowLeadingHyphen || !config.Commands["one"].AllowNodes {
		t.Fatalf("converted config = %#v executable=%q", config, executable)
	}
}

func TestCommandsRejectsUnqualifiedProfiles(t *testing.T) {
	for name, input := range map[string]string{
		"unknown":              `{"Unknown":true}`,
		"empty":                `{"Commands":{"x":{"Command":[]}}}`,
		"relative":             `{"Commands":{"x":{"Command":["notify"]}}}`,
		"interpreter":          `{"Commands":{"x":{"Command":["/bin/sh","-c","true"]}}}`,
		"credentials":          `{"Commands":{"x":{"Command":["/opt/a"],"UID":1}}}`,
		"multiple executables": `{"Commands":{"x":{"Command":["/opt/a"]},"y":{"Command":["/opt/b"]}}}`,
		"trailing":             `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Commands(json.RawMessage(input)); err == nil {
				t.Fatal("unsupported command profile accepted")
			} else if name == "interpreter" && !strings.Contains(err.Error(), "--no-isolation") {
				t.Fatalf("missing recovery guidance: %v", err)
			}
		})
	}
}

func FuzzCommands(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"Commands":{"x":{"Command":["/opt/x"],"AllowNodes":true}}}`)
	f.Add(`{"Commands":{"x":{"Command":["/bin/sh"]}}}`)
	f.Fuzz(func(t *testing.T, input string) {
		config, executable, err := Commands(json.RawMessage(input))
		if err != nil {
			return
		}
		if executable == "" && (len(config.Profiles) != 0 || len(config.Commands) != 0) {
			t.Fatal("empty executable returned nonempty configuration")
		}
		for _, profile := range config.Profiles {
			if profile.Executable != "/command" || profile.WorkingDirectory != "/state" {
				t.Fatalf("expanded command authority: %#v", profile)
			}
		}
	})
}
