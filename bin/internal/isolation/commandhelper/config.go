// Package commandhelper implements the isolated restricted command helper.
// Production bootstrap fixes its executable and profiles before runtime starts.
package commandhelper

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const (
	maxIDBytes          = 128
	maxArgumentCount    = 128
	maxArgumentBytes    = 32 * 1024
	maxEnvironmentItems = 128
	maxEnvironmentBytes = 32 * 1024
	maxCallbackBindings = 32
	maxConcurrentRuns   = 32
	defaultTimeout      = 10 * time.Second
	defaultOutputLimit  = 64 * 1024
)

var (
	validID        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	validSlotName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	errOutputLimit = errors.New("command output limit exceeded")
)

// Config is fixed protected input. Requests can select an ID and provide slot
// values, but cannot change a profile or any process property.
type Config struct {
	Profiles  map[string]Profile
	Commands  map[string]Command
	Callbacks map[string]Callback
}

// Profile defines all process inputs and limits. Sandbox names are resolved by
// the trusted launcher supplied to New. A string from a request never selects
// a sandbox.
type Profile struct {
	Sandbox          string
	Executable       string
	Arguments        []string
	Environment      map[string]string
	WorkingDirectory string
	Timeout          time.Duration
	MaxOutputBytes   int
	MaxConcurrent    int
	Slots            map[string]Slot
}

// Slot constrains one value before it is substituted into an argument.
// AllowLeadingHyphen is necessary only when a complete argument can be an
// option. Shell metacharacters have no special meaning because interpreters
// are not valid profiles.
type Slot struct {
	MaxLength          int
	Pattern            string
	Required           bool
	AllowEmpty         bool
	AllowLeadingHyphen bool
}

// Command binds a known command ID to a protected profile.
type Command struct {
	Profile    string
	AllowNodes bool
}

// Callback maps one broker-only event to a fixed profile. Slot values are
// derived from typed request fields by H, not supplied as a runtime map.
type Callback struct {
	Profile      string
	Event        string
	ResourceType protocol.ResourceType
	Slots        map[string]CallbackField
	ForwardSlots []string
}

// CallbackField selects one trusted callback value for a protected slot.
type CallbackField string

const (
	// CallbackEvent binds the fixed event name.
	CallbackEvent CallbackField = "event"
	// CallbackResourceID binds the committed resource ID.
	CallbackResourceID CallbackField = "resource_id"
	// CallbackResourceType binds the committed resource type.
	CallbackResourceType CallbackField = "resource_type"
	// CallbackResourceGeneration binds the committed resource generation.
	CallbackResourceGeneration CallbackField = "resource_generation"
	// CallbackGraphEpoch binds the committed resource graph epoch.
	CallbackGraphEpoch CallbackField = "graph_epoch"
)

type compiledConfig struct {
	profiles  map[string]*compiledProfile
	commands  map[string]Command
	callbacks map[string]Callback
}

type compiledProfile struct {
	profile    Profile
	slots      map[string]compiledSlot
	executable fileIdentity
	directory  fileIdentity
	semaphore  chan struct{}
}

type compiledSlot struct {
	limit   Slot
	pattern *regexp.Regexp
}

type fileIdentity struct {
	path   string
	info   os.FileInfo
	digest [32]byte
}

