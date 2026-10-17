package policy

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	sandbox "github.com/asciimoth/gobox"
)

type testTempDir interface {
	Helper()
	TempDir() string
}

func validConfig(t testTempDir) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		Mode:          sandbox.Off,
		TemporaryRoot: filepath.Join(root, "temporary"),
		RegistryRoot:  filepath.Join(root, "registry"),
		Programs: map[sandbox.ProgramID]Program{
			"helper": {Executable: filepath.Join(root, "helper"), Builder: "fixed-v1", Arguments: []Argument{{Literal: "--flag"}, {Input: "value"}}, Inputs: map[string]Input{"value": {Kind: sandbox.TextValue, Required: true, Maximum: 32}}},
		},
		Templates: map[sandbox.TemplateID]Template{
			"test": {AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper"}, RuntimeProfile: "direct-v1", PersistentRoot: filepath.Join(root, "persistent")},
		},
	}
}

func TestNormalizeProtectedPolicy(t *testing.T) {
	input := validConfig(t)
	value := input.Templates["test"]
	value.ReadOnly = []ReadOnly{{Source: filepath.Join(filepath.Dir(input.TemporaryRoot), "source"), Target: "/input"}}
	input.Templates["test"] = value
	got, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.Instances != 32 || got.Templates["test"].Limits.Commands != 1 {
		t.Fatalf("defaults not applied: %+v", got)
	}
	input.Programs["helper"] = Program{}
	if got.Programs["helper"].Executable == "" {
		t.Fatal("normalized policy aliases caller maps")
	}
	value = input.Templates["test"]
	value.ReadOnly[0].Target = "/changed"
	input.Templates["test"] = value
	if got.Templates["test"].ReadOnly[0].Target != "/input" {
		t.Fatal("normalized policy aliases caller template slices")
	}
}

func TestNormalizeRequiredLimitRequiresResourceEnforcement(t *testing.T) {
	input := validConfig(t)
	input.Mode = sandbox.Auto
	template := input.Templates["test"]
	template.Limits.Required = []string{"Tasks", "TemporaryBytes"}
	input.Templates["test"] = template
	got, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Templates["test"].RequiredFeatures, sandbox.ResourceLimits) {
		t.Fatal("required native limit did not require resource enforcement")
	}
}

func TestTemplateLimitsRejectRemovedPersistentBytes(t *testing.T) {
	var limits TemplateLimits
	if err := json.Unmarshal([]byte(`{"PersistentBytes": 1024}`), &limits); err == nil {
		t.Fatal("removed PersistentBytes field was accepted")
	}
}

func TestNormalizeOffDoesNotRequireUnusedLinuxLauncher(t *testing.T) {
	input := validConfig(t)
	template := input.Templates["test"]
	template.RuntimeProfile = "linux-landlock-v1"
	input.Templates["test"] = template
	if _, err := Normalize(input); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeAcceptsWindowsNativeProfile(t *testing.T) {
	input := validConfig(t)
	template := input.Templates["test"]
	template.RuntimeProfile = "windows-native-v1"
	input.Templates["test"] = template
	got, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Templates["test"].RuntimeProfile != "windows-native-v1" {
		t.Fatalf("runtime profile = %q", got.Templates["test"].RuntimeProfile)
	}
}

func TestNormalizeRejectsPolicyAuthorityShifts(t *testing.T) {
	tests := map[string]func(*Config){
		"relative executable": func(c *Config) { p := c.Programs["helper"]; p.Executable = "helper"; c.Programs["helper"] = p },
		"raw builder":         func(c *Config) { p := c.Programs["helper"]; p.Builder = "raw"; c.Programs["helper"] = p },
		"unknown input": func(c *Config) {
			p := c.Programs["helper"]
			p.Arguments = append(p.Arguments, Argument{Input: "other"})
			c.Programs["helper"] = p
		},
		"optional substitution": func(c *Config) {
			p := c.Programs["helper"]
			p.Inputs["value"] = Input{Kind: sandbox.TextValue}
			c.Programs["helper"] = p
		},
		"root overlap":         func(c *Config) { v := c.Templates["test"]; v.PersistentRoot = c.TemporaryRoot; c.Templates["test"] = v },
		"nested service roots": func(c *Config) { c.RegistryRoot = filepath.Join(c.TemporaryRoot, "registry") },
		"reserved read-only target": func(c *Config) {
			v := c.Templates["test"]
			v.ReadOnly = []ReadOnly{{Source: filepath.Join(filepath.Dir(c.TemporaryRoot), "source"), Target: "/almagest/temp/input"}}
			c.Templates["test"] = v
		},
		"conflicting Linux identity": func(c *Config) {
			v := c.Templates["test"]
			v.Linux = Linux{IdentityPool: "pool", UID: 1000, GID: 1000}
			c.Templates["test"] = v
		},
		"unknown runtime profile": func(c *Config) { v := c.Templates["test"]; v.RuntimeProfile = "unknown-v1"; c.Templates["test"] = v },
		"duplicate class": func(c *Config) {
			v := c.Templates["test"]
			v.AllowedClasses = append(v.AllowedClasses, v.AllowedClasses[0])
			c.Templates["test"] = v
		},
		"reserved environment": func(c *Config) {
			v := c.Templates["test"]
			v.Environment.Set = map[string]string{"LD_PRELOAD": "x"}
			c.Templates["test"] = v
		},
		"credential environment": func(c *Config) {
			v := c.Templates["test"]
			v.Environment.Inherit = []string{"SERVICE_TOKEN"}
			c.Templates["test"] = v
		},
		"unknown program": func(c *Config) {
			v := c.Templates["test"]
			v.Programs = []sandbox.ProgramID{"missing"}
			c.Templates["test"] = v
		},
		"off required feature": func(c *Config) {
			v := c.Templates["test"]
			v.RequiredFeatures = []sandbox.Feature{sandbox.PrivateNetwork}
			c.Templates["test"] = v
		},
		"unknown required limit": func(c *Config) {
			v := c.Templates["test"]
			v.Limits.Required = []string{"Unknown"}
			c.Templates["test"] = v
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validConfig(t)
			mutate(&input)
			if _, err := Normalize(input); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestNormalizeRejectsDescendantCycles(t *testing.T) {
	input := validConfig(t)
	p := input.Programs["helper"]
	p.Descendants = []sandbox.ProgramID{"child"}
	input.Programs["helper"] = p
	input.Programs["child"] = Program{Executable: p.Executable, Builder: "fixed-v1", Descendants: []sandbox.ProgramID{"helper"}}
	v := input.Templates["test"]
	v.Programs = append(v.Programs, "child")
	input.Templates["test"] = v
	if _, err := Normalize(input); err == nil {
		t.Fatal("descendant cycle accepted")
	}
}

func FuzzDecodeProtectedPolicy(f *testing.F) {
	valid := validConfig(f)
	encoded, err := json.Marshal(valid)
	if err != nil {
		f.Fatal(err)
	}
	for _, input := range [][]byte{
		encoded,
		{},
		[]byte(`null`),
		[]byte(`{"Mode":"off","Mode":"strict"}`),
		[]byte(`{"Unknown":true}`),
		append(append([]byte{}, encoded...), []byte(` true`)...),
	} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		got, err := Decode(input)
		if err != nil {
			return
		}
		canonical, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal accepted policy: %v", err)
		}
		again, err := Decode(canonical)
		if err != nil {
			t.Fatalf("decode canonical policy: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("policy normalization is not stable: first=%+v second=%+v", got, again)
		}
	})
}
