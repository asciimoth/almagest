// Package discovery expands explicitly enabled built-in sandbox policies from
// programs installed on the daemon host. It does not execute discovered code.
package discovery

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

const (
	torProgram     sandbox.ProgramID  = "tor"
	obfsProgram    sandbox.ProgramID  = "obfs4"
	torTemplate    sandbox.TemplateID = "tor"
	torDriverClass                    = "almagest/TorDriver"
)

// Options supplies startup-owned paths and test seams. StateRoot must be an
// absolute private directory selected by the daemon, not graph input.
type Options struct {
	Enable        bool
	StateRoot     string
	LinuxLauncher string
	GOOS          string
	LookPath      func(string) (string, error)
	RuntimeInputs func(...string) ([]policy.ReadOnly, error)
	LinuxIdentity policy.Linux
}

// Found describes one host executable added to the effective policy.
type Found struct {
	Program    sandbox.ProgramID
	Executable string
}

// Configure adds the built-in Tor policy when discovery is explicitly
// enabled. Existing program and template definitions always take precedence.
// Host lookup and dependency inspection are best effort: a failure is stored
// on the affected template or program and is returned when that capability is
// used, not as a daemon startup error.
func Configure(input policy.Config, options Options) (policy.Config, []Found, error) {
	if !input.DiscoverSystemPrograms && !options.Enable {
		return input, nil, nil
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.RuntimeInputs == nil {
		options.RuntimeInputs = discoverRuntimeInputs
	}
	if options.LinuxIdentity == (policy.Linux{}) {
		options.LinuxIdentity = defaultLinuxIdentity()
	}

	out := input
	out.DiscoverSystemPrograms = true
	out.Programs = clonePrograms(input.Programs)
	out.Templates = cloneTemplates(input.Templates)
	out.UnavailableTemplates = cloneTemplateDiagnostics(input.UnavailableTemplates)
	out.UnavailablePrograms = cloneProgramDiagnostics(input.UnavailablePrograms)
	found := make([]Found, 0, 2)
	var discoveredInputs []policy.ReadOnly

	if out.TemporaryRoot == "" || out.RegistryRoot == "" {
		if options.StateRoot == "" || !filepath.IsAbs(options.StateRoot) {
			out.UnavailableTemplates[torTemplate] = "automatic Tor sandbox discovery failed: daemon state root is not absolute"
			return out, found, nil
		}
		if err := prepareStateRoot(options.StateRoot); err != nil {
			return input, nil, fmt.Errorf("prepare automatic sandbox state root: %w", err)
		}
	}
	if out.TemporaryRoot == "" {
		out.TemporaryRoot = filepath.Join(options.StateRoot, "temporary")
	}
	if out.RegistryRoot == "" {
		out.RegistryRoot = filepath.Join(options.StateRoot, "registry")
	}
	_, torDefined := out.Programs[torProgram]
	if !torDefined {
		path, err := findExecutable(options.LookPath, "tor")
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				out.UnavailableTemplates[torTemplate] = "automatic Tor sandbox discovery failed: executable \"tor\" was not found in the host search path"
			} else {
				out.UnavailableTemplates[torTemplate] = discoveryFailure("automatic Tor sandbox discovery failed: locate executable", err)
			}
			return out, found, nil
		}
		tor := policy.Program{
			Executable: path,
			Builder:    "fixed-v1",
			Inputs: map[string]policy.Input{
				"defaults-torrc": {Kind: sandbox.PathValue, Required: true},
				"torrc":          {Kind: sandbox.PathValue, Required: true},
			},
			Arguments: []policy.Argument{
				{Literal: "--defaults-torrc"}, {Input: "defaults-torrc"},
				{Literal: "-f"}, {Input: "torrc"},
			},
		}
		inputs, inputErr := options.RuntimeInputs(path)
		if inputErr != nil {
			out.UnavailableTemplates[torTemplate] = discoveryFailure("automatic Tor sandbox discovery failed: inspect Tor runtime dependencies", inputErr)
			return out, found, nil
		}
		out.Programs[torProgram] = tor
		found = append(found, Found{Program: torProgram, Executable: path})
		delete(out.UnavailableTemplates, torTemplate)
		discoveredInputs = appendUniqueInputs(discoveredInputs, inputs)
	}

	// Managed transports are optional. Prefer the current lyrebird name, but
	// retain compatibility with distributions that package obfs4proxy.
	if !torDefined {
		if _, exists := out.Programs[obfsProgram]; !exists {
			path, err := findExecutable(options.LookPath, "lyrebird", "obfs4proxy")
			if err != nil {
				if errors.Is(err, exec.ErrNotFound) {
					out.UnavailablePrograms[obfsProgram] = "automatic obfs4 sandbox discovery failed: neither \"lyrebird\" nor \"obfs4proxy\" was found in the host search path"
				} else {
					out.UnavailablePrograms[obfsProgram] = discoveryFailure("automatic obfs4 sandbox discovery failed: locate executable", err)
				}
			} else {
				inputs, inputErr := options.RuntimeInputs(path)
				if inputErr != nil {
					out.UnavailablePrograms[obfsProgram] = discoveryFailure("automatic obfs4 sandbox discovery failed: inspect runtime dependencies", inputErr)
				} else {
					out.Programs[obfsProgram] = policy.Program{Executable: path, Builder: "fixed-v1"}
					tor := out.Programs[torProgram]
					tor.Descendants = append(tor.Descendants, obfsProgram)
					out.Programs[torProgram] = tor
					found = append(found, Found{Program: obfsProgram, Executable: path})
					delete(out.UnavailablePrograms, obfsProgram)
					discoveredInputs = appendUniqueInputs(discoveredInputs, inputs)
				}
			}
		}
	}

	template, templateDefined := out.Templates[torTemplate]
	if !templateDefined {
		mode := out.Mode
		if mode == "" {
			mode = sandbox.Auto
		}
		if options.GOOS == "linux" && mode != sandbox.Off && out.LinuxLauncher == "" && options.LinuxLauncher == "" {
			out.UnavailableTemplates[torTemplate] = "automatic Tor sandbox discovery failed: almagest-subprogram-launcher was not found beside the daemon executable"
			if !torDefined {
				delete(out.Programs, torProgram)
				delete(out.Programs, obfsProgram)
				found = nil
			}
			return out, found, nil
		}
		if out.LinuxLauncher == "" && options.GOOS == "linux" {
			out.LinuxLauncher = options.LinuxLauncher
		}
		template = policy.Template{
			AllowedClasses: []string{torDriverClass},
			Programs:       []sandbox.ProgramID{torProgram},
			RuntimeProfile: runtimeProfile(options.GOOS),
			Linux:          options.LinuxIdentity,
			Limits: policy.TemplateLimits{
				Commands: 1, Tasks: 128, Files: 1024, Sockets: 1024,
				MemoryBytes: 8 << 30, CPUPercent: 100,
				TransferBytes: 8 << 20, TemporaryBytes: 1 << 30, OutputBytes: 8 << 20,
			},
		}
	}
	if _, exists := out.Programs[obfsProgram]; exists && !slices.Contains(template.Programs, obfsProgram) && !templateDefined {
		template.Programs = append(template.Programs, obfsProgram)
	}
	// Runtime inputs were discovered independently for each executable above.
	// This keeps an optional transport failure from disabling Tor itself.
	template.ReadOnly = appendUniqueInputs(template.ReadOnly, discoveredInputs)
	out.Templates[torTemplate] = template
	return out, found, nil
}

