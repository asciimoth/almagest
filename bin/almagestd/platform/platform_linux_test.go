//go:build linux

package platform

import (
	"net/netip"
	"testing"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/configer/hujson"
)

func TestReadLinuxSystemConfigDefaults(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := readLinuxSystemConfig(cfg, nil)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}

	if got.Pmark.PinPath != "/sys/fs/bpf/almagest" {
		t.Fatalf("Pmark.PinPath = %q, want default", got.Pmark.PinPath)
	}
	if got.UserMark != linuxDefaultUserMark {
		t.Fatalf("UserMark = %#x, want %#x", got.UserMark, linuxDefaultUserMark)
	}
	if !got.KillswitchAllowExclude {
		t.Fatal("KillswitchAllowExclude = false, want true")
	}
	wantFallback := []netip.AddrPort{
		netip.MustParseAddrPort("8.8.8.8:53"),
		netip.MustParseAddrPort("1.1.1.1:53"),
	}
	if len(got.DNS.FallbackServers) != len(wantFallback) {
		t.Fatalf("FallbackServers = %v, want %v", got.DNS.FallbackServers, wantFallback)
	}
	for i, want := range wantFallback {
		if got.DNS.FallbackServers[i] != want {
			t.Fatalf("FallbackServers[%d] = %v, want %v", i, got.DNS.FallbackServers[i], want)
		}
	}
	if got.Logf == nil {
		t.Fatal("Logf = nil, want golog.Debugf")
	}
}

func TestReadLinuxSystemConfigOverrides(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"Pmark": {
				"PinPath": "/run/almagest-bpf",
				"Callbacks": {
					"Logf": "ignored for now"
				}
			},
			"DNS": {
				"Mode": "direct",
				"FallbackServers": ["9.9.9.9", "149.112.112.112:5353"]
			},
			"KillswitchAllowExclude": false,
			"AppBypassMark": "0xeb9f0001",
			"AppBypassMask": "ffffffff",
			"UserMark": 1095527682
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := readLinuxSystemConfig(cfg, nil)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}

	if got.Pmark.PinPath != "/run/almagest-bpf" {
		t.Fatalf("Pmark.PinPath = %q, want override", got.Pmark.PinPath)
	}
	if got.DNS.Mode != "direct" {
		t.Fatalf("DNS.Mode = %q, want direct", got.DNS.Mode)
	}
	wantFallback := []netip.AddrPort{
		netip.MustParseAddrPort("9.9.9.9:53"),
		netip.MustParseAddrPort("149.112.112.112:5353"),
	}
	for i, want := range wantFallback {
		if got.DNS.FallbackServers[i] != want {
			t.Fatalf("FallbackServers[%d] = %v, want %v", i, got.DNS.FallbackServers[i], want)
		}
	}
	if got.KillswitchAllowExclude {
		t.Fatal("KillswitchAllowExclude = true, want false override")
	}
	if got.AppBypassMark != 0xeb9f0001 {
		t.Fatalf("AppBypassMark = %#x, want override", got.AppBypassMark)
	}
	if got.AppBypassMask != 0xffffffff {
		t.Fatalf("AppBypassMask = %#x, want override", got.AppBypassMask)
	}
	if got.UserMark != 1095527682 {
		t.Fatalf("UserMark = %#x, want override", got.UserMark)
	}
}

func TestReadLinuxSystemConfigHexMarkStrings(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"AppBypassMark": "0xeb9f0001",
			"AppBypassMask": "ffffffff",
			"UserMark": "0x414c4d01",
			"UserMarkMask": "FFFFFFFF"
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := readLinuxSystemConfig(cfg, nil)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}

	if got.AppBypassMark != 0xeb9f0001 {
		t.Fatalf("AppBypassMark = %#x, want 0xeb9f0001", got.AppBypassMark)
	}
	if got.AppBypassMask != 0xffffffff {
		t.Fatalf("AppBypassMask = %#x, want 0xffffffff", got.AppBypassMask)
	}
	if got.UserMark != 0x414c4d01 {
		t.Fatalf("UserMark = %#x, want 0x414c4d01", got.UserMark)
	}
	if got.UserMarkMask != 0xffffffff {
		t.Fatalf("UserMarkMask = %#x, want 0xffffffff", got.UserMarkMask)
	}
}

func TestReadLinuxSystemConfigCallbacks(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"Callbacks": {
				"DNSConfigured": {
					"Command": "notify-dns",
					"Slots": {
						"callback": "{{ .Callback }}",
						"server": "{{ .Server }}"
					}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	runner := &recordingDispatcher{}

	got, err := readLinuxSystemConfig(cfg, runner)
	if err != nil {
		t.Fatalf("readLinuxSystemConfig: %v", err)
	}
	if got.Callbacks.DNSConfigured == nil {
		t.Fatal("DNSConfigured callback = nil, want command callback")
	}

	got.Callbacks.DNSConfigured(netip.MustParseAddr("192.0.2.53"))

	if len(runner.requests) != 1 {
		t.Fatalf("submitted requests = %d, want 1", len(runner.requests))
	}
	req := runner.requests[0]
	if req.Name != "notify-dns" {
		t.Fatalf("request command name = %q, want notify-dns", req.Name)
	}
	if req.Label != linuxCallbackDNSConfigured {
		t.Fatalf("request label = %q, want %q", req.Label, linuxCallbackDNSConfigured)
	}
	if got := req.Slots["callback"]; got != linuxCallbackDNSConfigured {
		t.Fatalf("request slot callback = %q, want %q", got, linuxCallbackDNSConfigured)
	}
	if got := req.Slots["server"]; got != "192.0.2.53" {
		t.Fatalf("request slot server = %q, want 192.0.2.53", got)
	}
}

func TestReadLinuxSystemConfigCallbacksRequireRunner(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Linux": {
			"Callbacks": {
				"DefaultTunClosed": {
					"Command": "notify-close"
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = readLinuxSystemConfig(cfg, nil)
	if err == nil {
		t.Fatal("readLinuxSystemConfig succeeded without callback runner")
	}
}

type recordingDispatcher struct {
	requests []recordedCommandRequest
}

type recordedCommandRequest struct {
	cmdrunner.Request
}

func (d *recordingDispatcher) Submit(req cmdrunner.Request) bool {
	d.requests = append(d.requests, recordedCommandRequest{Request: req})
	return true
}
