//go:build !windows

package main

import (
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestNonWindowsDefaultExecutionModeIsIsolation(t *testing.T) {
	mode, err := selectExecutionMode(args.Args{})
	if err != nil {
		t.Fatal(err)
	}
	if mode != executionModeIsolation {
		t.Fatalf("selectExecutionMode() = %q, want %q", mode, executionModeIsolation)
	}
}
