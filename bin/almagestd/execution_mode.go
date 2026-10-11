package main

import (
	"fmt"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

type executionMode string

const (
	executionModeDirect               executionMode = "direct"
	executionModeIsolation            executionMode = "isolation"
	executionModeIsolationDevelopment executionMode = "isolation-development"
)

func selectExecutionMode(arguments args.Args) (executionMode, error) {
	if arguments.Experimental && !arguments.NoIsolation {
		return "", fmt.Errorf("--experimental requires explicit --no-isolation")
	}
	if !arguments.NoIsolation {
		return executionModeIsolation, nil
	}
	return executionModeDirect, nil
}
