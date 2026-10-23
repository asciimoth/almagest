//go:build linux

package main

import (
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestLinuxDefaultExecutionModeIsDirect(t *testing.T) {
	mode, err := selectExecutionMode(args.Args{})
	if err != nil {
		t.Fatal(err)
	}
	if mode != executionModeDirect {
		t.Fatalf("selectExecutionMode() = %q, want %q", mode, executionModeDirect)
	}
}
