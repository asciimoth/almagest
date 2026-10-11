//go:build windows

package launchconfig

import (
	"encoding/json"
	"strings"
	"testing"

	sysnetwindows "github.com/asciimoth/sysnet-windows"
)

func TestWindowsSystemStrictSelection(t *testing.T) {
	config, err := WindowsSystem(json.RawMessage(`{"Type":"native","Windows":{"AdapterNamePrefix":"private","Recovery":"owned"}}`))
	if err != nil {
		t.Fatal(err)
	}
	native := config.Native(nil)
	if native.AdapterNamePrefix != "private" || native.Recovery != sysnetwindows.RecoveryOwned {
		t.Fatalf("config = %#v", native)
	}
	for _, input := range []string{
		`{"Type":"debug"}`,
		`{"Unknown":true}`,
		`{} {}`,
		`{"Windows":{"OperationTimeout":"invalid"}}`,
	} {
		if _, err := WindowsSystem(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		} else if strings.Contains(input, `"debug"`) && !strings.Contains(err.Error(), "--no-isolation") {
			t.Fatalf("missing direct-mode guidance: %v", err)
		}
	}
}

func FuzzWindowsSystem(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"Type":"native","Windows":{"OperationTimeout":"1s"}}`)
	f.Add(`{"Type":"debug"}`)
	f.Fuzz(func(t *testing.T, input string) {
		config, err := WindowsSystem(json.RawMessage(input))
		if err == nil && config.Native(nil).Logger == nil {
			t.Fatal("accepted config lost trusted logger")
		}
	})
}
