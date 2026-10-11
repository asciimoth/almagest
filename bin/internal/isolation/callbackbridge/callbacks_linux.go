//go:build linux

// Package callbackbridge binds trusted Linux backend lifecycle callbacks to
// the isolated H callback lane. N renders protected templates; H still selects
// the fixed executable/profile and validates every rendered slot.
package callbackbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"text/template"

	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sysnet"
	gtun "github.com/asciimoth/gonnect/tun"
	sysnetlinux "github.com/asciimoth/sysnet-linux"
	sysnetlinuxkillswitch "github.com/asciimoth/sysnet-linux/killswitch"
	sysnetlinuxrouting "github.com/asciimoth/sysnet-linux/routing"
)

const (
	TunCreated           = "sysnet-linux.tun-created"
	TunConfigured        = "sysnet-linux.tun-configured"
	DefaultTunCreated    = "sysnet-linux.default-tun-created"
	DefaultTunConfigured = "sysnet-linux.default-tun-configured"
	DefaultTunClosed     = "sysnet-linux.default-tun-closed"
	RoutingApplied       = "sysnet-linux.routing-applied"
	DNSConfigured        = "sysnet-linux.dns-configured"
	KillswitchUpdated    = "sysnet-linux.killswitch-updated"
)

type command struct {
	Command string            `json:"Command"`
	Slots   map[string]string `json:"Slots,omitempty"`
}

type document struct {
	TunCreated           command
	TunConfigured        command
	DefaultTunCreated    command
	DefaultTunConfigured command
	DefaultTunClosed     command
	RoutingApplied       command
	DNSConfigured        command
	KillswitchUpdated    command
}

type binding struct {
	id      string
	command command
}

type Bindings struct{ values map[string]command }

// Compile strictly decodes protected callback bindings and adds the matching
// broker-only profiles to H's config. It never installs executable behavior.
func Compile(raw json.RawMessage, config *commandhelper.Config) (Bindings, error) {
	if config == nil {
		return Bindings{}, protocol.ErrInvalidContract
	}
	doc := document{}
	if len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&doc); err != nil {
			return Bindings{}, fmt.Errorf("protected System.Linux.Callbacks: %w", err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return Bindings{}, errors.New("protected System.Linux.Callbacks has trailing data")
		}
	}
	entries := []binding{{TunCreated, doc.TunCreated}, {TunConfigured, doc.TunConfigured},
		{DefaultTunCreated, doc.DefaultTunCreated}, {DefaultTunConfigured, doc.DefaultTunConfigured},
		{DefaultTunClosed, doc.DefaultTunClosed}, {RoutingApplied, doc.RoutingApplied},
		{DNSConfigured, doc.DNSConfigured}, {KillswitchUpdated, doc.KillswitchUpdated}}
	out := Bindings{values: make(map[string]command)}
	if config.Callbacks == nil {
		config.Callbacks = make(map[string]commandhelper.Callback)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.command.Command) == "" {
			if len(entry.command.Slots) != 0 {
				return Bindings{}, fmt.Errorf("callback %s has slots without a command", entry.id)
			}
			continue
		}
		selected, ok := config.Commands[entry.command.Command]
		if !ok {
			return Bindings{}, fmt.Errorf("callback %s selects unknown command %q", entry.id, entry.command.Command)
		}
		profile, ok := config.Profiles[selected.Profile]
		if !ok {
			return Bindings{}, fmt.Errorf("callback %s selects unavailable profile", entry.id)
		}
		forward := make([]string, 0, len(entry.command.Slots))
		for slot, source := range entry.command.Slots {
			if _, ok := profile.Slots[slot]; !ok {
				return Bindings{}, fmt.Errorf("callback %s selects unknown slot %q", entry.id, slot)
			}
			if _, err := template.New("slot").Option("missingkey=error").Parse(source); err != nil {
				return Bindings{}, fmt.Errorf("callback %s slot %q: %w", entry.id, slot, err)
			}
			forward = append(forward, slot)
		}
		slices.Sort(forward)
		config.Callbacks[entry.id] = commandhelper.Callback{Profile: selected.Profile, Event: entry.id, ForwardSlots: forward}
		out.values[entry.id] = entry.command
	}
	return out, nil
}

