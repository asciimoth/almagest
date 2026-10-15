package main

import (
	"runtime"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/internal/isolation/selection"
)

type executionMode string

const (
	executionModeDirect               executionMode = "direct"
	executionModeIsolation            executionMode = "isolation"
	executionModeIsolationDevelopment executionMode = "isolation-development"
)

func selectExecutionMode(arguments args.Args) (executionMode, error) {
	return selectExecutionModeForPlatform(arguments, selection.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
}

func selectExecutionModeForPlatform(arguments args.Args, platform selection.Platform) (executionMode, error) {
	policy, err := selection.ResolvePolicy(arguments, platform)
	if err != nil {
		return "", err
	}
	if policy != selection.PolicyOff {
		return executionModeIsolation, nil
	}
	return executionModeDirect, nil
}
