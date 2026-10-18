//go:build linux

package launchconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/nativebackend"
)

func TestLinuxSystemPreservesNativeSettingsAndSeparatesCallbacks(t *testing.T) {
	got, err := LinuxSystem(json.RawMessage(`{"Type":"native","Linux":{"UserMark":"0x1234","DNS":{"FallbackServers":["::1"]},"Callbacks":{"DNSConfigured":{"Command":"notify"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if uint32(got.Native(nil).UserMark) != 0x1234 || len(got.Native(nil).DNS.FallbackServers) != 1 ||
		string(got.Callbacks) != `{"DNSConfigured":{"Command":"notify"}}` || !reflect.DeepEqual(got.Native(nil).Callbacks, nativebackend.NewLinuxFileConfig().Native(nil).Callbacks) {
		t.Fatalf("linux settings = %#v", got)
	}
}

func TestLinuxSystemRejectsNonNativeAndAmbiguousInput(t *testing.T) {
	for name, input := range map[string]string{
		"debug":    `{"Type":"debug"}`,
		"unknown":  `{"Type":"other"}`,
		"field":    `{"Unknown":true}`,
		"trailing": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LinuxSystem(json.RawMessage(input)); err == nil {
				t.Fatal("unsupported system accepted")
			} else if (name == "debug" || name == "unknown") && !strings.Contains(err.Error(), "--no-isolation") {
				t.Fatalf("missing recovery guidance: %v", err)
			}
		})
	}
}

func FuzzLinuxSystem(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"Type":"native","Linux":{"UserMark":"0x1"}}`)
	f.Add(`{"Type":"debug"}`)
	defaultCallbacks := nativebackend.NewLinuxFileConfig().Native(nil).Callbacks
	f.Fuzz(func(t *testing.T, input string) {
		settings, err := LinuxSystem(json.RawMessage(input))
		if err != nil {
			return
		}
		native := settings.Native(nil)
		if native.Logf != nil || !reflect.DeepEqual(native.Callbacks, defaultCallbacks) {
			t.Fatal("JSON installed executable behavior")
		}
	})
}