func compileConfig(cfg Config, launcher Launcher) (*compiledConfig, error) {
	if launcher == nil {
		return nil, errors.New("command launcher is required")
	}
	out := &compiledConfig{profiles: make(map[string]*compiledProfile, len(cfg.Profiles)), commands: make(map[string]Command, len(cfg.Commands)), callbacks: make(map[string]Callback, len(cfg.Callbacks))}
	for id, profile := range cfg.Profiles {
		if err := validateID("profile", id); err != nil {
			return nil, err
		}
		compiled, err := compileProfile(profile, launcher)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", id, err)
		}
		out.profiles[id] = compiled
	}
	for id, command := range cfg.Commands {
		if err := validateID("command", id); err != nil {
			return nil, err
		}
		if out.profiles[command.Profile] == nil {
			return nil, fmt.Errorf("command %q: unknown profile %q", id, command.Profile)
		}
		out.commands[id] = command
	}
	for id, callback := range cfg.Callbacks {
		if err := validateID("callback", id); err != nil {
			return nil, err
		}
		profile := out.profiles[callback.Profile]
		if profile == nil {
			return nil, fmt.Errorf("callback %q: unknown profile %q", id, callback.Profile)
		}
		if err := validateID("callback event", callback.Event); err != nil {
			return nil, fmt.Errorf("callback %q: %w", id, err)
		}
		if len(callback.ForwardSlots) == 0 && !callbackResourceType(callback.ResourceType) || len(callback.ForwardSlots) != 0 && callback.ResourceType != "" {
			return nil, fmt.Errorf("callback %q: invalid resource type %q", id, callback.ResourceType)
		}
		if len(callback.Slots) > maxCallbackBindings {
			return nil, fmt.Errorf("callback %q: too many slot bindings", id)
		}
		if len(callback.ForwardSlots) > maxCallbackBindings || len(callback.Slots) != 0 && len(callback.ForwardSlots) != 0 {
			return nil, fmt.Errorf("callback %q: invalid forwarded slot bindings", id)
		}
		seenForward := make(map[string]bool, len(callback.ForwardSlots))
		for _, slot := range callback.ForwardSlots {
			if profile.slots[slot].limit.MaxLength == 0 || seenForward[slot] {
				return nil, fmt.Errorf("callback %q: unknown or duplicate forwarded slot %q", id, slot)
			}
			seenForward[slot] = true
		}
		for slot, field := range callback.Slots {
			if profile.slots[slot].limit.MaxLength == 0 {
				return nil, fmt.Errorf("callback %q: unknown profile slot %q", id, slot)
			}
			if !field.valid() {
				return nil, fmt.Errorf("callback %q: invalid field %q", id, field)
			}
		}
		for slot, value := range profile.slots {
			if value.limit.Required {
				_, typed := callback.Slots[slot]
				if !typed && !seenForward[slot] {
					return nil, fmt.Errorf("callback %q: required slot %q has no binding", id, slot)
				}
			}
		}
		copyCallback := callback
		copyCallback.Slots = cloneCallbackFields(callback.Slots)
		copyCallback.ForwardSlots = slices.Clone(callback.ForwardSlots)
		out.callbacks[id] = copyCallback
	}
	return out, nil
}

