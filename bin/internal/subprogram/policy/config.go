package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/asciimoth/almagest/lib/sandbox"
)

// Decode parses one standardized protected Sandbox object with strict field
// handling, then returns the normalized immutable policy.
func Decode(data []byte) (Config, error) {
	var value Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("%w: decode sandbox policy: %v", sandbox.ErrInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, fmt.Errorf("%w: trailing sandbox policy data", sandbox.ErrInvalid)
	}
	return Normalize(value)
}

// Config is the normalized, protected sandbox policy. Paths and launch rules
// are host authority and must never be constructed from graph input.
type Config struct {
	Mode            sandbox.Mode
	TemporaryRoot   string
	RegistryRoot    string
	LinuxLauncher   string
	LinuxCgroupRoot string
	Limits          ServiceLimits
	Programs        map[sandbox.ProgramID]Program
	Templates       map[sandbox.TemplateID]Template
}

type ServiceLimits struct {
	Instances  int
	Commands   int
	RelayBytes int64
	Receipts   int
}

type Program struct {
	Executable  string
	Builder     string
	Arguments   []Argument
	Inputs      map[string]Input
	Descendants []sandbox.ProgramID
}

type Argument struct {
	Literal string
	Input   string
}

type Input struct {
	Kind     sandbox.ValueKind
	Required bool
	Maximum  int64
}

type Template struct {
	AllowedClasses            []string
	Programs                  []sandbox.ProgramID
	RuntimeProfile            string
	PersistentRoot            string
	DeleteStateOnOwnerRemoval bool
	RequiredFeatures          []sandbox.Feature
	ReadOnly                  []ReadOnly
	Environment               Environment
	Linux                     Linux
	Limits                    TemplateLimits
}

type ReadOnly struct {
	Source string
	Target string
}

type Linux struct {
	IdentityPool string
	UID          uint32
	GID          uint32
	CurrentUser  bool
}

type Environment struct {
	Inherit []string
	Set     map[string]string
}

type TemplateLimits struct {
	Commands       int
	Tasks          int
	Files          int
	Sockets        int
	MemoryBytes    int64
	CPUPercent     int
	TransferBytes  int64
	TemporaryBytes int64
	OutputBytes    int64
	Required       []string
}

func (limits *TemplateLimits) UnmarshalJSON(data []byte) error {
	type plainTemplateLimits TemplateLimits
	var decoded plainTemplateLimits
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*limits = TemplateLimits(decoded)
	return nil
}

