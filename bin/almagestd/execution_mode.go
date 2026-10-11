package main

import (
	"errors"
	"fmt"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

type executionMode string

const (
	executionModeDirect               executionMode = "direct"
	executionModeIsolationDevelopment executionMode = "isolation-development"
)

func selectExecutionMode(arguments args.Args) (executionMode, error) {
	if arguments.Experimental && !arguments.NoIsolation {
		return "", fmt.Errorf("--experimental requires explicit --no-isolation")
	}
	if !arguments.NoIsolation {
		return "", errors.New("isolation is not implemented yet; use --no-isolation to run in direct mode")
	}
	return executionModeDirect, nil
}
