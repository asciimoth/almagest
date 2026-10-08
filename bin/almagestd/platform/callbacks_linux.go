//go:build linux

package platform

import (
	"net/netip"
	"strings"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/gonnect/sysnet"
	gtun "github.com/asciimoth/gonnect/tun"
	sysnetlinux "github.com/asciimoth/sysnet-linux"
	sysnetlinuxkillswitch "github.com/asciimoth/sysnet-linux/killswitch"
	sysnetlinuxrouting "github.com/asciimoth/sysnet-linux/routing"
	"github.com/kataras/golog"
)

const (
	linuxCallbackTunCreated           = "sysnet-linux.tun-created"
	linuxCallbackTunConfigured        = "sysnet-linux.tun-configured"
	linuxCallbackDefaultTunCreated    = "sysnet-linux.default-tun-created"
	linuxCallbackDefaultTunConfigured = "sysnet-linux.default-tun-configured"
	linuxCallbackDefaultTunClosed     = "sysnet-linux.default-tun-closed"
	linuxCallbackRoutingApplied       = "sysnet-linux.routing-applied"
	linuxCallbackDNSConfigured        = "sysnet-linux.dns-configured"
	linuxCallbackKillswitchUpdated    = "sysnet-linux.killswitch-updated"
)

// linuxCallbacksConfig maps protected System.Linux.Callbacks entries to the
// sysnet-linux lifecycle callback names.
type linuxCallbacksConfig struct {
	TunCreated           linuxCallbackCommand
	TunConfigured        linuxCallbackCommand
	DefaultTunCreated    linuxCallbackCommand
	DefaultTunConfigured linuxCallbackCommand
	DefaultTunClosed     linuxCallbackCommand
	RoutingApplied       linuxCallbackCommand
	DNSConfigured        linuxCallbackCommand
	KillswitchUpdated    linuxCallbackCommand
}

// linuxCallbackCommand connects a sysnet-linux callback to one command-runner
// allowlist entry. Command must name a protected CommandRunner.Commands entry;
// Slots are protected templates that derive bounded runtime slot values from
// callback data. The callback config never contains executable argv, which keeps
// future admin-facing callback controls from becoming arbitrary command exec.
type linuxCallbackCommand struct {
	Command string            `json:"Command"`
	Slots   map[string]string `json:"Slots,omitempty"`
}

func (c linuxCallbackCommand) configured() bool {
	return strings.TrimSpace(c.Command) != ""
}

func (c linuxCallbacksConfig) configured() bool {
	return c.TunCreated.configured() ||
		c.TunConfigured.configured() ||
		c.DefaultTunCreated.configured() ||
		c.DefaultTunConfigured.configured() ||
		c.DefaultTunClosed.configured() ||
		c.RoutingApplied.configured() ||
		c.DNSConfigured.configured() ||
		c.KillswitchUpdated.configured()
}

func (c linuxCallbacksConfig) sysnetCallbacks(runner daemonservices.BackendCallbackDispatcher) sysnetlinux.Callbacks {
	var callbacks sysnetlinux.Callbacks
	if c.TunCreated.configured() {
		callbacks.TunCreated = func(t gtun.Tun) {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackTunCreated,
				c.TunCreated,
				tunCallbackValues(linuxCallbackTunCreated, t),
			)
		}
	}
	if c.TunConfigured.configured() {
		callbacks.TunConfigured = func(t gtun.Tun, opts sysnet.TunOpts) {
			values := tunCallbackValues(linuxCallbackTunConfigured, t)
			values["Opts"] = opts
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackTunConfigured,
				c.TunConfigured,
				values,
			)
		}
	}
	if c.DefaultTunCreated.configured() {
		callbacks.DefaultTunCreated = func(t sysnet.DefaultTun) {
			values := tunCallbackValues(linuxCallbackDefaultTunCreated, t)
			values["DefaultTun"] = t
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackDefaultTunCreated,
				c.DefaultTunCreated,
				values,
			)
		}
	}
	if c.DefaultTunConfigured.configured() {
		callbacks.DefaultTunConfigured = func(t sysnet.DefaultTun, opts sysnet.DefaultTunOpts) {
			values := tunCallbackValues(linuxCallbackDefaultTunConfigured, t)
			values["DefaultTun"] = t
			values["Opts"] = opts
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackDefaultTunConfigured,
				c.DefaultTunConfigured,
				values,
			)
		}
	}
	if c.DefaultTunClosed.configured() {
		callbacks.DefaultTunClosed = func() {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackDefaultTunClosed,
				c.DefaultTunClosed,
				map[string]any{"Callback": linuxCallbackDefaultTunClosed},
			)
		}
	}
	if c.RoutingApplied.configured() {
		callbacks.RoutingApplied = func(config sysnetlinuxrouting.Config) {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackRoutingApplied,
				c.RoutingApplied,
				map[string]any{
					"Callback": linuxCallbackRoutingApplied,
					"Config":   config,
				},
			)
		}
	}
	if c.DNSConfigured.configured() {
		callbacks.DNSConfigured = func(server netip.Addr) {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackDNSConfigured,
				c.DNSConfigured,
				map[string]any{
					"Callback": linuxCallbackDNSConfigured,
					"Server":   server,
				},
			)
		}
	}
	if c.KillswitchUpdated.configured() {
		callbacks.KillswitchUpdated = func(rules sysnetlinuxkillswitch.AllowRules) {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackKillswitchUpdated,
				c.KillswitchUpdated,
				map[string]any{
					"Callback": linuxCallbackKillswitchUpdated,
					"Rules":    rules,
				},
			)
		}
	}
	return callbacks
}

func tunCallbackValues(callback string, t gtun.Tun) map[string]any {
	values := map[string]any{
		"Callback":  callback,
		"Tun":       t,
		"TunName":   "",
		"TunMTU":    0,
		"TunNative": false,
	}
	if t == nil {
		return values
	}
	if name, err := t.Name(); err == nil {
		values["TunName"] = name
	}
	if mtu, err := t.MTU(); err == nil {
		values["TunMTU"] = mtu
	}
	values["TunNative"] = t.IsNative()
	return values
}

func submitLinuxCallbackCommand(
	runner daemonservices.BackendCallbackDispatcher,
	name string,
	config linuxCallbackCommand,
	values map[string]any,
) {
	if runner == nil {
		golog.Warnf("sysnet-linux callback command %s dropped: command runner is unavailable", name)
		return
	}
	slots, err := cmdrunner.RenderSlotTemplates(config.Slots, values)
	if err != nil {
		golog.Warnf("sysnet-linux callback command %s dropped: %v", name, err)
		return
	}
	req := cmdrunner.Request{
		Name:  config.Command,
		Label: name,
		Slots: slots,
	}
	if !runner.SubmitBackendCallback(req) {
		golog.Warnf("sysnet-linux callback command %s dropped: command runner is stopped", name)
	}
}