// Normalize validates policy and returns an immutable deep copy with finite
// defaults. It does not probe platform support or create host resources.
func Normalize(input Config) (Config, error) {
	if input.Mode == "" {
		input.Mode = sandbox.Auto
	}
	if input.Mode != sandbox.Off && input.Mode != sandbox.Auto && input.Mode != sandbox.Strict {
		return Config{}, fmt.Errorf("%w: sandbox mode %q", sandbox.ErrInvalid, input.Mode)
	}
	if !rooted(input.TemporaryRoot) || !rooted(input.RegistryRoot) || pathsOverlap(input.TemporaryRoot, input.RegistryRoot) {
		return Config{}, fmt.Errorf("%w: sandbox service roots", sandbox.ErrInvalid)
	}
	if input.Limits.Instances == 0 {
		input.Limits.Instances = 32
	}
	if input.Limits.Commands == 0 {
		input.Limits.Commands = 64
	}
	if input.Limits.RelayBytes == 0 {
		input.Limits.RelayBytes = 16 << 20
	}
	if input.Limits.Receipts == 0 {
		input.Limits.Receipts = 4096
	}
	if input.Limits.Instances < 1 || input.Limits.Instances > 65536 || input.Limits.Commands < 1 || input.Limits.Commands > 65536 || input.Limits.RelayBytes < 4096 || input.Limits.RelayBytes > 1<<30 || input.Limits.Receipts < 1 || input.Limits.Receipts > 1<<20 {
		return Config{}, fmt.Errorf("%w: sandbox service limits", sandbox.ErrInvalid)
	}
	if len(input.Programs) == 0 || len(input.Programs) > 1024 || len(input.Templates) == 0 || len(input.Templates) > 1024 {
		return Config{}, fmt.Errorf("%w: sandbox programs and templates are required", sandbox.ErrInvalid)
	}

	out := input
	out.Programs = make(map[sandbox.ProgramID]Program, len(input.Programs))
	for id, program := range input.Programs {
		if err := sandbox.ValidateName(string(id)); err != nil {
			return Config{}, fmt.Errorf("%w: program ID %q", sandbox.ErrInvalid, id)
		}
		if !rooted(program.Executable) || program.Builder != "fixed-v1" {
			return Config{}, fmt.Errorf("%w: program %q executable or builder", sandbox.ErrInvalid, id)
		}
		if len(program.Arguments) > 256 || len(program.Inputs) > 128 || len(program.Descendants) > 64 {
			return Config{}, fmt.Errorf("%w: program %q bounds", sandbox.ErrInvalid, id)
		}
		program.Arguments = slices.Clone(program.Arguments)
		program.Inputs = maps.Clone(program.Inputs)
		program.Descendants = slices.Clone(program.Descendants)
		used := map[string]bool{}
		for name, spec := range program.Inputs {
			if sandbox.ValidateName(name) != nil || !validValueKind(spec.Kind) || spec.Maximum < 0 || spec.Maximum > 1<<20 {
				return Config{}, fmt.Errorf("%w: program %q input %q", sandbox.ErrInvalid, id, name)
			}
		}
		for _, argument := range program.Arguments {
			if (argument.Literal == "") == (argument.Input == "") || len(argument.Literal) > 1<<16 || !utf8.ValidString(argument.Literal) || strings.IndexByte(argument.Literal, 0) >= 0 {
				return Config{}, fmt.Errorf("%w: program %q argument", sandbox.ErrInvalid, id)
			}
			if argument.Input != "" {
				spec, ok := program.Inputs[argument.Input]
				if !ok || !spec.Required {
					return Config{}, fmt.Errorf("%w: program %q unknown input %q", sandbox.ErrInvalid, id, argument.Input)
				}
				used[argument.Input] = true
			}
		}
		for name := range program.Inputs {
			if !used[name] {
				return Config{}, fmt.Errorf("%w: program %q unused input %q", sandbox.ErrInvalid, id, name)
			}
		}
		out.Programs[id] = program
	}
	for id, program := range out.Programs {
		seen := map[sandbox.ProgramID]bool{}
		for _, descendant := range program.Descendants {
			if descendant == id || seen[descendant] || out.Programs[descendant].Executable == "" {
				return Config{}, fmt.Errorf("%w: program %q descendant %q", sandbox.ErrInvalid, id, descendant)
			}
			seen[descendant] = true
		}
	}
	if hasDescendantCycle(out.Programs) {
		return Config{}, fmt.Errorf("%w: recursive program descendants", sandbox.ErrInvalid)
	}

	out.Templates = make(map[sandbox.TemplateID]Template, len(input.Templates))
	writableRoots := []string{input.TemporaryRoot, input.RegistryRoot}
	for id, template := range input.Templates {
		if sandbox.ValidateName(string(id)) != nil || len(template.AllowedClasses) == 0 || len(template.Programs) == 0 ||
			template.RuntimeProfile != "direct-v1" && template.RuntimeProfile != "linux-landlock-v1" && template.RuntimeProfile != "windows-native-v1" {
			return Config{}, fmt.Errorf("%w: template %q", sandbox.ErrInvalid, id)
		}
		if template.PersistentRoot != "" && (!rooted(template.PersistentRoot) || overlapsAny(template.PersistentRoot, input.TemporaryRoot, input.RegistryRoot)) {
			return Config{}, fmt.Errorf("%w: template %q persistent root", sandbox.ErrInvalid, id)
		}
		if template.PersistentRoot != "" && overlapsAny(template.PersistentRoot, writableRoots...) {
			return Config{}, fmt.Errorf("%w: template %q overlaps another writable root", sandbox.ErrInvalid, id)
		}
		template.AllowedClasses = slices.Clone(template.AllowedClasses)
		template.Programs = slices.Clone(template.Programs)
		template.RequiredFeatures = slices.Clone(template.RequiredFeatures)
		template.ReadOnly = slices.Clone(template.ReadOnly)
		template.Environment.Inherit = slices.Clone(template.Environment.Inherit)
		template.Environment.Set = maps.Clone(template.Environment.Set)
		template.Limits.Required = slices.Clone(template.Limits.Required)
		if err := normalizeTemplate(&template, out.Programs); err != nil {
			return Config{}, fmt.Errorf("template %q: %w", id, err)
		}
		if input.Mode == sandbox.Off && len(template.RequiredFeatures) != 0 {
			return Config{}, fmt.Errorf("%w: template %q requires features in off mode", sandbox.ErrDenied, id)
		}
		out.Templates[id] = template
		if template.PersistentRoot != "" {
			writableRoots = append(writableRoots, template.PersistentRoot)
		}
	}
	usesLinuxLauncher := false
	for _, template := range out.Templates {
		usesLinuxLauncher = usesLinuxLauncher || input.Mode != sandbox.Off && template.RuntimeProfile == "linux-landlock-v1"
	}
	if usesLinuxLauncher && !rooted(input.LinuxLauncher) {
		return Config{}, fmt.Errorf("%w: Linux launcher is required", sandbox.ErrInvalid)
	}
	if input.LinuxLauncher != "" && (!rooted(input.LinuxLauncher) || overlapsAny(input.LinuxLauncher, writableRoots...)) {
		return Config{}, fmt.Errorf("%w: Linux launcher", sandbox.ErrInvalid)
	}
	if input.LinuxCgroupRoot != "" && (!rooted(input.LinuxCgroupRoot) ||
		input.LinuxCgroupRoot != filepath.Clean(input.LinuxCgroupRoot) ||
		input.LinuxCgroupRoot != "/sys/fs/cgroup" && !strings.HasPrefix(input.LinuxCgroupRoot, "/sys/fs/cgroup/")) {
		return Config{}, fmt.Errorf("%w: Linux cgroup root", sandbox.ErrInvalid)
	}
	for id, program := range out.Programs {
		if overlapsAny(program.Executable, writableRoots...) {
			return Config{}, fmt.Errorf("%w: program %q overlaps writable storage", sandbox.ErrInvalid, id)
		}
	}
	for id, template := range out.Templates {
		for _, input := range template.ReadOnly {
			if overlapsAny(input.Source, writableRoots...) {
				return Config{}, fmt.Errorf("%w: template %q read-only source overlaps writable storage", sandbox.ErrInvalid, id)
			}
		}
	}
	return out, nil
}

