//go:build linux

package nativebackend

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"

	sysnetlinux "github.com/asciimoth/sysnet-linux"
)

func TestLinuxBackendConfigDefaultsAndOverrides(t *testing.T) {
	defaults := NewLinuxFileConfig().Native(nil)
	parsed, err := ParseLinuxFileConfig([]byte(`{}`))
	if err != nil || !reflect.DeepEqual(parsed.Native(nil), defaults) {
		t.Fatalf("defaults: %v", err)
	}
	if defaults.UserMark != LinuxDefaultUserMark || !defaults.KillswitchAllowExclude || defaults.Pmark.PinPath != "/sys/fs/bpf/almagest" {
		t.Fatal("lost daemon defaults")
	}
	parsed, err = ParseLinuxFileConfig([]byte(`{"AppBypassMark":"0xeb9f0001","AppBypassMask":"FFFFFFFF","UserMark":42,"UserMarkMask":null,"KillswitchAllowExclude":false,"DNS":{"Mode":"direct","FallbackServers":["9.9.9.9","[::1]:5353"]},"Pmark":{"PinPath":"/run/private-bpf","TombTTL":100,"Priority":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := parsed.Native(nil)
	if got.AppBypassMark != 0xeb9f0001 || got.AppBypassMask != 0xffffffff || got.UserMark != 42 || got.UserMarkMask != defaults.UserMarkMask || got.KillswitchAllowExclude {
		t.Fatal("lost scalar overrides")
	}
	if got.Pmark.PinPath != "/run/private-bpf" || got.Pmark.TombTTL != 100 || got.Pmark.Priority != 3 {
		t.Fatal("lost process-mark options")
	}
	if got.DNS.Mode != "direct" || !reflect.DeepEqual(got.DNS.FallbackServers, []netip.AddrPort{netip.MustParseAddrPort("9.9.9.9:53"), netip.MustParseAddrPort("[::1]:5353")}) {
		t.Fatal("lost DNS options")
	}
}

func TestLinuxBackendConfigSeparatesExecutableCallbacks(t *testing.T) {
	parsed, err := ParseLinuxFileConfig([]byte(`{"Logf":"not executable","Callbacks":{"DNSConfigured":{"Command":"notify"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed.Callbacks) != `{"DNSConfigured":{"Command":"notify"}}` {
		t.Fatal("lost fixed callback input")
	}
	called := false
	got := parsed.Native(func(string, ...any) { called = true })
	if !reflect.DeepEqual(got.Callbacks, sysnetlinux.Callbacks{}) {
		t.Fatal("JSON installed executable callbacks")
	}
	got.Logf("test")
	if !called {
		t.Fatal("trusted logger not installed")
	}
}

func TestLinuxBackendConfigRejectsMalformedScalars(t *testing.T) {
	for _, input := range []string{`[]`, `{"UserMark":-1}`, `{"UserMark":4294967296}`, `{"UserMark":"100000000"}`, `{"UserMark":"0x"}`, `{"UserMark":true}`, `{"DNS":{"FallbackServers":[""]}}`, `{"DNS":{"FallbackServers":["host.example:53"]}}`, `{"DNS":{"FallbackServers":["[::1]:65536"]}}`, `{"Pmark":{"TombTTL":"1s"}}`} {
		if _, err := ParseLinuxFileConfig([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func FuzzLinuxBackendConfig(f *testing.F) {
	for _, seed := range []string{`{}`, `null`, `{"UserMark":"0x414c4d01"}`, `{"DNS":{"FallbackServers":["::1"]}}`, `{"Callbacks":{"DNSConfigured":{"Command":"notify"}}}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		parsed, err := ParseLinuxFileConfig(input)
		if err != nil {
			return
		}
		if !json.Valid(input) {
			t.Fatal("accepted invalid JSON")
		}
		native := parsed.Native(nil)
		if native.Logf != nil || !reflect.DeepEqual(native.Callbacks, sysnetlinux.Callbacks{}) {
			t.Fatal("input installed executable code")
		}
		for _, server := range native.DNS.FallbackServers {
			if !server.IsValid() {
				t.Fatal("accepted invalid DNS server")
			}
		}
	})
}