func (b Bindings) IDs() []string {
	ids := make([]string, 0, len(b.values))
	for id := range b.values {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (b Bindings) Empty() bool { return len(b.values) == 0 }

// Submitter accepts already rendered bounded values on N's private H lane.
type Submitter interface {
	Submit(callbackID, event string, slots map[string]string) bool
}

func (b Bindings) Native(submitter Submitter, logf func(string, ...any)) sysnetlinux.Callbacks {
	submit := func(id string, values any) {
		configured, ok := b.values[id]
		if !ok {
			return
		}
		slots, err := render(configured.Slots, values)
		if err != nil {
			if logf != nil {
				logf("callback %s dropped: %v", id, err)
			}
			return
		}
		if submitter == nil || !submitter.Submit(id, id, slots) {
			if logf != nil {
				logf("callback %s dropped: helper unavailable", id)
			}
		}
	}
	callbacks := sysnetlinux.Callbacks{}
	if _, ok := b.values[TunCreated]; ok {
		callbacks.TunCreated = func(t gtun.Tun) { submit(TunCreated, tunValues(TunCreated, t)) }
	}
	if _, ok := b.values[TunConfigured]; ok {
		callbacks.TunConfigured = func(t gtun.Tun, options sysnet.TunOpts) {
			values := tunValues(TunConfigured, t)
			values["Opts"] = options
			submit(TunConfigured, values)
		}
	}
	if _, ok := b.values[DefaultTunCreated]; ok {
		callbacks.DefaultTunCreated = func(t sysnet.DefaultTun) {
			values := tunValues(DefaultTunCreated, t)
			values["DefaultTun"] = t
			submit(DefaultTunCreated, values)
		}
	}
	if _, ok := b.values[DefaultTunConfigured]; ok {
		callbacks.DefaultTunConfigured = func(t sysnet.DefaultTun, options sysnet.DefaultTunOpts) {
			values := tunValues(DefaultTunConfigured, t)
			values["DefaultTun"], values["Opts"] = t, options
			submit(DefaultTunConfigured, values)
		}
	}
	if _, ok := b.values[DefaultTunClosed]; ok {
		callbacks.DefaultTunClosed = func() { submit(DefaultTunClosed, map[string]any{"Callback": DefaultTunClosed}) }
	}
	if _, ok := b.values[RoutingApplied]; ok {
		callbacks.RoutingApplied = func(config sysnetlinuxrouting.Config) {
			submit(RoutingApplied, map[string]any{"Callback": RoutingApplied, "Config": config})
		}
	}
	if _, ok := b.values[DNSConfigured]; ok {
		callbacks.DNSConfigured = func(server netip.Addr) {
			submit(DNSConfigured, map[string]any{"Callback": DNSConfigured, "Server": server})
		}
	}
	if _, ok := b.values[KillswitchUpdated]; ok {
		callbacks.KillswitchUpdated = func(rules sysnetlinuxkillswitch.AllowRules) {
			submit(KillswitchUpdated, map[string]any{"Callback": KillswitchUpdated, "Rules": rules})
		}
	}
	return callbacks
}

func render(inputs map[string]string, values any) (map[string]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(inputs))
	for name, input := range inputs {
		if !strings.Contains(input, "{{") {
			out[name] = input
			continue
		}
		tmpl, err := template.New("slot").Option("missingkey=error").Parse(input)
		if err != nil {
			return nil, err
		}
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, values); err != nil {
			return nil, err
		}
		out[name] = rendered.String()
	}
	return out, nil
}

func tunValues(event string, t gtun.Tun) map[string]any {
	values := map[string]any{"Callback": event, "Tun": t, "TunName": "", "TunMTU": 0, "TunNative": false}
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