func normalizeTemplate(template *Template, programs map[sandbox.ProgramID]Program) error {
	if len(template.ReadOnly) > 128 || len(template.AllowedClasses) > 256 || len(template.Programs) > 256 || len(template.Environment.Inherit) > 128 || len(template.Environment.Set) > 128 {
		return fmt.Errorf("%w: template collection bounds", sandbox.ErrInvalid)
	}
	allowed := map[sandbox.ProgramID]bool{}
	for _, id := range template.Programs {
		if allowed[id] || programs[id].Executable == "" {
			return fmt.Errorf("%w: unknown or duplicate program %q", sandbox.ErrInvalid, id)
		}
		allowed[id] = true
	}
	for id := range allowed {
		for _, descendant := range programs[id].Descendants {
			if !allowed[descendant] {
				return fmt.Errorf("%w: descendant %q is outside template", sandbox.ErrInvalid, descendant)
			}
		}
	}
	seenClasses := map[string]bool{}
	for _, class := range template.AllowedClasses {
		if class == "" || len(class) > 256 || !utf8.ValidString(class) || !strings.Contains(class, "/") || strings.ContainsFunc(class, unicode.IsControl) || seenClasses[class] {
			return fmt.Errorf("%w: allowed class", sandbox.ErrInvalid)
		}
		seenClasses[class] = true
	}
	if err := normalizeRequiredLimits(template); err != nil {
		return err
	}
	seenFeatures := map[sandbox.Feature]bool{}
	for _, feature := range template.RequiredFeatures {
		if !KnownFeature(feature) || seenFeatures[feature] {
			return fmt.Errorf("%w: required feature", sandbox.ErrInvalid)
		}
		seenFeatures[feature] = true
	}
	if err := normalizeReadOnly(template.ReadOnly); err != nil {
		return err
	}
	linuxSelectors := 0
	if template.Linux.IdentityPool != "" {
		linuxSelectors++
		if sandbox.ValidateName(template.Linux.IdentityPool) != nil {
			return fmt.Errorf("%w: Linux identity pool", sandbox.ErrInvalid)
		}
	}
	if template.Linux.UID != 0 || template.Linux.GID != 0 {
		linuxSelectors++
		if template.Linux.UID == 0 || template.Linux.GID == 0 || template.Linux.UID == ^uint32(0) || template.Linux.GID == ^uint32(0) {
			return fmt.Errorf("%w: Linux UID and GID", sandbox.ErrInvalid)
		}
	}
	if template.Linux.CurrentUser {
		linuxSelectors++
	}
	if linuxSelectors > 1 {
		return fmt.Errorf("%w: conflicting Linux identity selectors", sandbox.ErrInvalid)
	}
	seenEnv := map[string]bool{}
	for _, name := range template.Environment.Inherit {
		key := strings.ToUpper(name)
		if !validEnvironmentName(name) || reservedEnvironment(name) || seenEnv[key] {
			return fmt.Errorf("%w: inherited environment", sandbox.ErrInvalid)
		}
		seenEnv[key] = true
	}
	for name, value := range template.Environment.Set {
		key := strings.ToUpper(name)
		if !validEnvironmentName(name) || reservedEnvironment(name) || seenEnv[key] || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len(value) > 1<<16 {
			return fmt.Errorf("%w: fixed environment", sandbox.ErrInvalid)
		}
		seenEnv[key] = true
	}
	limits := &template.Limits
	if limits.Commands == 0 {
		limits.Commands = 1
	}
	if limits.Tasks == 0 {
		limits.Tasks = 64
	}
	if limits.Files == 0 {
		limits.Files = 64
	}
	if limits.Sockets == 0 {
		limits.Sockets = 512
	}
	if limits.MemoryBytes == 0 {
		limits.MemoryBytes = 512 << 20
	}
	if limits.CPUPercent == 0 {
		limits.CPUPercent = 100
	}
	if limits.TransferBytes == 0 {
		limits.TransferBytes = 8 << 20
	}
	if limits.TemporaryBytes == 0 {
		limits.TemporaryBytes = 64 << 20
	}
	if limits.OutputBytes == 0 {
		limits.OutputBytes = 8 << 20
	}
	if limits.Commands < 1 || limits.Commands > 1024 || limits.Tasks < 1 || limits.Tasks > 4096 || limits.Files < 1 || limits.Files > 65536 || limits.Sockets < 1 || limits.Sockets > 65536 || limits.MemoryBytes < 32<<20 || limits.MemoryBytes > 1<<40 || limits.CPUPercent < 1 || limits.CPUPercent > 100 || limits.TransferBytes < 1 || limits.TransferBytes > 1<<30 || limits.TemporaryBytes < 1 || limits.TemporaryBytes > 1<<50 || limits.OutputBytes < 1 || limits.OutputBytes > 1<<30 {
		return fmt.Errorf("%w: limits", sandbox.ErrInvalid)
	}
	return nil
}

