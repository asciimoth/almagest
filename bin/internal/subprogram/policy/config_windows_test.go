//go:build windows

package policy

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/lib/sandbox"
)

func TestNormalizeRejectsWindowsAuthorityPathAliases(t *testing.T) {
	tests := map[string]func(*Config){
		"short executable alias": func(c *Config) {
			program := c.Programs["helper"]
			program.Executable = `C:\PROGRA~1\helper.exe`
			c.Programs["helper"] = program
		},
		"alternate data stream": func(c *Config) {
			program := c.Programs["helper"]
			program.Executable = `C:\program\helper.exe:stream`
			c.Programs["helper"] = program
		},
		"UNC root":    func(c *Config) { c.TemporaryRoot = `\\server\share\temporary` },
		"device root": func(c *Config) { c.TemporaryRoot = `\\?\C:\temporary` },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			config := validConfig(t)
			config.TemporaryRoot = filepath.Join(root, "temporary")
			config.RegistryRoot = filepath.Join(root, "registry")
			program := config.Programs["helper"]
			program.Executable = filepath.Join(root, "helper.exe")
			config.Programs["helper"] = program
			template := config.Templates["test"]
			template.PersistentRoot = filepath.Join(root, "persistent")
			config.Templates["test"] = template
			mutate(&config)
			if _, err := Normalize(config); !errors.Is(err, sandbox.ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
