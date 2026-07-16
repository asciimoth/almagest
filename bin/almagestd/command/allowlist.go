package command

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"
)

const (
	// MaxSlotValueLength is the largest configured per-slot MaxLength accepted
	// by the runner. The protected config may choose lower limits per command
	// slot, but it cannot disable slot size checks entirely.
	MaxSlotValueLength = 32 * 1024

	maxCommandNameLen = 128
	maxSlotNameLen    = 64
)

var (
	validCommandNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	validSlotNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Config is the protected command-runner allowlist read once during daemon
// startup.
//
// Only entries in Commands can ever be executed by Runner. Admin APIs and
// other runtime callers submit a Request naming one entry and supplying slot
// values; they must not pass argv, environment variables, credentials, or shell
// fragments directly. Keeping those execution properties in protected config is
// the boundary that prevents a writable owner channel from becoming arbitrary
// command execution.
type Config struct {
	Commands map[string]AllowedCommand `json:"Commands,omitempty"`
}

// AllowedCommand defines one executable command in protected config.
//
// Command is an argv array with a fixed executable in element 0. Later argv
// elements may contain slot placeholders like ${tun_name}. Slot values are
// validated by the matching SlotLimit before substitution. Prefer direct
// executable argv over shells; if a shell is unavoidable, every slot used in the
// shell script must have a restrictive Pattern and MaxLength because the shell
// will interpret the rendered value.
type AllowedCommand struct {
	Command []string          `json:"Command"`
	Env     map[string]string `json:"Env,omitempty"`
	UID     *uint32           `json:"UID,omitempty"`
	GID     *uint32           `json:"GID,omitempty"`
	Sync    bool              `json:"Sync,omitempty"`
	Timeout time.Duration     `json:"Timeout,omitempty"`
	Slots   map[string]Slot   `json:"Slots,omitempty"`

	// AllowNodes makes this protected allowlist entry visible to the
	// almagestd/Command node. Commands without this explicit opt-in may still be
	// used by other protected integrations, but command nodes must treat them as
	// unavailable even when a workspace config names them.
	AllowNodes bool `json:"AllowNodes,omitempty"`
}

// Configured reports whether c contains an argv array to execute.
func (c AllowedCommand) Configured() bool {
	return len(c.Command) > 0
}

// Slot limits one runtime value accepted by an AllowedCommand.
//
// MaxLength is mandatory and counts bytes after template rendering. Pattern is
// an optional Go regexp that must match the complete slot value, not merely a
// substring. Required slots must be present and non-empty. Optional slots may be
// omitted; when an omitted or explicitly empty slot renders an argv element to
// an empty string, that argv element is dropped.
type Slot struct {
	MaxLength  int    `json:"MaxLength"`
	Pattern    string `json:"Pattern,omitempty"`
	Required   bool   `json:"Required,omitempty"`
	AllowEmpty bool   `json:"AllowEmpty,omitempty"`
}

// Request asks Runner to execute one protected allowlist entry.
//
// Name is the key in Config.Commands. Label is a non-secret, static event label
// used for logs, such as a sysnet callback name; do not fill it with admin/user
// input or slot values. Slots contains runtime values only. Extra slots and
// values that violate their configured Slot limits are rejected before exec.
type Request struct {
	Name  string
	Label string
	Slots map[string]string
}

type registry struct {
	commands map[string]compiledCommand
}

type compiledCommand struct {
	name    string
	command []string
	env     map[string]string
	uid     *uint32
	gid     *uint32
	sync    bool
	timeout time.Duration
	slots   map[string]compiledSlot
}

type compiledSlot struct {
	limit   Slot
	pattern *regexp.Regexp
}

type resolvedCommand struct {
	Name    string
	Label   string
	Command []string
	Env     map[string]string
	UID     *uint32
	GID     *uint32
	Sync    bool
	Timeout time.Duration
}

func newRegistry(cfg Config) (*registry, error) {
	out := &registry{
		commands: make(map[string]compiledCommand, len(cfg.Commands)),
	}
	for name, command := range cfg.Commands {
		compiled, err := compileAllowedCommand(name, command)
		if err != nil {
			return nil, fmt.Errorf("allowed command %q: %w", name, err)
		}
		if _, exists := out.commands[compiled.name]; exists {
			return nil, fmt.Errorf("duplicate allowed command %q", compiled.name)
		}
		out.commands[compiled.name] = compiled
	}
	return out, nil
}

func compileAllowedCommand(name string, cfg AllowedCommand) (compiledCommand, error) {
	if err := validateCommandName(name); err != nil {
		return compiledCommand{}, err
	}
	if len(cfg.Command) == 0 {
		return compiledCommand{}, errors.New("empty argv")
	}
	if strings.TrimSpace(cfg.Command[0]) == "" {
		return compiledCommand{}, errors.New("empty executable")
	}
	if cfg.Timeout < 0 {
		return compiledCommand{}, errors.New("negative timeout")
	}
	if err := validateEnvKeys(cfg.Env); err != nil {
		return compiledCommand{}, err
	}

	slots, err := compileSlots(cfg.Slots)
	if err != nil {
		return compiledCommand{}, err
	}
	usedSlots := map[string]struct{}{}
	for i, arg := range cfg.Command {
		refs, err := slotReferences(arg)
		if err != nil {
			return compiledCommand{}, fmt.Errorf("argv[%d]: %w", i, err)
		}
		if i == 0 && len(refs) > 0 {
			return compiledCommand{}, errors.New("executable must be fixed and cannot contain slot placeholders")
		}
		for _, ref := range refs {
			if _, ok := slots[ref]; !ok {
				return compiledCommand{}, fmt.Errorf("argv[%d]: undeclared slot %q", i, ref)
			}
			usedSlots[ref] = struct{}{}
		}
	}
	for name := range slots {
		if _, ok := usedSlots[name]; !ok {
			return compiledCommand{}, fmt.Errorf("slot %q is declared but not used in argv", name)
		}
	}

	return compiledCommand{
		name:    name,
		command: slices.Clone(cfg.Command),
		env:     cloneStringMap(cfg.Env),
		uid:     cloneUint32Ptr(cfg.UID),
		gid:     cloneUint32Ptr(cfg.GID),
		sync:    cfg.Sync,
		timeout: cfg.Timeout,
		slots:   slots,
	}, nil
}

func validateCommandName(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if len(name) > maxCommandNameLen {
		return fmt.Errorf("name exceeds %d bytes", maxCommandNameLen)
	}
	if !validCommandNameRE.MatchString(name) {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

func compileSlots(cfg map[string]Slot) (map[string]compiledSlot, error) {
	if len(cfg) == 0 {
		return nil, nil
	}
	out := make(map[string]compiledSlot, len(cfg))
	for name, limit := range cfg {
		if err := validateSlotName(name); err != nil {
			return nil, err
		}
		if limit.MaxLength <= 0 {
			return nil, fmt.Errorf("slot %q MaxLength must be greater than zero", name)
		}
		if limit.MaxLength > MaxSlotValueLength {
			return nil, fmt.Errorf("slot %q MaxLength exceeds %d", name, MaxSlotValueLength)
		}
		if limit.Required && limit.AllowEmpty {
			return nil, fmt.Errorf("slot %q cannot be both Required and AllowEmpty", name)
		}
		var pattern *regexp.Regexp
		if limit.Pattern != "" {
			compiled, err := regexp.Compile(limit.Pattern)
			if err != nil {
				return nil, fmt.Errorf("slot %q Pattern: %w", name, err)
			}
			pattern = compiled
		}
		out[name] = compiledSlot{
			limit:   limit,
			pattern: pattern,
		}
	}
	return out, nil
}

func validateSlotName(name string) error {
	if name == "" {
		return errors.New("empty slot name")
	}
	if len(name) > maxSlotNameLen {
		return fmt.Errorf("slot name %q exceeds %d bytes", name, maxSlotNameLen)
	}
	if !validSlotNameRE.MatchString(name) {
		return fmt.Errorf("invalid slot name %q", name)
	}
	return nil
}

func (r *registry) resolve(req Request) (resolvedCommand, error) {
	if r == nil {
		return resolvedCommand{}, errors.New("command registry is not initialized")
	}
	compiled, ok := r.commands[req.Name]
	if !ok {
		return resolvedCommand{}, fmt.Errorf("command %q is not allowed", req.Name)
	}
	slots, err := compiled.validateSlots(req.Slots)
	if err != nil {
		return resolvedCommand{}, err
	}

	argv := make([]string, 0, len(compiled.command))
	for i, arg := range compiled.command {
		rendered, err := renderSlotPlaceholders(arg, slots)
		if err != nil {
			return resolvedCommand{}, fmt.Errorf("argv[%d]: %w", i, err)
		}
		if arg != "" && rendered == "" {
			continue
		}
		argv = append(argv, rendered)
	}
	out := resolvedCommand{
		Name:    compiled.name,
		Label:   req.Label,
		Command: argv,
		Env:     cloneStringMap(compiled.env),
		UID:     cloneUint32Ptr(compiled.uid),
		GID:     cloneUint32Ptr(compiled.gid),
		Sync:    compiled.sync,
		Timeout: compiled.timeout,
	}
	if err := out.validate(); err != nil {
		return resolvedCommand{}, err
	}
	return out, nil
}

func (c compiledCommand) validateSlots(input map[string]string) (map[string]string, error) {
	for name := range input {
		if err := validateSlotName(name); err != nil {
			return nil, err
		}
		if _, ok := c.slots[name]; !ok {
			return nil, fmt.Errorf("unknown slot %q", name)
		}
	}
	out := make(map[string]string, len(c.slots))
	for name, slot := range c.slots {
		value, ok := input[name]
		if !ok {
			if slot.limit.Required {
				return nil, fmt.Errorf("missing required slot %q", name)
			}
			out[name] = ""
			continue
		}
		if err := slot.validate(name, value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, nil
}

func (s compiledSlot) validate(name, value string) error {
	if value == "" {
		if s.limit.Required {
			return fmt.Errorf("slot %q is required", name)
		}
		if !s.limit.AllowEmpty {
			return fmt.Errorf("slot %q is empty", name)
		}
		return nil
	}
	if len(value) > s.limit.MaxLength {
		return fmt.Errorf("slot %q exceeds %d bytes", name, s.limit.MaxLength)
	}
	if s.pattern != nil && !regexpFullMatch(s.pattern, value) {
		return fmt.Errorf("slot %q does not match required pattern", name)
	}
	return nil
}

func regexpFullMatch(re *regexp.Regexp, value string) bool {
	match := re.FindStringIndex(value)
	return match != nil && match[0] == 0 && match[1] == len(value)
}

func slotReferences(arg string) ([]string, error) {
	var refs []string
	start := 0
	for {
		open := strings.Index(arg[start:], "${")
		if open < 0 {
			return refs, nil
		}
		open += start
		close := strings.IndexByte(arg[open+2:], '}')
		if close < 0 {
			return nil, errors.New("unterminated slot placeholder")
		}
		close += open + 2
		name := arg[open+2 : close]
		if err := validateSlotName(name); err != nil {
			return nil, fmt.Errorf("slot placeholder: %w", err)
		}
		refs = append(refs, name)
		start = close + 1
	}
}

func renderSlotPlaceholders(arg string, slots map[string]string) (string, error) {
	if !strings.Contains(arg, "${") {
		return arg, nil
	}
	var out strings.Builder
	start := 0
	for {
		open := strings.Index(arg[start:], "${")
		if open < 0 {
			out.WriteString(arg[start:])
			return out.String(), nil
		}
		open += start
		out.WriteString(arg[start:open])
		close := strings.IndexByte(arg[open+2:], '}')
		if close < 0 {
			return "", errors.New("unterminated slot placeholder")
		}
		close += open + 2
		name := arg[open+2 : close]
		value, ok := slots[name]
		if !ok {
			return "", fmt.Errorf("unknown slot %q", name)
		}
		out.WriteString(value)
		start = close + 1
	}
}

// RenderSlotTemplates renders protected callback slot templates into concrete
// runner slot values.
//
// This helper is for protected platform configuration that maps trusted daemon
// event fields to a command's declared slots. The returned values are still
// treated as untrusted by Runner and must satisfy the selected AllowedCommand's
// Slot limits before any process is started.
func RenderSlotTemplates(slotTemplates map[string]string, values any) (map[string]string, error) {
	if len(slotTemplates) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(slotTemplates))
	for name, value := range slotTemplates {
		if err := validateSlotName(name); err != nil {
			return nil, err
		}
		rendered, err := renderTemplateString(value, values)
		if err != nil {
			return nil, fmt.Errorf("slot %q: %w", name, err)
		}
		out[name] = rendered
	}
	return out, nil
}

func renderTemplateString(value string, values any) (string, error) {
	if !strings.Contains(value, "{{") {
		return value, nil
	}
	tmpl, err := template.New("slot").Option("missingkey=error").Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", value, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, values); err != nil {
		return "", fmt.Errorf("render template %q: %w", value, err)
	}
	return out.String(), nil
}

func validateEnvKeys(env map[string]string) error {
	for key := range env {
		if key == "" || strings.Contains(key, "=") {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
	}
	return nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneUint32Ptr(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