func normalizeRequiredLimits(template *Template) error {
	known := map[string]bool{
		"Tasks": true, "Files": true, "MemoryBytes": true, "CPUPercent": true,
		"TemporaryBytes": true,
	}
	seen := map[string]bool{}
	for _, name := range template.Limits.Required {
		if !known[name] || seen[name] {
			return fmt.Errorf("%w: required limit %q", sandbox.ErrInvalid, name)
		}
		seen[name] = true
	}
	if len(seen) != 0 && !slices.Contains(template.RequiredFeatures, sandbox.ResourceLimits) {
		template.RequiredFeatures = append(template.RequiredFeatures, sandbox.ResourceLimits)
	}
	return nil
}

func normalizeReadOnly(inputs []ReadOnly) error {
	targets := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if !rooted(input.Source) || !validSandboxTarget(input.Target) {
			return fmt.Errorf("%w: read-only input", sandbox.ErrInvalid)
		}
		for _, reserved := range []string{"/almagest/temp", "/almagest/state", "/almagest/program", "/proc", "/dev"} {
			if sandboxTargetsOverlap(input.Target, reserved) {
				return fmt.Errorf("%w: read-only target overlaps reserved path", sandbox.ErrInvalid)
			}
		}
		for _, target := range targets {
			if sandboxTargetsOverlap(input.Target, target) {
				return fmt.Errorf("%w: read-only targets overlap", sandbox.ErrInvalid)
			}
		}
		targets = append(targets, input.Target)
	}
	return nil
}

