//go:build linux

package platform

import (
	"net/netip"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
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
	TunCreated           cmdrunner.Command
	TunConfigured        cmdrunner.Command
	DefaultTunCreated    cmdrunner.Command
	DefaultTunConfigured cmdrunner.Command
	DefaultTunClosed     cmdrunner.Command
	RoutingApplied       cmdrunner.Command
	DNSConfigured        cmdrunner.Command
	KillswitchUpdated    cmdrunner.Command
}

func (c linuxCallbacksConfig) configured() bool {
	return c.TunCreated.Configured() ||
		c.TunConfigured.Configured() ||
		c.DefaultTunCreated.Configured() ||
		c.DefaultTunConfigured.Configured() ||
		c.DefaultTunClosed.Configured() ||
		c.RoutingApplied.Configured() ||
		c.DNSConfigured.Configured() ||
		c.KillswitchUpdated.Configured()
}

func (c linuxCallbacksConfig) sysnetCallbacks(runner cmdrunner.Dispatcher) sysnetlinux.Callbacks {
	var callbacks sysnetlinux.Callbacks
	if c.TunCreated.Configured() {
		callbacks.TunCreated = func(t gtun.Tun) {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackTunCreated,
				c.TunCreated,
				tunCallbackValues(linuxCallbackTunCreated, t),
			)
		}
	}
	if c.TunConfigured.Configured() {
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
	if c.DefaultTunCreated.Configured() {
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
	if c.DefaultTunConfigured.Configured() {
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
	if c.DefaultTunClosed.Configured() {
		callbacks.DefaultTunClosed = func() {
			submitLinuxCallbackCommand(
				runner,
				linuxCallbackDefaultTunClosed,
				c.DefaultTunClosed,
				map[string]any{"Callback": linuxCallbackDefaultTunClosed},
			)
		}
	}
	if c.RoutingApplied.Configured() {
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
	if c.DNSConfigured.Configured() {
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
	if c.KillswitchUpdated.Configured() {
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
	runner cmdrunner.Dispatcher,
	name string,
	config cmdrunner.Command,
	values map[string]any,
) {
	if runner == nil {
		golog.Warnf("sysnet-linux callback command %s dropped: command runner is unavailable", name)
		return
	}
	config.Name = name
	if !runner.Submit(config, values) {
		golog.Warnf("sysnet-linux callback command %s dropped: command runner is stopped", name)
	}
}
