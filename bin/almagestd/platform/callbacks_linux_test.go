//go:build linux

package platform

import (
	"net/netip"
	"testing"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetlinuxkillswitch "github.com/asciimoth/sysnet-linux/killswitch"
	sysnetlinuxrouting "github.com/asciimoth/sysnet-linux/routing"
)

type callbackTestDispatcher struct {
	requests []cmdrunner.Request
	accept   bool
}

func (d *callbackTestDispatcher) SubmitBackendCallback(req cmdrunner.Request) bool {
	d.requests = append(d.requests, req)
	return d.accept
}

func TestLinuxCallbacksDispatchConfiguredCommands(t *testing.T) {
	command := linuxCallbackCommand{Command: "hook", Slots: map[string]string{"callback": "{{.Callback}}"}}
	config := linuxCallbacksConfig{
		TunCreated:           command,
		TunConfigured:        command,
		DefaultTunCreated:    command,
		DefaultTunConfigured: command,
		DefaultTunClosed:     command,
		RoutingApplied:       command,
		DNSConfigured:        command,
		KillswitchUpdated:    command,
	}
	if !config.configured() || (linuxCallbacksConfig{}).configured() || (linuxCallbackCommand{}).configured() {
		t.Fatal("callback configured state is incorrect")
	}
	dispatcher := &callbackTestDispatcher{accept: true}
	callbacks := config.sysnetCallbacks(dispatcher)
	callbacks.TunCreated(nil)
	callbacks.TunConfigured(nil, sysnet.TunOpts{})
	callbacks.DefaultTunCreated(nil)
	callbacks.DefaultTunConfigured(nil, sysnet.DefaultTunOpts{})
	callbacks.DefaultTunClosed()
	callbacks.RoutingApplied(sysnetlinuxrouting.Config{})
	callbacks.DNSConfigured(netip.MustParseAddr("192.0.2.1"))
	callbacks.KillswitchUpdated(sysnetlinuxkillswitch.AllowRules{})
	if len(dispatcher.requests) != 8 {
		t.Fatalf("requests = %#v", dispatcher.requests)
	}
	for _, req := range dispatcher.requests {
		if req.Name != "hook" || req.Slots["callback"] != req.Label {
			t.Errorf("request = %#v", req)
		}
	}

	// These paths must drop the callback safely.
	submitLinuxCallbackCommand(nil, "nil-runner", command, map[string]any{"Callback": "nil-runner"})
	submitLinuxCallbackCommand(dispatcher, "bad-template", linuxCallbackCommand{
		Command: "hook",
		Slots:   map[string]string{"bad": "{{"},
	}, map[string]any{})
	dispatcher.accept = false
	submitLinuxCallbackCommand(dispatcher, "stopped", command, map[string]any{"Callback": "stopped"})
}

func TestTunCallbackValuesForNilTun(t *testing.T) {
	values := tunCallbackValues("test", nil)
	if values["Callback"] != "test" || values["TunName"] != "" || values["TunMTU"] != 0 || values["TunNative"] != false {
		t.Fatalf("values = %#v", values)
	}
}