func validSandboxTarget(target string) bool {
	if len(target) < 2 || len(target) > 4096 || target[0] != '/' || !utf8.ValidString(target) || strings.ContainsRune(target, 0) {
		return false
	}
	for _, part := range strings.Split(target[1:], "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.ContainsAny(part, `\:`) || strings.ContainsFunc(part, unicode.IsControl) {
			return false
		}
	}
	return true
}

func sandboxTargetsOverlap(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func validValueKind(kind sandbox.ValueKind) bool {
	switch kind {
	case sandbox.TextValue, sandbox.IntegerValue, sandbox.BooleanValue, sandbox.PathValue, sandbox.ProgramValue, sandbox.EndpointValue:
		return true
	default:
		return false
	}
}

func validEnvironmentName(name string) bool {
	if name == "" || len(name) > 128 || name[0] == '=' {
		return false
	}
	for _, char := range name {
		if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func reservedEnvironment(name string) bool {
	switch strings.ToUpper(name) {
	case "ALMAGEST_SANDBOX_TEMP", "ALMAGEST_SANDBOX_STATE", "TMPDIR", "TMP", "TEMP", "HOME", "PATH",
		"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
		"BASH_ENV", "ENV", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_AUTH_SOCK", "PYTHONPATH", "PYTHONHOME", "NODE_OPTIONS",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN":
		return true
	default:
		return strings.HasSuffix(strings.ToUpper(name), "_PASSWORD") || strings.HasSuffix(strings.ToUpper(name), "_SECRET") || strings.HasSuffix(strings.ToUpper(name), "_TOKEN")
	}
}

func hasDescendantCycle(programs map[sandbox.ProgramID]Program) bool {
	state := map[sandbox.ProgramID]uint8{}
	var visit func(sandbox.ProgramID) bool
	visit = func(id sandbox.ProgramID) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, next := range programs[id].Descendants {
			if visit(next) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range programs {
		if visit(id) {
			return true
		}
	}
	return false
}

func rooted(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	if runtime.GOOS != "windows" {
		return true
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) {
		return false
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return false
	}
	relative := strings.TrimPrefix(path[len(volume):], `\`)
	// A tilde can name an 8.3 alias for a different lexical path. Reject it in
	// daemon-authority paths so overlap checks cannot be bypassed through a
	// short name.
	if strings.ContainsRune(relative, '~') {
		return false
	}
	relative = filepath.ToSlash(relative)
	return sandbox.ValidatePath(sandbox.Path{Root: sandbox.Temporary, Rel: relative}, true) == nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func overlapsAny(path string, others ...string) bool {
	for _, other := range others {
		if pathsOverlap(path, other) {
			return true
		}
	}
	return false
}

func pathsOverlap(a, b string) bool {
	if samePath(a, b) {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