func compileProfile(profile Profile, launcher Launcher) (*compiledProfile, error) {
	if err := validateID("sandbox", profile.Sandbox); err != nil {
		return nil, err
	}
	if err := ValidateExecutablePath(profile.Executable); err != nil {
		return nil, err
	}
	if profile.WorkingDirectory == "" || !filepath.IsAbs(profile.WorkingDirectory) || filepath.Clean(profile.WorkingDirectory) != profile.WorkingDirectory {
		return nil, errors.New("working directory must be an absolute canonical path")
	}
	if profile.Timeout <= 0 {
		profile.Timeout = defaultTimeout
	}
	if profile.MaxOutputBytes <= 0 {
		profile.MaxOutputBytes = defaultOutputLimit
	}
	if profile.Timeout > time.Hour || profile.MaxOutputBytes > protocol.MaxCommandOutputBytes || profile.MaxConcurrent <= 0 || profile.MaxConcurrent > maxConcurrentRuns {
		return nil, errors.New("timeout, output, or concurrency limit is outside compiled bounds")
	}
	if len(profile.Arguments) > maxArgumentCount || len(profile.Environment) > maxEnvironmentItems || len(profile.Slots) > protocol.MaxCommandSlots {
		return nil, errors.New("argument, environment, or slot count exceeds compiled bounds")
	}
	argumentBytes := 0
	used := make(map[string]bool)
	for i, argument := range profile.Arguments {
		argumentBytes += len(argument)
		refs, err := slotReferences(argument)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i, err)
		}
		for _, ref := range refs {
			if _, ok := profile.Slots[ref]; !ok {
				return nil, fmt.Errorf("argument %d: undeclared slot %q", i, ref)
			}
			used[ref] = true
		}
	}
	if argumentBytes > maxArgumentBytes {
		return nil, errors.New("argument bytes exceed compiled bound")
	}
	slots := make(map[string]compiledSlot, len(profile.Slots))
	for name, limit := range profile.Slots {
		if !validSlotName.MatchString(name) || len(name) > 64 || !used[name] {
			return nil, fmt.Errorf("invalid or unused slot %q", name)
		}
		if limit.MaxLength <= 0 || limit.MaxLength > protocol.MaxCommandSlotBytes || limit.Required && limit.AllowEmpty {
			return nil, fmt.Errorf("slot %q has invalid limits", name)
		}
		var pattern *regexp.Regexp
		if limit.Pattern != "" {
			var err error
			pattern, err = regexp.Compile(limit.Pattern)
			if err != nil {
				return nil, fmt.Errorf("slot %q pattern: %w", name, err)
			}
		}
		slots[name] = compiledSlot{limit: limit, pattern: pattern}
	}
	environmentBytes := 0
	for key, value := range profile.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("invalid environment entry %q", key)
		}
		environmentBytes += len(key) + len(value) + 1
	}
	if environmentBytes > maxEnvironmentBytes {
		return nil, errors.New("environment bytes exceed compiled bound")
	}
	if err := validatePlatformEnvironment(profile.Environment); err != nil {
		return nil, err
	}
	executable, err := inspectFile(profile.Executable, false)
	if err != nil {
		return nil, fmt.Errorf("inspect executable: %w", err)
	}
	directory, err := inspectFile(profile.WorkingDirectory, true)
	if err != nil {
		return nil, fmt.Errorf("inspect working directory: %w", err)
	}
	profile.Arguments = slices.Clone(profile.Arguments)
	profile.Environment = cloneStrings(profile.Environment)
	profile.Slots = cloneSlots(profile.Slots)
	if err := launcher.Validate(profile); err != nil {
		return nil, fmt.Errorf("unsupported protected execution profile: %w; change the profile or use explicit --no-isolation", err)
	}
	return &compiledProfile{profile: profile, slots: slots, executable: executable, directory: directory, semaphore: make(chan struct{}, profile.MaxConcurrent)}, nil
}

// ValidateExecutablePath applies the isolated executable-name policy without
// opening the path. Trusted bootstrap uses it before a host executable is
// copied into H's fixed /command location; H validates /command again after
// its filesystem restrictions are installed.
func ValidateExecutablePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("executable must be an absolute canonical path")
	}
	if isInterpreter(path) {
		return errors.New("shell and interpreter executables are not supported in isolated mode; change the profile or use explicit --no-isolation")
	}
	return nil
}

func inspectFile(path string, directory bool) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || (!directory && !info.Mode().IsRegular()) {
		return fileIdentity{}, errors.New("path has an unsafe type")
	}
	identity := fileIdentity{path: path, info: info}
	if directory {
		return identity, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fileIdentity{}, err
	}
	defer f.Close() //nolint:errcheck
	identity.digest, err = digestReader(f)
	return identity, err
}

func (f fileIdentity) unchanged() error {
	current, err := inspectFile(f.path, f.info.IsDir())
	if err != nil {
		return err
	}
	if !os.SameFile(f.info, current.info) || (!f.info.IsDir() && f.digest != current.digest) {
		return errors.New("protected path identity or content changed")
	}
	return nil
}