func findExecutable(lookPath func(string) (string, error), names ...string) (string, error) {
	for _, name := range names {
		path, err := lookPath(name)
		if errors.Is(err, exec.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		return filepath.Clean(path), nil
	}
	return "", exec.ErrNotFound
}

func discoveryFailure(operation string, err error) string {
	value := strings.ToValidUTF8(fmt.Sprintf("%s: %v", operation, err), "?")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	const maximum = 2048
	if len(value) > maximum {
		value = strings.ToValidUTF8(value[:maximum], "?") + "..."
	}
	return value
}

func runtimeProfile(goos string) string {
	switch goos {
	case "linux":
		return "linux-landlock-v1"
	case "windows":
		return "windows-native-v1"
	default:
		return "direct-v1"
	}
}

func clonePrograms(input map[sandbox.ProgramID]policy.Program) map[sandbox.ProgramID]policy.Program {
	out := make(map[sandbox.ProgramID]policy.Program, len(input)+2)
	for id, program := range input {
		program.Arguments = slices.Clone(program.Arguments)
		program.Descendants = slices.Clone(program.Descendants)
		program.Inputs = cloneInputs(program.Inputs)
		out[id] = program
	}
	return out
}

func cloneInputs(input map[string]policy.Input) map[string]policy.Input {
	if input == nil {
		return nil
	}
	out := make(map[string]policy.Input, len(input))
	for name, value := range input {
		out[name] = value
	}
	return out
}

func cloneTemplates(input map[sandbox.TemplateID]policy.Template) map[sandbox.TemplateID]policy.Template {
	out := make(map[sandbox.TemplateID]policy.Template, len(input)+1)
	for id, template := range input {
		template.AllowedClasses = slices.Clone(template.AllowedClasses)
		template.Programs = slices.Clone(template.Programs)
		template.RequiredFeatures = slices.Clone(template.RequiredFeatures)
		template.ReadOnly = slices.Clone(template.ReadOnly)
		template.Environment.Inherit = slices.Clone(template.Environment.Inherit)
		template.Limits.Required = slices.Clone(template.Limits.Required)
		out[id] = template
	}
	return out
}

func cloneTemplateDiagnostics(input map[sandbox.TemplateID]string) map[sandbox.TemplateID]string {
	out := make(map[sandbox.TemplateID]string, len(input)+1)
	for id, reason := range input {
		out[id] = reason
	}
	return out
}

func cloneProgramDiagnostics(input map[sandbox.ProgramID]string) map[sandbox.ProgramID]string {
	out := make(map[sandbox.ProgramID]string, len(input)+1)
	for id, reason := range input {
		out[id] = reason
	}
	return out
}

func appendUniqueInputs(current, additions []policy.ReadOnly) []policy.ReadOnly {
	targets := make(map[string]bool, len(current)+len(additions))
	for _, input := range current {
		targets[input.Target] = true
	}
	for _, input := range additions {
		if !targets[input.Target] {
			current = append(current, input)
			targets[input.Target] = true
		}
	}
	return current
}
