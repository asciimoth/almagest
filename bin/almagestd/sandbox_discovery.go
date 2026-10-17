package main

import (
	"fmt"
	"os"
	"path/filepath"

	sandboxdiscovery "github.com/asciimoth/almagest/bin/internal/subprogram/discovery"
	sandboxpolicy "github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/kataras/golog"
)

func prepareSystemProgramPolicy(conf *Config, stateRoot string) error {
	launcher := ""
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "almagest-subprogram-launcher")
		if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
			launcher = resolved
		}
	}
	configured, found, err := sandboxdiscovery.Configure(conf.Protected.Sandbox, sandboxdiscovery.Options{
		StateRoot: sandboxdiscovery.DefaultStateRoot(stateRoot), LinuxLauncher: launcher,
	})
	if err != nil {
		golog.Warnf("System sandbox program discovery failed; explicit sandbox policy remains active: %v", err)
		return nil
	}
	conf.Protected.Sandbox = configured
	for _, item := range found {
		golog.Debugf("Discovered sandbox program %s at %s", item.Program, item.Executable)
	}
	for id, reason := range configured.UnavailableTemplates {
		golog.Warnf("Sandbox template %s is unavailable: %s", id, reason)
	}
	for id, reason := range configured.UnavailablePrograms {
		golog.Warnf("Sandbox program %s is unavailable: %s", id, reason)
	}
	if len(configured.Programs) == 0 && len(configured.Templates) == 0 && len(configured.UnavailableTemplates) == 0 {
		return nil
	}
	normalized, err := sandboxpolicy.Normalize(configured)
	if err != nil {
		return fmt.Errorf("protected sandbox config: %w", err)
	}
	conf.Protected.Sandbox = normalized
	return nil
}
