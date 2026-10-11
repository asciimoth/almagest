//go:build linux

package callbackbridge

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
)

type recordingSubmitter struct {
	id, event string
	slots     map[string]string
}

func (r *recordingSubmitter) Submit(id, event string, slots map[string]string) bool {
	r.id, r.event, r.slots = id, event, slots
	return true
}

func callbackConfig() commandhelper.Config {
	return commandhelper.Config{Profiles: map[string]commandhelper.Profile{"notify": {
		Slots: map[string]commandhelper.Slot{"server": {MaxLength: 64, Required: true}, "event": {MaxLength: 64, Required: true}},
	}}, Commands: map[string]commandhelper.Command{"notify": {Profile: "notify"}}}
}

func TestCompileAndRenderProtectedLinuxCallback(t *testing.T) {
	config := callbackConfig()
	bindings, err := Compile(json.RawMessage(`{"DNSConfigured":{"Command":"notify","Slots":{"server":"{{.Server}}","event":"{{.Callback}}"}}}`), &config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bindings.IDs(), []string{DNSConfigured}) || len(config.Callbacks) != 1 {
		t.Fatalf("bindings=%v callbacks=%#v", bindings.IDs(), config.Callbacks)
	}
	recorder := &recordingSubmitter{}
	callbacks := bindings.Native(recorder, t.Logf)
	callbacks.DNSConfigured(netip.MustParseAddr("192.0.2.53"))
	if recorder.id != DNSConfigured || recorder.event != DNSConfigured || recorder.slots["server"] != "192.0.2.53" || recorder.slots["event"] != DNSConfigured {
		t.Fatalf("submitted callback = %#v", recorder)
	}
}

func TestCompileRejectsCallbackAuthorityErrors(t *testing.T) {
	for name, input := range map[string]string{
		"unknown field":   `{"Other":{"Command":"notify"}}`,
		"unknown command": `{"DNSConfigured":{"Command":"other"}}`,
		"unknown slot":    `{"DNSConfigured":{"Command":"notify","Slots":{"other":"x"}}}`,
		"bad template":    `{"DNSConfigured":{"Command":"notify","Slots":{"server":"{{"}}}`,
		"slots only":      `{"DNSConfigured":{"Slots":{"server":"x"}}}`,
		"trailing":        `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			config := callbackConfig()
			if _, err := Compile(json.RawMessage(input), &config); err == nil {
				t.Fatal("invalid callback accepted")
			}
		})
	}
}

func FuzzCompile(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"DNSConfigured":{"Command":"notify","Slots":{"server":"{{.Server}}"}}}`)
	f.Add(`{"Unknown":true}`)
	f.Fuzz(func(t *testing.T, input string) {
		config := callbackConfig()
		bindings, err := Compile(json.RawMessage(input), &config)
		if err != nil {
			return
		}
		if len(bindings.IDs()) != len(config.Callbacks) || len(bindings.IDs()) > 8 {
			t.Fatalf("inconsistent bindings: %v %#v", bindings.IDs(), config.Callbacks)
		}
	})
}