func digestReader(r io.Reader) ([32]byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func (p *compiledProfile) resolve(input map[string]string) (Execution, error) {
	if len(input) > protocol.MaxCommandSlots {
		return Execution{}, protocol.ErrLimitExceeded
	}
	total := 0
	values := make(map[string]string, len(p.slots))
	for name := range input {
		if !validSlotName.MatchString(name) || p.slots[name].limit.MaxLength == 0 {
			return Execution{}, fmt.Errorf("unknown slot %q", name)
		}
	}
	for name, slot := range p.slots {
		value, exists := input[name]
		if !exists {
			if slot.limit.Required {
				return Execution{}, fmt.Errorf("missing required slot %q", name)
			}
			value = ""
		}
		if err := slot.validate(name, value); err != nil {
			return Execution{}, err
		}
		total += len(name) + len(value)
		if total > protocol.MaxCommandTotalSlotBytes {
			return Execution{}, protocol.ErrLimitExceeded
		}
		values[name] = value
	}
	arguments := make([]string, 0, len(p.profile.Arguments))
	argumentBytes := 0
	for _, argument := range p.profile.Arguments {
		rendered, err := renderArgument(argument, values, p.slots)
		if err != nil {
			return Execution{}, err
		}
		if argument != "" && rendered == "" {
			continue
		}
		if strings.ContainsRune(rendered, 0) {
			return Execution{}, errors.New("argument contains NUL")
		}
		argumentBytes += len(rendered)
		if argumentBytes > maxArgumentBytes {
			return Execution{}, protocol.ErrLimitExceeded
		}
		arguments = append(arguments, rendered)
	}
	return Execution{Sandbox: p.profile.Sandbox, Executable: p.profile.Executable, Arguments: arguments, Environment: environmentList(p.profile.Environment), WorkingDirectory: p.profile.WorkingDirectory, OutputLimit: p.profile.MaxOutputBytes}, nil
}

func (s compiledSlot) validate(name, value string) error {
	if value == "" {
		if s.limit.Required || !s.limit.AllowEmpty {
			return fmt.Errorf("slot %q is empty", name)
		}
		return nil
	}
	if len(value) > s.limit.MaxLength || strings.ContainsRune(value, 0) {
		return fmt.Errorf("slot %q exceeds its limit or contains NUL", name)
	}
	if s.pattern != nil {
		match := s.pattern.FindStringIndex(value)
		if match == nil || match[0] != 0 || match[1] != len(value) {
			return fmt.Errorf("slot %q does not match its complete pattern", name)
		}
	}
	return nil
}

func renderArgument(argument string, values map[string]string, slots map[string]compiledSlot) (string, error) {
	refs, err := slotReferences(argument)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		value := values[ref]
		if argument == "${"+ref+"}" && strings.HasPrefix(value, "-") && !slots[ref].limit.AllowLeadingHyphen {
			return "", fmt.Errorf("slot %q cannot select an option", ref)
		}
		argument = strings.ReplaceAll(argument, "${"+ref+"}", value)
	}
	return argument, nil
}

func slotReferences(argument string) ([]string, error) {
	var out []string
	for start := 0; ; {
		open := strings.Index(argument[start:], "${")
		if open < 0 {
			return out, nil
		}
		open += start
		close := strings.IndexByte(argument[open+2:], '}')
		if close < 0 {
			return nil, errors.New("unterminated slot placeholder")
		}
		close += open + 2
		name := argument[open+2 : close]
		if !validSlotName.MatchString(name) || len(name) > 64 {
			return nil, fmt.Errorf("invalid slot placeholder %q", name)
		}
		out = append(out, name)
		start = close + 1
	}
}

func isInterpreter(path string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	_, found := map[string]struct{}{"sh": {}, "bash": {}, "dash": {}, "zsh": {}, "fish": {}, "cmd": {}, "powershell": {}, "pwsh": {}, "python": {}, "python3": {}, "perl": {}, "ruby": {}, "node": {}, "php": {}, "java": {}, "wscript": {}, "cscript": {}}[name]
	return found
}

func validateID(kind, id string) error {
	if len(id) == 0 || len(id) > maxIDBytes || !validID.MatchString(id) {
		return fmt.Errorf("invalid %s ID %q", kind, id)
	}
	return nil
}

func (f CallbackField) valid() bool {
	switch f {
	case CallbackEvent, CallbackResourceID, CallbackResourceType, CallbackResourceGeneration, CallbackGraphEpoch:
		return true
	default:
		return false
	}
}

func callbackResourceType(value protocol.ResourceType) bool {
	switch value {
	case protocol.ResourceTun, protocol.ResourceDefaultTun, protocol.ResourceDNS:
		return true
	default:
		return false
	}
}

func environmentList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func cloneStrings(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneSlots(values map[string]Slot) map[string]Slot {
	out := make(map[string]Slot, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneCallbackFields(values map[string]CallbackField) map[string]CallbackField {
	out := make(map[string]CallbackField, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
